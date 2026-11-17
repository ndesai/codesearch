// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package indexer maintains incremental trigram indexes for the
// repositories listed in a codesearch configuration.
//
// Each repository is indexed into one or more shards, each covering a
// contiguous range of file paths. A manifest records every indexed
// file's size and modification time. On each run the indexer walks the
// repository (stat only), compares the result to the manifest, and then
// for each shard either keeps it, applies the changes as a delta (a small
// index of just the changed files, merged into the existing shard), or
// rebuilds it if most of it changed. Unchanged repositories cost only a
// directory walk; small edits to a huge tree such as AOSP rewrite only
// the affected shards.
package indexer

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ndesai/codesearch/index"
	"github.com/ndesai/codesearch/internal/config"
)

var errLocked = errors.New("another indexer is already running")

// ErrLocked is returned by Update when another process holds the index lock.
var ErrLocked = errLocked

// Options control an indexing run.
type Options struct {
	Full    bool                 // rebuild every shard from scratch
	Repos   []string             // only update these repos (default all)
	Wait    bool                 // wait for a concurrent indexer instead of failing
	Logf    func(string, ...any) // progress log (default log.Printf)
	Verbose bool                 // log details about each shard
	OnRepo  func(r *Result)      // called after each repository is updated
	Status  *Status              // if non-nil, updated with live progress
}

// Result describes the outcome of updating one repository.
type Result struct {
	Repo          string
	Files         int // files in the repository after the update
	Added         int
	Modified      int
	Deleted       int
	Shards        int // shards after the update
	ShardsKept    int
	ShardsDelta   int // shards updated by merging a delta
	ShardsRebuilt int
	Full          bool
	Duration      time.Duration
	Err           error
}

func (r *Result) Changed() bool {
	return r.Added+r.Modified+r.Deleted > 0 || r.ShardsRebuilt > 0
}

func (r *Result) String() string {
	if r.Err != nil {
		return fmt.Sprintf("%s: error: %v", r.Repo, r.Err)
	}
	if !r.Changed() {
		return fmt.Sprintf("%s: up to date (%d files, %d shards, %.1fs)", r.Repo, r.Files, r.Shards, r.Duration.Seconds())
	}
	kind := "incremental"
	if r.Full {
		kind = "full"
	}
	return fmt.Sprintf("%s: %s update: +%d ~%d -%d files; %d shards (%d kept, %d delta, %d rebuilt); %d files total; %.1fs",
		r.Repo, kind, r.Added, r.Modified, r.Deleted, r.Shards, r.ShardsKept, r.ShardsDelta, r.ShardsRebuilt, r.Files, r.Duration.Seconds())
}

// Status reports live indexing progress. It is safe for concurrent use.
type Status struct {
	mu      sync.Mutex
	running bool
	repo    string
	phase   string
	started time.Time
	last    map[string]*Result

	Walked  atomic.Int64 // files seen by the current walk
	Indexed atomic.Int64 // files indexed in the current repo
	ToIndex atomic.Int64 // files to index in the current repo
}

// StatusSnapshot is a point-in-time copy of Status.
type StatusSnapshot struct {
	Running bool              `json:"running"`
	Repo    string            `json:"repo,omitempty"`
	Phase   string            `json:"phase,omitempty"`
	Walked  int64             `json:"walked,omitempty"`
	Indexed int64             `json:"indexed,omitempty"`
	ToIndex int64             `json:"toIndex,omitempty"`
	Elapsed float64           `json:"elapsed,omitempty"`
	Last    map[string]string `json:"last,omitempty"`
}

func (s *Status) set(repo, phase string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.repo, s.phase = repo, phase
	s.mu.Unlock()
}

// Snapshot returns the current progress.
func (s *Status) Snapshot() StatusSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := StatusSnapshot{Running: s.running, Repo: s.repo, Phase: s.phase, Last: map[string]string{}}
	if s.running {
		snap.Walked = s.Walked.Load()
		snap.Indexed = s.Indexed.Load()
		snap.ToIndex = s.ToIndex.Load()
		snap.Elapsed = time.Since(s.started).Seconds()
	}
	for k, r := range s.last {
		snap.Last[k] = r.String()
	}
	return snap
}

// RepoDir returns the directory holding the index for the named repo.
func RepoDir(cfg *config.Config, name string) string {
	return filepath.Join(cfg.IndexDir, "repos", name)
}

// ManifestFile returns the manifest file for the named repo.
func ManifestFile(cfg *config.Config, name string) string {
	return filepath.Join(RepoDir(cfg, name), "manifest")
}

// updateMu serializes updates within a process, since the index
// package's temporary directory is a global setting.
var updateMu sync.Mutex

// Update brings the index for the configured repositories up to date.
func Update(cfg *config.Config, opts Options) ([]*Result, error) {
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	updateMu.Lock()
	defer updateMu.Unlock()
	if err := os.MkdirAll(filepath.Join(cfg.IndexDir, "tmp"), 0755); err != nil {
		return nil, err
	}
	unlock, err := lockFile(filepath.Join(cfg.IndexDir, "lock"), opts.Wait)
	if err != nil {
		return nil, err
	}
	defer unlock()

	index.TempDir = filepath.Join(cfg.IndexDir, "tmp")
	cleanTemp(filepath.Join(cfg.IndexDir, "tmp"))

	if st := opts.Status; st != nil {
		st.mu.Lock()
		st.running = true
		st.started = time.Now()
		if st.last == nil {
			st.last = map[string]*Result{}
		}
		st.mu.Unlock()
		defer func() {
			st.mu.Lock()
			st.running = false
			st.repo, st.phase = "", ""
			st.mu.Unlock()
		}()
	}

	want := map[string]bool{}
	for _, n := range opts.Repos {
		if cfg.Repo(n) == nil {
			return nil, fmt.Errorf("unknown repo %q", n)
		}
		want[n] = true
	}
	var results []*Result
	for _, r := range cfg.Repos {
		if len(want) > 0 && !want[r.Name] {
			continue
		}
		res := updateRepo(cfg, r, opts)
		results = append(results, res)
		if st := opts.Status; st != nil {
			st.mu.Lock()
			st.last[r.Name] = res
			st.mu.Unlock()
		}
		if opts.OnRepo != nil {
			opts.OnRepo(res)
		}
	}
	if len(want) == 0 {
		removeStaleRepos(cfg, opts.Logf)
	}
	return results, nil
}

// cleanTemp removes temporary files left behind by an interrupted run.
func cleanTemp(dir string) {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		os.Remove(filepath.Join(dir, e.Name()))
	}
}

// removeStaleRepos deletes index data for repos no longer in the config.
func removeStaleRepos(cfg *config.Config, logf func(string, ...any)) {
	dir := filepath.Join(cfg.IndexDir, "repos")
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.IsDir() && cfg.Repo(e.Name()) == nil {
			logf("removing index for %s (no longer in config)", e.Name())
			os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// A shardPlan describes how to produce one shard of the new index.
type shardPlan struct {
	start   string
	files   []FileInfo // all files in the new shard's range
	old     *Shard     // existing shard to keep or merge into (nil for rebuild)
	touched []touched  // for delta: changed and deleted paths, sorted
	result  Shard
	err     error
}

type touched struct {
	path    string
	deleted bool
}

func updateRepo(cfg *config.Config, repo *config.Repo, opts Options) (res *Result) {
	start := time.Now()
	res = &Result{Repo: repo.Name}
	defer func() {
		res.Duration = time.Since(start)
		if e := recover(); e != nil {
			res.Err = fmt.Errorf("panic: %v\n%s", e, debug.Stack())
		}
	}()
	logf := opts.Logf
	dir := RepoDir(cfg, repo.Name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		res.Err = err
		return
	}
	mfile := ManifestFile(cfg, repo.Name)
	old, err := ReadManifest(mfile)
	if err != nil {
		logf("%v; rebuilding", err)
		old = nil
	}
	fp := cfg.Fingerprint(repo)
	full := opts.Full || old == nil || old.Fingerprint != fp || old.Root != repo.Path
	if old != nil && !full {
		for _, s := range old.Shards {
			if _, err := os.Stat(filepath.Join(dir, s.File)); err != nil {
				logf("%s: shard %s missing; rebuilding", repo.Name, s.File)
				full = true
				break
			}
		}
	}
	res.Full = full

	st := opts.Status
	st.set(repo.Name, "scanning")
	var walked *atomic.Int64
	if st != nil {
		st.Walked.Store(0)
		st.Indexed.Store(0)
		st.ToIndex.Store(0)
		walked = &st.Walked
	}
	files, err := walk(repo.Path, repo.Excludes, cfg.Gitignore(), int64(cfg.MaxFileSize), max(8, runtime.NumCPU()), walked, func(f string, a ...any) {
		if opts.Verbose {
			logf(f, a...)
		}
	})
	if err != nil {
		res.Err = err
		return
	}
	res.Files = len(files)

	var plans []*shardPlan
	if full {
		res.Added = len(files)
		if old != nil {
			res.Added, res.Modified, res.Deleted = diffCounts(old.Files, files)
		}
		plans = splitPlans("", files, cfg.ShardFiles)
	} else {
		plans = planIncremental(old, files, cfg.ShardFiles, res)
		if res.Added+res.Modified+res.Deleted == 0 && len(plans) == len(old.Shards) {
			// Nothing changed. Leave the manifest alone so readers
			// don't reload, unless metadata like mtimes needs refreshing.
			res.Shards = len(old.Shards)
			res.ShardsKept = len(old.Shards)
			return
		}
	}

	gen := 1
	if old != nil {
		gen = old.Generation + 1
	}
	if st != nil {
		var n int64
		for _, p := range plans {
			if p.old == nil {
				n += int64(len(p.files))
			} else {
				for _, t := range p.touched {
					if !t.deleted {
						n++
					}
				}
			}
		}
		st.ToIndex.Store(n)
	}
	st.set(repo.Name, "indexing")
	if res.Added+res.Modified+res.Deleted > 0 || full {
		logf("%s: indexing %d new, %d modified, %d deleted files", repo.Name, res.Added, res.Modified, res.Deleted)
	}

	// Build shards in parallel.
	var wg sync.WaitGroup
	sem := make(chan struct{}, cfg.Parallelism)
	for i, p := range plans {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			name := fmt.Sprintf("%06d-%04d.idx", gen, i)
			p.err = buildShard(repo.Path, dir, name, p, st)
			if p.err != nil && p.old != nil && p.touched != nil {
				// Delta merge failed; fall back to a rebuild.
				logf("%s: delta update of %s failed (%v); rebuilding shard", repo.Name, p.old.File, p.err)
				p.old, p.touched = nil, nil
				p.err = buildShard(repo.Path, dir, name, p, st)
			}
		}()
	}
	wg.Wait()

	m := &Manifest{
		Root:        repo.Path,
		Fingerprint: fp,
		Generation:  gen,
		Updated:     time.Now(),
		Files:       files,
	}
	for _, p := range plans {
		if p.err != nil {
			for _, q := range plans {
				if q.old == nil || q.result.File != q.old.File {
					os.Remove(filepath.Join(dir, q.result.File))
				}
			}
			res.Err = p.err
			return
		}
		m.Shards = append(m.Shards, p.result)
		switch {
		case p.old == nil:
			res.ShardsRebuilt++
		case p.touched == nil:
			res.ShardsKept++
		default:
			res.ShardsDelta++
		}
	}
	res.Shards = len(m.Shards)
	if err := m.Write(mfile); err != nil {
		res.Err = err
		return
	}

	// Remove shard files no longer referenced.
	keep := map[string]bool{"manifest": true}
	for _, s := range m.Shards {
		keep[s.File] = true
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if !keep[e.Name()] {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	if opts.Verbose {
		logf("%s", res)
	}
	return
}

func diffCounts(old, cur []FileInfo) (added, modified, deleted int) {
	i, j := 0, 0
	for i < len(old) || j < len(cur) {
		switch {
		case j >= len(cur) || i < len(old) && comparePaths(old[i].Path, cur[j].Path) < 0:
			deleted++
			i++
		case i >= len(old) || comparePaths(old[i].Path, cur[j].Path) > 0:
			added++
			j++
		default:
			if old[i].Size != cur[j].Size || old[i].MTime != cur[j].MTime {
				modified++
			}
			i++
			j++
		}
	}
	return
}

// splitPlans returns rebuild plans for files (all in one path range
// beginning at start), split into shards of about target files.
func splitPlans(start string, files []FileInfo, target int) []*shardPlan {
	if len(files) == 0 {
		return []*shardPlan{{start: start}}
	}
	n := (len(files) + target - 1) / target
	var plans []*shardPlan
	for i := range n {
		lo, hi := i*len(files)/n, (i+1)*len(files)/n
		s := start
		if i > 0 {
			s = files[lo].Path
		}
		plans = append(plans, &shardPlan{start: s, files: files[lo:hi]})
	}
	return plans
}

// planIncremental compares the old manifest to the current files and
// decides what to do with each shard.
func planIncremental(old *Manifest, files []FileInfo, target int, res *Result) []*shardPlan {
	var plans []*shardPlan
	i, j := 0, 0 // indexes into old.Files and files
	for si := range old.Shards {
		sh := &old.Shards[si]
		var limit string // end of range ("" means unbounded)
		hasLimit := si+1 < len(old.Shards)
		if hasLimit {
			limit = old.Shards[si+1].Start
		}
		before := func(p string) bool { return !hasLimit || comparePaths(p, limit) < 0 }

		var tch []touched
		j0 := j
		for (i < len(old.Files) && before(old.Files[i].Path)) || (j < len(files) && before(files[j].Path)) {
			oi := i < len(old.Files) && before(old.Files[i].Path)
			cj := j < len(files) && before(files[j].Path)
			switch {
			case oi && (!cj || comparePaths(old.Files[i].Path, files[j].Path) < 0):
				tch = append(tch, touched{old.Files[i].Path, true})
				res.Deleted++
				i++
			case cj && (!oi || comparePaths(old.Files[i].Path, files[j].Path) > 0):
				tch = append(tch, touched{files[j].Path, false})
				res.Added++
				j++
			default:
				if old.Files[i].Size != files[j].Size || old.Files[i].MTime != files[j].MTime {
					tch = append(tch, touched{files[j].Path, false})
					res.Modified++
				}
				i++
				j++
			}
		}
		cur := files[j0:j]

		switch {
		case len(tch) == 0:
			plans = append(plans, &shardPlan{start: sh.Start, files: cur, old: sh, result: *sh})
		case len(cur) == 0:
			// Every file in the range is gone: drop the shard.
			// Its range is absorbed by the previous shard.
		case len(cur) > 2*target || len(tch) > max(len(cur)/2, 2000):
			// Too big, or mostly changed: rebuild (and maybe split).
			plans = append(plans, splitPlans(sh.Start, cur, target)...)
		default:
			plans = append(plans, &shardPlan{start: sh.Start, files: cur, old: sh, touched: tch})
		}
	}
	if len(plans) > 0 {
		plans[0].start = "" // first shard always starts at the beginning
	}
	if len(plans) == 0 {
		plans = append(plans, &shardPlan{start: ""})
	}
	return plans
}

// buildShard creates the index file name in dir according to p.
func buildShard(root, dir, name string, p *shardPlan, st *Status) (err error) {
	defer func() {
		if e := recover(); e != nil {
			err = fmt.Errorf("%v", e)
		}
	}()
	p.result = Shard{Start: p.start, File: name, Files: len(p.files)}
	for _, f := range p.files {
		p.result.Bytes += f.Size
	}
	if p.old != nil && p.touched == nil {
		p.result.File = p.old.File
		return nil // unchanged
	}

	abs := func(rel string) string {
		return root + string(filepath.Separator) + filepath.FromSlash(rel)
	}
	out := filepath.Join(dir, name)
	tmp := filepath.Join(index.TempDir, name+".new")
	defer os.Remove(tmp)

	if p.old == nil {
		ix := index.Create(tmp)
		ix.AddRoots([]index.Path{index.MakePath(root)})
		var names []string
		for _, f := range p.files {
			names = append(names, abs(f.Path))
		}
		addFiles(ix, names, st)
		ix.Flush()
	} else {
		// Delta: index the changed files under roots naming every
		// changed or deleted file, then merge over the old shard.
		// The merge drops the old entries for every root and
		// substitutes the delta's (possibly empty) entries.
		delta := filepath.Join(index.TempDir, name+".delta")
		defer os.Remove(delta)
		ix := index.Create(delta)
		var roots []index.Path
		var names []string
		for _, t := range p.touched {
			roots = append(roots, index.MakePath(abs(t.path)))
			if !t.deleted {
				names = append(names, abs(t.path))
			}
		}
		ix.AddRoots(roots)
		addFiles(ix, names, st)
		ix.Flush()
		index.Merge(tmp, filepath.Join(dir, p.old.File), delta)
	}

	// Sanity check the result before installing it.
	ix, err := index.OpenFile(tmp)
	if err != nil {
		return err
	}
	ix.Close()
	return os.Rename(tmp, out)
}

// addFiles adds the named files to ix in order, reading ahead in
// parallel so that indexing is not limited by I/O latency.
func addFiles(ix *index.IndexWriter, names []string, st *Status) {
	type result struct {
		data []byte
		err  error
	}
	const ahead = 16
	ch := make(chan chan result, ahead)
	go func() {
		sem := make(chan struct{}, 8)
		for _, name := range names {
			c := make(chan result, 1)
			ch <- c
			sem <- struct{}{}
			go func() {
				data, err := os.ReadFile(name)
				<-sem
				c <- result{data, err}
			}()
		}
		close(ch)
	}()
	for _, name := range names {
		r := <-<-ch
		if st != nil {
			st.Indexed.Add(1)
		}
		if r.err != nil {
			continue // removed or unreadable since the walk
		}
		if looksBinary(r.data) {
			continue
		}
		if err := ix.Add(name, bytes.NewReader(r.data)); err != nil && !errors.Is(err, io.EOF) {
			if !strings.Contains(err.Error(), "malformed name") {
				log.Printf("%s: %v", name, err)
			}
		}
	}
}

// looksBinary reports whether data is clearly not text,
// avoiding the cost of trigram extraction for it.
func looksBinary(data []byte) bool {
	n := min(len(data), 8000)
	return bytes.IndexByte(data[:n], 0) >= 0
}
