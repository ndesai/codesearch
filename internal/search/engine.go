// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package search implements code search over the indexes maintained by
// package indexer: regexp search with Google Code Search style queries,
// file name search, and search-based cross references.
package search

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ndesai/codesearch/index"
	"github.com/ndesai/codesearch/internal/config"
	"github.com/ndesai/codesearch/internal/indexer"
	"github.com/ndesai/codesearch/internal/symbols"
)

// An Engine searches the indexed repositories of a configuration.
// It is safe for concurrent use. Call Refresh (or run Watch) to pick up
// index changes written by the indexer.
type Engine struct {
	cfg    *config.Config
	repos  []*Repo
	byName map[string]*Repo
}

// A Repo is one configured repository and its current index snapshot.
type Repo struct {
	Name string
	Root string
	cfg  *config.Repo

	mu      sync.Mutex
	cur     *snapshot
	modTime time.Time
	size    int64
	err     error
}

// A snapshot is an immutable set of open shards for one repo.
// Searches hold a reference so shards aren't unmapped under them.
type snapshot struct {
	refs     atomic.Int32
	repo     *Repo
	manifest *indexer.Manifest
	shards   []*shard
}

type shard struct {
	file   string
	ix     *index.Index
	syms   *symbols.Table // nil if the shard has no symbol table
	prefix int            // length of root + separator, stripped from names

	namesOnce sync.Once
	names     *nameTable
}

// New returns an engine for cfg and loads the current indexes.
func New(cfg *config.Config) *Engine {
	e := &Engine{cfg: cfg, byName: map[string]*Repo{}}
	for _, r := range cfg.Repos {
		repo := &Repo{Name: r.Name, Root: r.Path, cfg: r}
		e.repos = append(e.repos, repo)
		e.byName[r.Name] = repo
	}
	e.Refresh()
	return e
}

// Config returns the engine's configuration.
func (e *Engine) Config() *config.Config { return e.cfg }

// Repo returns the named repo, or nil.
func (e *Engine) Repo(name string) *Repo { return e.byName[name] }

// Repos returns the configured repos in configuration order.
func (e *Engine) Repos() []*Repo { return e.repos }

// Refresh reloads any repository whose manifest has changed.
func (e *Engine) Refresh() {
	for _, r := range e.repos {
		r.refresh(e.cfg)
	}
}

// Watch calls Refresh every interval until stop is closed.
func (e *Engine) Watch(interval time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			e.Refresh()
		}
	}
}

func (r *Repo) refresh(cfg *config.Config) {
	mfile := indexer.ManifestFile(cfg, r.Name)
	st, err := os.Stat(mfile)
	r.mu.Lock()
	unchanged := err == nil && st.ModTime().Equal(r.modTime) && st.Size() == r.size && r.cur != nil
	if err != nil && r.cur == nil {
		r.err = nil // not indexed yet
	}
	r.mu.Unlock()
	if err != nil || unchanged {
		return
	}

	m, err := indexer.ReadManifestHeader(mfile)
	if err != nil || m == nil {
		r.setErr(fmt.Errorf("reading manifest: %v", err))
		return
	}
	snap := &snapshot{repo: r, manifest: m}
	snap.refs.Store(1)
	dir := indexer.RepoDir(cfg, r.Name)
	for _, s := range m.Shards {
		ix, err := index.OpenFile(filepath.Join(dir, s.File))
		if err != nil {
			// Probably raced with the indexer replacing the shard;
			// the next refresh will pick up the new manifest.
			snap.close()
			r.setErr(err)
			return
		}
		sh := &shard{file: s.File, ix: ix, prefix: len(m.Root) + 1}
		if t, err := symbols.Open(filepath.Join(dir, indexer.SymbolFile(s.File))); err == nil {
			sh.syms = t
		}
		snap.shards = append(snap.shards, sh)
	}

	r.mu.Lock()
	old := r.cur
	r.cur = snap
	r.modTime = st.ModTime()
	r.size = st.Size()
	r.err = nil
	r.mu.Unlock()
	if old != nil {
		old.release()
	}
}

func (r *Repo) setErr(err error) {
	log.Printf("%s: %v", r.Name, err)
	r.mu.Lock()
	r.err = err
	r.mu.Unlock()
}

// acquire returns the current snapshot with a reference held, or nil.
func (r *Repo) acquire() *snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.cur
	if s != nil {
		s.refs.Add(1)
	}
	return s
}

func (s *snapshot) release() {
	if s.refs.Add(-1) == 0 {
		s.close()
	}
}

func (s *snapshot) close() {
	for _, sh := range s.shards {
		sh.ix.Close()
		sh.syms.Close()
	}
}

// RepoInfo summarizes a repository for display.
type RepoInfo struct {
	Name    string    `json:"name"`
	Root    string    `json:"root"`
	Indexed bool      `json:"indexed"`
	Files   int       `json:"files"`
	Bytes   int64     `json:"bytes"`
	Shards  int       `json:"shards"`
	Updated time.Time `json:"updated,omitempty"`
	Error   string    `json:"error,omitempty"`
}

// Info returns a summary of the repo's index.
func (r *Repo) Info() RepoInfo {
	info := RepoInfo{Name: r.Name, Root: r.Root}
	r.mu.Lock()
	if r.err != nil {
		info.Error = r.err.Error()
	}
	r.mu.Unlock()
	s := r.acquire()
	if s == nil {
		return info
	}
	defer s.release()
	info.Indexed = true
	info.Files = s.manifest.NumFiles()
	info.Bytes = s.manifest.TotalBytes()
	info.Shards = len(s.shards)
	info.Updated = s.manifest.Updated
	return info
}

// Abs returns the absolute file name for a slash-separated path in r,
// or an error if the path escapes the repository root.
func (r *Repo) Abs(rel string) (string, error) {
	rel = strings.TrimPrefix(rel, "/")
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." {
		return r.Root, nil
	}
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid path %q", rel)
	}
	abs := filepath.Join(r.Root, clean)
	// Refuse symlinks that lead outside the repository.
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		root, err := filepath.EvalSymlinks(r.Root)
		if err == nil && real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
			return "", fmt.Errorf("path %q leaves the repository", rel)
		}
	}
	return abs, nil
}

// Excluded reports whether the configuration excludes rel from indexing.
func (r *Repo) Excluded(rel string) bool {
	return r.cfg.Excludes.Excluded(rel)
}

// A nameTable holds a shard's file names, relative to the repository
// root, as one newline-separated blob so that file name regexps can be
// run over all names at once with the line-oriented DFA matcher.
type nameTable struct {
	blob []byte
	offs []uint32 // offs[i] is the start of name i; offs[n] = len(blob)
}

func (sh *shard) nameTable() *nameTable {
	sh.namesOnce.Do(func() {
		n := sh.ix.NumNames()
		t := &nameTable{offs: make([]uint32, 0, n+1)}
		for p := range sh.ix.Names(0, n) {
			s := p.String()
			if len(s) > sh.prefix {
				s = s[sh.prefix:]
			}
			if filepath.Separator != '/' {
				s = strings.ReplaceAll(s, string(filepath.Separator), "/")
			}
			t.offs = append(t.offs, uint32(len(t.blob)))
			t.blob = append(t.blob, s...)
			t.blob = append(t.blob, '\n')
		}
		t.offs = append(t.offs, uint32(len(t.blob)))
		sh.names = t
	})
	return sh.names
}

func (t *nameTable) len() int { return len(t.offs) - 1 }

func (t *nameTable) name(i int) string {
	return string(t.blob[t.offs[i] : t.offs[i+1]-1])
}

// idAt returns the index of the name containing byte offset off.
func (t *nameTable) idAt(off int) int {
	return sort.Search(len(t.offs), func(i int) bool { return int(t.offs[i]) > off }) - 1
}

// A SymbolDef is a definition found in a symbol table.
type SymbolDef struct {
	Repo  string
	Path  string
	Line  int
	Kind  string
	Depth int
}

// symbolDefs returns the definitions of name in the given snapshots.
func symbolDefs(snaps []*snapshot, name string) []SymbolDef {
	var out []SymbolDef
	for _, s := range snaps {
		for _, sh := range s.shards {
			for _, e := range sh.syms.Lookup(name) {
				out = append(out, SymbolDef{Repo: s.repo.Name, Path: e.Path, Line: e.Line, Kind: e.Kind, Depth: e.Depth})
			}
		}
	}
	return out
}
