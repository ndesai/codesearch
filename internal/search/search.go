// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package search

import (
	"bytes"
	"context"
	"io"
	"math"
	"os"
	"path"
	stdregexp "regexp"
	"regexp/syntax"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/ndesai/codesearch/index"
	"github.com/ndesai/codesearch/internal/lang"
	"github.com/ndesai/codesearch/regexp"
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
	// Facets break the matching files down by repository, language
	// and directory, for narrowing the search.
	Facets *Facets `json:"facets,omitempty"`
}

// Facets count matching files by attribute.
type Facets struct {
	Repos []Facet `json:"repos"`
	Langs []Facet `json:"langs"`
	Dirs  []Facet `json:"dirs"`
	// Kinds counts tests, generated and third-party files, with filters
	// that exclude them.
	Kinds []Facet `json:"kinds,omitempty"`
}

// A Facet is one value of an attribute, with the query filter that
// selects it.
type Facet struct {
	Label  string `json:"label"`
	Filter string `json:"filter"`
	Count  int    `json:"count"`
}

// A FileMatch is a file with matching lines.
type FileMatch struct {
	Repo       string      `json:"repo"`
	Path       string      `json:"path"`
	Lang       string      `json:"lang"`
	Lines      []LineMatch `json:"lines"`
	NumMatches int         `json:"numMatches"`
	Score      float64     `json:"-"`

	test, generated, vendored bool  // file categories (see classifyFile)
	mtime                     int64 // modification time (unix ns), 0 if unknown
}

// readHead returns up to n bytes from the start of the named file.
func readHead(name string, n int) []byte {
	f, err := os.Open(name)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, n)
	k, _ := io.ReadFull(f, buf)
	return buf[:k]
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
	Dir  bool   `json:"dir,omitempty"`
}

const maxLineLen = 1000

// Search runs the query over every matching repository.
func (e *Engine) Search(ctx context.Context, q *Query, opt Options) *Result {
	opt.defaults()
	if len(q.SymFilters) > 0 {
		return e.symbolSearch(ctx, q, opt)
	}
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
	recency := e.cfg.RecencyRanking()
	if recency {
		for _, s := range snaps {
			s.loadMtimes() // usually already loaded in the background
		}
	}

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
	// Definitions known to the symbol tables mark matching lines as
	// definitions (for sym: queries and for ranking identifier searches).
	var tableDefs map[string]map[int]bool
	if name := defSymbol(q); name != "" {
		tableDefs = map[string]map[int]bool{}
		for _, d := range symbolDefs(snaps, name) {
			key := d.Repo + "\x00" + d.Path
			if tableDefs[key] == nil {
				tableDefs[key] = map[int]bool{}
			}
			tableDefs[key][d.Line] = true
		}
	}
	for range min(len(jobs), runtime.GOMAXPROCS(0)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := newScanner(q)
			s.tableDefs = tableDefs
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
					fm.mtime = j.sh.mtime(id)
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
	if len(q.Terms) == 1 && q.Symbol == "" && q.Simple {
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
	res.Facets = facets(res.Files)
	res.NumFiles = len(res.Files)
	now := time.Now().UnixNano()
	for _, f := range res.Files {
		res.NumMatches += f.NumMatches
		f.Score = score(f, q)
		if recency {
			f.Score += recencyBoost(f.mtime, now)
		}
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

// A scanner holds per-goroutine state for matching files.
// The DFA regexp matcher is not safe for concurrent use,
// so each goroutine compiles its own copies.
type scanner struct {
	q     *Query
	terms []*regexp.Regexp // compiled q.AllTerms
	iq    *index.Query     // nil if there are no content terms
	defs  *defMatcher
	// tableDefs maps repo + "\x00" + path to the lines that the
	// symbol tables say define the searched identifier.
	tableDefs map[string]map[int]bool
}

// defSymbol returns the identifier whose definitions a query looks
// for: the sym: symbol, or a single identifier search term.
func defSymbol(q *Query) string {
	if q.Symbol != "" {
		return q.Symbol
	}
	if len(q.Terms) == 1 && q.Terms[0].IsIdent() {
		return q.Terms[0].Text
	}
	return ""
}

func newScanner(q *Query) *scanner {
	s := &scanner{q: q}
	var syn []*syntax.Regexp
	for _, t := range q.AllTerms {
		re, err := regexp.Compile("(?m)" + t.Pattern)
		if err != nil {
			panic(err) // validated by ParseQuery
		}
		s.terms = append(s.terms, re)
		syn = append(syn, re.Syntax)
	}
	if len(q.AllTerms) > 0 {
		s.iq = q.IndexQuery(syn)
	}
	if name := defSymbol(q); name != "" {
		s.defs = defsFor(name)
	}
	return s
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
		if !s.q.PathMatch(rel) {
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
		var head []byte
		if s.q.NeedsContent() {
			head = readHead(r.Root+string(os.PathSeparator)+filepathFromSlash(rel), generatedHeadLen)
		}
		if !s.q.Match(rel, nil, head) {
			return nil
		}
		fm := &FileMatch{Repo: r.Name, Path: rel, Lang: lang.Detect(rel).Name}
		classifyFile(fm, head)
		return fm
	}
	data, err := os.ReadFile(r.Root + string(os.PathSeparator) + filepathFromSlash(rel))
	if err != nil {
		return nil
	}

	// Find each term's matching lines on demand: the expression may
	// not need every term (for example, the second operand of a
	// satisfied OR), and terms under NOT only need a yes or no.
	type hit struct{ lo, hi, terms int }
	var hits []hit
	count := 0
	const (
		unknown = iota
		absent
		present   // found, lines not collected
		collected // lines collected (found)
	)
	state := make([]int8, len(s.terms))
	// For usage:, comment: and string: terms, a line counts only if a
	// match falls in the right region of the file.
	var spans []regionSpan
	scanned := false
	inRegion := func(i, lo, hi int) bool {
		t := s.q.AllTerms[i]
		if t.Region == regionAny {
			return true
		}
		if !scanned {
			spans, scanned = scanRegions(lang.Detect(rel).Name, data), true
		}
		for _, m := range t.Std.FindAllStringIndex(string(data[lo:hi]), -1) {
			if regionAt(spans, lo+m[0]) == t.Region {
				return true
			}
		}
		return false
	}
	collect := func(i int) {
		if state[i] == collected || state[i] == absent {
			return
		}
		n := 0
		eachLine(s.terms[i], data, func(lo, hi int) bool {
			if !inRegion(i, lo, hi) {
				return true
			}
			n++
			if len(hits) < 4*maxLines {
				hits = append(hits, hit{lo, hi, 1})
			}
			return true
		})
		count += n
		state[i] = collected
		if n == 0 {
			state[i] = absent
		}
	}
	has := func(i int) bool {
		if state[i] == unknown {
			if s.q.IsPositive(i) {
				collect(i)
			} else if s.q.AllTerms[i].Region != regionAny {
				state[i] = absent
				eachLine(s.terms[i], data, func(lo, hi int) bool {
					if inRegion(i, lo, hi) {
						state[i] = present
						return false
					}
					return true
				})
			} else if s.terms[i].Match(data, true, true) >= 0 {
				state[i] = present
			} else {
				state[i] = absent
			}
		}
		return state[i] >= present
	}
	if !s.q.Match(rel, has, data) {
		return nil
	}
	// Show lines for every positive term present, including any the
	// expression didn't need to look at.
	for i := range s.terms {
		if s.q.IsPositive(i) {
			collect(i)
		}
	}
	slices.SortFunc(hits, func(a, b hit) int { return a.lo - b.lo })
	if len(s.terms) > 1 {
		// Merge lines matched by several terms, counting the terms.
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
	classifyFile(fm, data)
	lineno, pos := 1, 0
	for _, h := range hits {
		lineno += bytes.Count(data[pos:h.lo], nl)
		pos = h.lo
		line := data[h.lo:h.hi]
		lm := LineMatch{Line: lineno}
		if s.defs != nil {
			lm.Def = s.tableDefs[r.Name+"\x00"+rel][lineno] || s.defs.isDef(l.Name, string(line))
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
	for k < len(fm.Lines) && (fm.Lines[k].Def || len(s.q.Terms) > 1 && fm.Lines[k].terms == len(s.q.Terms)) {
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

	// Demote tests, generated and third-party code (the is: categories),
	// and a few other usually less interesting places.
	p := "/" + strings.ToLower(f.Path)
	switch {
	case f.test:
		sc -= 6
	case f.generated || f.vendored:
		sc -= 4
	default:
		for _, w := range []string{"/mock", "fake", "/benchmark", "/out/", "/build/", "/dist/"} {
			if strings.Contains(p, w) {
				sc -= 3
				break
			}
		}
	}
	sc -= 0.25 * float64(strings.Count(f.Path, "/"))
	return sc
}

// nameMatches finds files and directories whose base name matches t.
func (e *Engine) nameMatches(ctx context.Context, snaps []*snapshot, q *Query, t *Term, limit int) ([]*NameMatch, int) {
	all := collectNames(ctx, snaps, q, t, true)
	return topNames(all, limit), len(all)
}

// Suggest returns up to limit files and directories whose names match
// the text typed so far, for search-as-you-type.
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
	// Suggestions also accept matches anywhere in the path,
	// so "core/Activity" works.
	return topNames(collectNames(ctx, snaps, q, t, false), limit)
}

type nameCand struct {
	repo, path string
	dir        bool
	rank       int
}

// collectNames finds the files and directories whose names match t.
// Lower ranks are better: an exact name match, then a prefix match,
// then a substring match, then a regexp match. If baseOnly is false,
// files whose full path (but not base name) matches are included last.
func collectNames(ctx context.Context, snaps []*snapshot, q *Query, t *Term, baseOnly bool) []nameCand {
	re, err := regexp.Compile("(?m)" + t.Pattern)
	if err != nil {
		return nil
	}
	text := strings.ToLower(t.Text)
	rank := func(name string) int {
		lb := strings.ToLower(name)
		switch {
		case lb == text || strings.TrimSuffix(lb, path.Ext(lb)) == text:
			return 0
		case strings.HasPrefix(lb, text):
			return 1
		case strings.Contains(lb, text):
			return 2
		}
		return 3
	}
	// Directories are filtered like files (a lang: filter rules them out).
	dirOK := q.PathMatch
	var all []nameCand
	for _, s := range snaps {
		seen := map[string]bool{} // directories already considered
		for _, sh := range s.shards {
			if ctx.Err() != nil {
				break
			}
			names := sh.nameTable()
			eachLine(re, names.blob, func(lo, hi int) bool {
				p := string(names.blob[lo:hi])
				if base := path.Base(p); t.Std.MatchString(base) {
					if q.PathMatch(p) {
						all = append(all, nameCand{s.repo.Name, p, false, rank(base)})
					}
				} else if !baseOnly && q.PathMatch(p) {
					all = append(all, nameCand{s.repo.Name, p, false, 4})
				}
				// Directories containing this file whose names match.
				for i := 0; i < len(p); i++ {
					if p[i] != '/' {
						continue
					}
					d := p[:i]
					if seen[d] {
						continue
					}
					seen[d] = true
					if base := path.Base(d); t.Std.MatchString(base) && dirOK(d) {
						all = append(all, nameCand{s.repo.Name, d, true, rank(base)})
					}
				}
				return len(all) < 100000
			})
		}
	}
	return all
}

// topNames returns the best limit candidates: by rank, then shorter
// paths, with directories before files at equal rank and length.
func topNames(all []nameCand, limit int) []*NameMatch {
	slices.SortFunc(all, func(a, b nameCand) int {
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		if da, db := strings.Count(a.path, "/"), strings.Count(b.path, "/"); da != db {
			return da - db
		}
		if a.dir != b.dir {
			if a.dir {
				return -1
			}
			return 1
		}
		if len(a.path) != len(b.path) {
			return len(a.path) - len(b.path)
		}
		return strings.Compare(a.path, b.path)
	})
	var out []*NameMatch
	for _, c := range all[:min(limit, len(all))] {
		out = append(out, &NameMatch{Repo: c.repo, Path: c.path, Dir: c.dir})
	}
	return out
}

const maxFacets = 12

// facets counts files by repository, language and directory.
func facets(files []*FileMatch) *Facets {
	if len(files) == 0 {
		return nil
	}
	top := func(counts map[string]int, filter func(string) string) []Facet {
		var out []Facet
		for k, n := range counts {
			out = append(out, Facet{Label: k, Filter: filter(k), Count: n})
		}
		slices.SortFunc(out, func(a, b Facet) int {
			if a.Count != b.Count {
				return b.Count - a.Count
			}
			return strings.Compare(a.Label, b.Label)
		})
		return out[:min(len(out), maxFacets)]
	}
	repos := map[string]int{}
	langs := map[string]int{}
	for _, f := range files {
		repos[f.Repo]++
		langs[f.Lang]++
	}
	f := &Facets{
		Repos: top(repos, func(r string) string { return "repo:^" + stdregexp.QuoteMeta(r) + "$" }),
		Langs: top(langs, langFilter),
	}
	var tests, gen, vend int
	for _, fm := range files {
		if fm.test {
			tests++
		}
		if fm.generated {
			gen++
		}
		if fm.vendored {
			vend++
		}
	}
	for _, k := range []struct {
		label, filter string
		n             int
	}{{"Tests", "-is:test", tests}, {"Generated", "-is:generated", gen}, {"Third-party", "-is:vendored", vend}} {
		if k.n > 0 {
			f.Kinds = append(f.Kinds, Facet{Label: k.label, Filter: k.filter, Count: k.n})
		}
	}

	// Directories: group by leading path elements, descending while a
	// single directory holds every match (so results all under
	// frameworks/base/core show that directory's children).
	prefix := ""
	var dirs map[string]int
	for {
		dirs = map[string]int{}
		for _, fm := range files {
			if p, ok := strings.CutPrefix(fm.Path, prefix); ok {
				if i := strings.IndexByte(p, '/'); i >= 0 {
					dirs[prefix+p[:i+1]]++
				}
			}
		}
		if len(dirs) != 1 || len(repos) > 1 {
			break
		}
		next := ""
		for d, n := range dirs {
			if n == len(files) {
				next = d
			}
		}
		if next == "" {
			break
		}
		prefix = next
	}
	f.Dirs = top(dirs, func(d string) string { return "file:^" + stdregexp.QuoteMeta(d) })
	return f
}

// langFilter returns the lang: filter for a detected language name.
func langFilter(name string) string {
	v := strings.ToLower(name)
	if strings.ContainsAny(v, ` "`) {
		return `lang:"` + strings.ReplaceAll(v, `"`, `\"`) + `"`
	}
	return "lang:" + v
}

// Recently modified files rank slightly higher: the boost starts at
// recencyMax and halves every recencyHalfLife, small next to the boosts
// for definitions and file name matches.
const (
	recencyMax      = 2.5
	recencyHalfLife = 30 * 24 * time.Hour
)

func recencyBoost(mtime, now int64) float64 {
	if mtime <= 0 {
		return 0
	}
	age := max(time.Duration(now-mtime), 0)
	return recencyMax * math.Pow(0.5, float64(age)/float64(recencyHalfLife))
}
