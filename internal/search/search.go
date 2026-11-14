// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package search

import (
	"bytes"
	"context"
	"math"
	"os"
	"path"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/codesearch/index"
	"github.com/google/codesearch/internal/lang"
	"github.com/google/codesearch/regexp"
)

// Options limit the work done by a search.
type Options struct {
	MaxFiles        int           // stop after this many matching files (default 2000)
	MaxLinesPerFile int           // matching lines kept per file (default 100)
	Timeout         time.Duration // default 15s
}

func (o *Options) defaults() {
	if o.MaxFiles <= 0 {
		o.MaxFiles = 2000
	}
	if o.MaxLinesPerFile <= 0 {
		o.MaxLinesPerFile = 100
	}
	if o.Timeout <= 0 {
		o.Timeout = 15 * time.Second
	}
}

// Result is the outcome of a search.
type Result struct {
	Query       string       `json:"query"`
	Files       []*FileMatch `json:"files"`
	NameMatches []*NameMatch `json:"nameMatches,omitempty"`
	NumNames    int          `json:"numNameMatches,omitempty"`
	NumFiles    int          `json:"numFiles"`
	NumMatches  int          `json:"numMatches"`
	Candidates  int          `json:"candidates"`
	Truncated   bool         `json:"truncated,omitempty"`
	TimedOut    bool         `json:"timedOut,omitempty"`
	Elapsed     float64      `json:"elapsed"`
	// Terms are the content patterns, for highlighting in file views.
	Terms []string `json:"terms,omitempty"`
}

// A FileMatch is a file with matching lines.
type FileMatch struct {
	Repo       string      `json:"repo"`
	Path       string      `json:"path"`
	Lang       string      `json:"lang"`
	Lines      []LineMatch `json:"lines"`
	NumMatches int         `json:"numMatches"`
	Score      float64     `json:"-"`
}

// A LineMatch is one matching line.
type LineMatch struct {
	Line   int      `json:"line"`
	Text   string   `json:"text"`
	Ranges [][2]int `json:"ranges,omitempty"` // byte ranges of matches within Text
	Def    bool     `json:"def,omitempty"`    // looks like a definition
	terms  int      // number of query terms matching the line
}

// A NameMatch is a file whose name matches the query.
type NameMatch struct {
	Repo string `json:"repo"`
	Path string `json:"path"`
}

const maxLineLen = 1000

// Search runs the query over every matching repository.
func (e *Engine) Search(ctx context.Context, q *Query, opt Options) *Result {
	opt.defaults()
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, opt.Timeout)
	defer cancel()

	res := &Result{Query: q.Raw}
	for _, t := range q.Terms {
		res.Terms = append(res.Terms, t.Pattern)
	}

	snaps := e.acquire(q)
	defer func() {
		for _, s := range snaps {
			s.release()
		}
	}()

	type job struct {
		snap *snapshot
		sh   *shard
	}
	var jobs []job
	for _, s := range snaps {
		for _, sh := range s.shards {
			jobs = append(jobs, job{s, sh})
		}
	}

	var (
		mu        sync.Mutex
		found     atomic.Int64
		cands     atomic.Int64
		truncated atomic.Bool
		next      atomic.Int64
		wg        sync.WaitGroup
	)
	sym := q.Symbol
	for range min(len(jobs), runtime.GOMAXPROCS(0)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := newScanner(q)
			for {
				i := int(next.Add(1) - 1)
				if i >= len(jobs) || ctx.Err() != nil {
					return
				}
				j := jobs[i]
				s.shard(ctx, j.snap.repo, j.sh, func(id int, rel string) bool {
					if found.Load() >= int64(opt.MaxFiles) {
						truncated.Store(true)
						return false
					}
					cands.Add(1)
					fm := s.file(j.snap.repo, rel, opt.MaxLinesPerFile, sym)
					if fm == nil {
						return true
					}
					found.Add(1)
					mu.Lock()
					res.Files = append(res.Files, fm)
					mu.Unlock()
					return true
				})
			}
		}()
	}

	// File name matches run concurrently with content search.
	if len(q.Terms) == 1 && q.Symbol == "" && len(q.NotTerms) == 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res.NameMatches, res.NumNames = e.nameMatches(ctx, snaps, q, q.Terms[0], 10)
		}()
	}
	wg.Wait()

	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.Truncated = true
	}
	if truncated.Load() {
		res.Truncated = true
	}
	res.Candidates = int(cands.Load())
	res.NumFiles = len(res.Files)
	for _, f := range res.Files {
		res.NumMatches += f.NumMatches
		f.Score = score(f, q)
	}
	sort.Slice(res.Files, func(i, j int) bool {
		a, b := res.Files[i], res.Files[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Repo != b.Repo {
			return a.Repo < b.Repo
		}
		return a.Path < b.Path
	})
	res.Elapsed = time.Since(start).Seconds()
	return res
}

// acquire returns snapshots for the repos selected by q.
func (e *Engine) acquire(q *Query) []*snapshot {
	var snaps []*snapshot
	for _, r := range e.repos {
		if !repoMatch(q, r.Name) {
			continue
		}
		if s := r.acquire(); s != nil {
			snaps = append(snaps, s)
		}
	}
	return snaps
}

func repoMatch(q *Query, name string) bool {
	for _, re := range q.Repos {
		if !re.MatchString(name) {
			return false
		}
	}
	for _, re := range q.NotRepos {
		if re.MatchString(name) {
			return false
		}
	}
	return true
}

// pathMatch reports whether rel passes the query's path and language filters.
func pathMatch(q *Query, rel string) bool {
	for _, re := range q.Files {
		if !re.MatchString(rel) {
			return false
		}
	}
	for _, re := range q.NotFiles {
		if re.MatchString(rel) {
			return false
		}
	}
	for _, f := range q.Langs {
		if !f.Match(rel) {
			return false
		}
	}
	for _, f := range q.NotLangs {
		if f.Match(rel) {
			return false
		}
	}
	return true
}

// A scanner holds per-goroutine state for matching files.
// The DFA regexp matcher is not safe for concurrent use,
// so each goroutine compiles its own copies.
type scanner struct {
	q     *Query
	terms []*regexp.Regexp
	nots  []*regexp.Regexp
	iq    *index.Query
	defs  *defMatcher
}

func newScanner(q *Query) *scanner {
	s := &scanner{q: q}
	var iq *index.Query
	for _, t := range q.Terms {
		re, err := regexp.Compile("(?m)" + t.Pattern)
		if err != nil {
			panic(err) // validated by ParseQuery
		}
		s.terms = append(s.terms, re)
		tq := index.RegexpQuery(re.Syntax)
		if iq == nil {
			iq = tq
		} else {
			iq = andQuery(iq, tq)
		}
	}
	for _, t := range q.NotTerms {
		re, err := regexp.Compile("(?m)" + t.Pattern)
		if err != nil {
			panic(err)
		}
		s.nots = append(s.nots, re)
	}
	s.iq = iq
	if q.Symbol != "" {
		s.defs = defsFor(q.Symbol)
	} else if len(q.Terms) == 1 && q.Terms[0].IsIdent() {
		s.defs = defsFor(q.Terms[0].Text)
	}
	return s
}

func andQuery(a, b *index.Query) *index.Query {
	return &index.Query{Op: index.QAnd, Sub: []*index.Query{a, b}}
}

// shard calls fn for each file in sh that may match the query,
// stopping early if fn returns false.
func (s *scanner) shard(ctx context.Context, r *Repo, sh *shard, fn func(id int, rel string) bool) {
	defer func() {
		if e := recover(); e != nil {
			r.setErr(errorf("searching %s: %v", sh.file, e))
		}
	}()
	names := sh.nameTable()
	var ids []int
	if s.iq != nil {
		ids = sh.ix.PostingQuery(s.iq)
	} else {
		// No content terms: list files by name.
		ids = s.nameCandidates(names)
	}
	for k, id := range ids {
		if k%64 == 0 && ctx.Err() != nil {
			return
		}
		if id >= names.len() {
			continue
		}
		rel := names.name(id)
		if !pathMatch(s.q, rel) {
			continue
		}
		if !fn(id, rel) {
			return
		}
	}
}

// nameCandidates returns the ids of names matching the first file:
// pattern, using the DFA over the whole name table; or all names.
func (s *scanner) nameCandidates(names *nameTable) []int {
	if len(s.q.Files) == 0 {
		ids := make([]int, names.len())
		for i := range ids {
			ids[i] = i
		}
		return ids
	}
	re, err := regexp.Compile("(?m)" + s.q.Files[0].String())
	if err != nil {
		return nil
	}
	var ids []int
	eachLine(re, names.blob, func(lo, hi int) bool {
		ids = append(ids, names.idAt(lo))
		return true
	})
	return ids
}

// eachLine calls fn with the bounds of each line of data matching re
// (excluding the newline), stopping if fn returns false.
func eachLine(re *regexp.Regexp, data []byte, fn func(lo, hi int) bool) {
	pos := 0
	begin := true
	for pos < len(data) {
		m := re.Match(data[pos:], begin, true)
		begin = false
		if m < 0 {
			return
		}
		m += pos
		lo := bytes.LastIndexByte(data[pos:m], '\n') + 1 + pos
		hi := m
		if hi > len(data) {
			hi = len(data)
		}
		if !fn(lo, hi) {
			return
		}
		pos = hi + 1
	}
}

// file searches one file, returning nil if it does not match.
func (s *scanner) file(r *Repo, rel string, maxLines int, sym string) *FileMatch {
	if len(s.terms) == 0 {
		return &FileMatch{Repo: r.Name, Path: rel, Lang: lang.Detect(rel).Name}
	}
	abs := r.Root + string(os.PathSeparator) + rel
	if os.PathSeparator != '/' {
		abs = r.Root + string(os.PathSeparator) + strings.ReplaceAll(rel, "/", string(os.PathSeparator))
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil
	}
	for _, re := range s.nots {
		if re.Match(data, true, true) >= 0 {
			return nil
		}
	}

	type hit struct{ lo, hi, terms int }
	var hits []hit
	count := 0
	for _, re := range s.terms {
		n := 0
		eachLine(re, data, func(lo, hi int) bool {
			n++
			if len(hits) < 4*maxLines {
				hits = append(hits, hit{lo, hi, 1})
			}
			return true
		})
		if n == 0 {
			return nil // every term must match
		}
		count += n
	}
	if len(s.terms) > 1 {
		// Merge lines matched by several terms, counting the terms.
		slices.SortFunc(hits, func(a, b hit) int { return a.lo - b.lo })
		out := hits[:0]
		for _, h := range hits {
			if n := len(out); n > 0 && out[n-1].lo == h.lo {
				out[n-1].terms++
				continue
			}
			out = append(out, h)
		}
		hits = out
		count = len(hits)
	}

	l := lang.Detect(rel)
	fm := &FileMatch{Repo: r.Name, Path: rel, Lang: l.Name, NumMatches: count}
	lineno, pos := 1, 0
	for _, h := range hits {
		lineno += bytes.Count(data[pos:h.lo], nl)
		pos = h.lo
		line := data[h.lo:h.hi]
		lm := LineMatch{Line: lineno}
		if s.defs != nil {
			lm.Def = s.defs.isDef(l.Name, string(line))
		}
		if sym != "" && !lm.Def {
			continue
		}
		lm.Text, lm.Ranges = snippet(line, s.q.Terms)
		lm.terms = h.terms
		fm.Lines = append(fm.Lines, lm)
	}
	if sym != "" {
		if len(fm.Lines) == 0 {
			return nil
		}
		fm.NumMatches = len(fm.Lines)
	}
	// Show the best lines first: definitions, then lines matching the
	// most terms, then the rest in line order.
	best := func(l LineMatch) int {
		if l.Def {
			return 1 << 20
		}
		return l.terms
	}
	slices.SortStableFunc(fm.Lines, func(a, b LineMatch) int { return best(b) - best(a) })
	if len(fm.Lines) > maxLines {
		fm.Lines = fm.Lines[:maxLines]
	}
	k := 0
	for k < len(fm.Lines) && (fm.Lines[k].Def || len(s.terms) > 1 && fm.Lines[k].terms == len(s.terms)) {
		k++
	}
	slices.SortFunc(fm.Lines[k:], func(a, b LineMatch) int { return a.Line - b.Line })
	return fm
}

var nl = []byte("\n")

// snippet returns the text of line (valid UTF-8, length-limited)
// and the byte ranges where the terms match.
func snippet(line []byte, terms []*Term) (string, [][2]int) {
	line = bytes.TrimRight(line, "\r")
	text := string(line)
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "�")
	}
	var ranges [][2]int
	for _, t := range terms {
		for _, m := range t.Std.FindAllStringIndex(text, 50) {
			if m[1] > m[0] {
				ranges = append(ranges, [2]int{m[0], m[1]})
			}
		}
	}
	slices.SortFunc(ranges, func(a, b [2]int) int { return a[0] - b[0] })
	// Merge overlapping ranges.
	var merged [][2]int
	for _, r := range ranges {
		if n := len(merged); n > 0 && r[0] <= merged[n-1][1] {
			merged[n-1][1] = max(merged[n-1][1], r[1])
			continue
		}
		merged = append(merged, r)
	}
	if len(text) > maxLineLen {
		// Keep a window around the first match.
		start := 0
		if len(merged) > 0 && merged[0][0] > maxLineLen/4 {
			start = merged[0][0] - maxLineLen/4
		}
		end := min(len(text), start+maxLineLen)
		for start > 0 && !utf8.RuneStart(text[start]) {
			start++
		}
		for end < len(text) && !utf8.RuneStart(text[end]) {
			end--
		}
		text = text[start:end]
		var clipped [][2]int
		for _, r := range merged {
			a, b := r[0]-start, r[1]-start
			if b <= 0 || a >= len(text) {
				continue
			}
			clipped = append(clipped, [2]int{max(a, 0), min(b, len(text))})
		}
		merged = clipped
	}
	return text, merged
}

// score ranks a file match; higher is better.
func score(f *FileMatch, q *Query) float64 {
	sc := 0.0
	base := path.Base(f.Path)
	stem := strings.TrimSuffix(base, path.Ext(base))
	for _, t := range q.Terms {
		switch {
		case strings.EqualFold(stem, t.Text):
			sc += 30
		case t.Std.MatchString(base):
			sc += 12
		case t.Std.MatchString(f.Path):
			sc += 4
		}
	}
	for _, l := range f.Lines {
		if l.Def {
			sc += 20
			break
		}
	}
	sc += math.Min(math.Log2(1+float64(f.NumMatches)), 5)

	p := "/" + strings.ToLower(f.Path)
	for _, w := range []string{"/test/", "/tests/", "/testing/", "/testdata/", "_test.", "test.java", "tests.java", "test.kt", ".test.", ".spec.", "/mock", "fake", "/benchmark"} {
		if strings.Contains(p, w) {
			sc -= 6
			break
		}
	}
	for _, w := range []string{"/third_party/", "/external/", "/vendor/", "/prebuilts/", "/node_modules/", "/generated/", "/gen/", "/out/", "/build/", "/dist/", ".pb.", "_pb2.", ".generated."} {
		if strings.Contains(p, w) {
			sc -= 4
			break
		}
	}
	sc -= 0.25 * float64(strings.Count(f.Path, "/"))
	return sc
}

// nameMatches finds files whose base name matches term.
func (e *Engine) nameMatches(ctx context.Context, snaps []*snapshot, q *Query, t *Term, limit int) ([]*NameMatch, int) {
	re, err := regexp.Compile("(?m)" + t.Pattern)
	if err != nil {
		return nil, 0
	}
	type cand struct {
		repo, path string
		rank       int
	}
	var all []cand
	text := strings.ToLower(t.Text)
	for _, s := range snaps {
		for _, sh := range s.shards {
			if ctx.Err() != nil {
				break
			}
			names := sh.nameTable()
			eachLine(re, names.blob, func(lo, hi int) bool {
				p := string(names.blob[lo:hi])
				base := path.Base(p)
				if !t.Std.MatchString(base) || !pathMatch(q, p) {
					return true
				}
				lb := strings.ToLower(base)
				rank := 3
				switch {
				case strings.TrimSuffix(lb, path.Ext(lb)) == text || lb == text:
					rank = 0
				case strings.HasPrefix(lb, text):
					rank = 1
				case t.Literal || t.IsIdent():
					rank = 2
				}
				all = append(all, cand{s.repo.Name, p, rank})
				return len(all) < 100000
			})
		}
	}
	slices.SortFunc(all, func(a, b cand) int {
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		if len(a.path) != len(b.path) {
			return len(a.path) - len(b.path)
		}
		return strings.Compare(a.path, b.path)
	})
	var out []*NameMatch
	for _, c := range all[:min(limit, len(all))] {
		out = append(out, &NameMatch{Repo: c.repo, Path: c.path})
	}
	return out, len(all)
}

// Suggest returns up to limit files whose names match the text typed so
// far, for search-as-you-type.
func (e *Engine) Suggest(ctx context.Context, text string, limit int) []*NameMatch {
	q, err := ParseQuery(text)
	if err != nil || len(q.Terms) == 0 {
		return nil
	}
	t := q.Terms[len(q.Terms)-1]
	if len(t.Text) < 2 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	snaps := e.acquire(q)
	defer func() {
		for _, s := range snaps {
			s.release()
		}
	}()
	// For suggestions match the full path, so "core/Activity" works.
	re, err := regexp.Compile("(?m)" + t.Pattern)
	if err != nil {
		return nil
	}
	type cand struct {
		repo, path string
		rank       int
	}
	var all []cand
	text = strings.ToLower(t.Text)
	for _, s := range snaps {
		for _, sh := range s.shards {
			if ctx.Err() != nil {
				break
			}
			names := sh.nameTable()
			eachLine(re, names.blob, func(lo, hi int) bool {
				p := string(names.blob[lo:hi])
				if !pathMatch(q, p) {
					return true
				}
				lb := strings.ToLower(path.Base(p))
				rank := 4
				switch {
				case strings.TrimSuffix(lb, path.Ext(lb)) == text || lb == text:
					rank = 0
				case strings.HasPrefix(lb, text):
					rank = 1
				case strings.Contains(lb, text):
					rank = 2
				case t.Std.MatchString(path.Base(p)):
					rank = 3
				}
				all = append(all, cand{s.repo.Name, p, rank})
				return len(all) < 50000
			})
		}
	}
	slices.SortFunc(all, func(a, b cand) int {
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		if len(a.path) != len(b.path) {
			return len(a.path) - len(b.path)
		}
		return strings.Compare(a.path, b.path)
	})
	var out []*NameMatch
	for _, c := range all[:min(limit, len(all))] {
		out = append(out, &NameMatch{Repo: c.repo, Path: c.path})
	}
	return out
}
