// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package search

import (
	"context"
	"fmt"
	"os"
	stdregexp "regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ndesai/codesearch/index"
	"github.com/ndesai/codesearch/internal/lang"
	"github.com/ndesai/codesearch/internal/outline"
	"github.com/ndesai/codesearch/regexp"
)

// The call hierarchy is search based, like cross references: a call is
// an identifier followed by "(" in code (not in a comment or string),
// and the caller is the function whose definition most closely precedes
// the call in the file's outline.

// A CallSite is one call of a function.
type CallSite struct {
	Line   int      `json:"line"`
	Text   string   `json:"text"`
	Ranges [][2]int `json:"ranges,omitempty"`
}

// A Caller is a function that calls the symbol, with its call sites.
type Caller struct {
	Repo  string     `json:"repo"`
	Path  string     `json:"path"`
	Name  string     `json:"name"` // "" for calls outside any function
	Kind  string     `json:"kind,omitempty"`
	Line  int        `json:"line"` // line of the caller's definition, 0 if none
	Calls []CallSite `json:"calls"`
	dist  int
}

// CallersResult lists the callers of a symbol.
type CallersResult struct {
	Symbol    string    `json:"symbol"`
	Callers   []*Caller `json:"callers"`
	NumCalls  int       `json:"numCalls"`
	Truncated bool      `json:"truncated,omitempty"`
	Elapsed   float64   `json:"elapsed"`
}

const (
	maxCallerFiles  = 2000
	maxCallers      = 200
	maxCallsPerFile = 50
)

// Callers finds the functions that call sym, nearest to the viewed file
// (repo, from) first.
func (e *Engine) Callers(ctx context.Context, sym, repo, from string) (*CallersResult, error) {
	if !identRE.MatchString(sym) {
		return nil, fmt.Errorf("not an identifier: %q", sym)
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	callPat := `\b` + stdregexp.QuoteMeta(sym) + `\s*\(`
	re, err := regexp.Compile("(?m)" + callPat)
	if err != nil {
		return nil, err
	}
	std := stdregexp.MustCompile(callPat)
	nameRe := stdregexp.MustCompile(`\b` + stdregexp.QuoteMeta(sym) + `\b`)
	iq := index.RegexpQuery(re.Syntax)
	fromLang := lang.Detect(from).Name

	snaps := e.acquire(&Query{})
	defer func() {
		for _, s := range snaps {
			s.release()
		}
	}()
	type cand struct {
		repo *Repo
		rel  string
		dist int
	}
	var cands []cand
	for _, s := range snaps {
		for _, sh := range s.shards {
			names := sh.nameTable()
			func() {
				defer func() { recover() }()
				for _, id := range sh.ix.PostingQuery(iq) {
					if id >= names.len() {
						continue
					}
					rel := names.name(id)
					// Calls are matched by name, so a call in another
					// language is to a different function.
					if fromLang != "Text" && !sameFamily(lang.Detect(rel).Name, fromLang) {
						continue
					}
					cands = append(cands, cand{s.repo, rel, distance(s.repo.Name, rel, repo, from, fromLang)})
				}
			}()
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].dist < cands[j].dist })
	res := &CallersResult{Symbol: sym, Callers: []*Caller{}}
	if len(cands) > maxCallerFiles {
		cands, res.Truncated = cands[:maxCallerFiles], true
	}

	defs := defsFor(sym)
	results := make([][]*Caller, len(cands))
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(len(cands), runtime.GOMAXPROCS(0)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			re, _ := regexp.Compile("(?m)" + callPat)
			for {
				i := int(next.Add(1) - 1)
				if i >= len(cands) || ctx.Err() != nil {
					return
				}
				c := cands[i]
				data, err := os.ReadFile(c.repo.Root + string(os.PathSeparator) + filepathFromSlash(c.rel))
				if err != nil {
					continue
				}
				results[i] = callersInFile(c.repo.Name, c.rel, data, re, std, nameRe, defs, c.dist)
			}
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		res.Truncated = true
	}
	for _, cs := range results {
		for _, c := range cs {
			res.NumCalls += len(c.Calls)
			if len(res.Callers) < maxCallers {
				res.Callers = append(res.Callers, c)
			} else {
				res.Truncated = true
			}
		}
	}
	slices.SortStableFunc(res.Callers, func(a, b *Caller) int { return a.dist - b.dist })
	res.Elapsed = time.Since(start).Seconds()
	return res, nil
}

// callersInFile groups the calls in one file by enclosing function.
func callersInFile(repo, rel string, data []byte, re *regexp.Regexp, std, nameRe *stdregexp.Regexp, defs *defMatcher, dist int) []*Caller {
	langName := lang.Detect(rel).Name
	var regions []regionSpan
	var syms []outline.Symbol
	scanned := false
	byCaller := map[int]*Caller{} // keyed by caller definition line
	var order []int
	lineno, pos, n := 1, 0, 0
	eachLine(re, data, func(lo, hi int) bool {
		lineno += strings.Count(string(data[pos:lo]), "\n")
		pos = lo
		line := string(data[lo:hi])
		if defs.isDef(langName, line) {
			return true // the definition itself (or a declaration)
		}
		if !scanned {
			regions = scanRegions(langName, data)
			syms = outline.Extract(langName, string(data))
			scanned = true
		}
		inCode := false
		for _, m := range std.FindAllStringIndex(line, -1) {
			if regionAt(regions, lo+m[0]) == regionCode {
				inCode = true
				break
			}
		}
		if !inCode {
			return true
		}
		// Enclosing function: the last function or method defined
		// before this line.
		var enc *outline.Symbol
		for i := range syms {
			if syms[i].Line > lineno {
				break
			}
			if syms[i].Kind == "func" || syms[i].Kind == "method" || syms[i].Kind == "macro" {
				enc = &syms[i]
			}
		}
		key := 0
		if enc != nil {
			key = enc.Line
		}
		c := byCaller[key]
		if c == nil {
			c = &Caller{Repo: repo, Path: rel, dist: dist}
			if enc != nil {
				c.Name, c.Kind, c.Line = enc.Name, enc.Kind, enc.Line
			}
			byCaller[key] = c
			order = append(order, key)
		}
		text, ranges := clipLine(strings.TrimRight(line, "\r"), matchRanges(line, nameRe))
		c.Calls = append(c.Calls, CallSite{Line: lineno, Text: text, Ranges: ranges})
		n++
		return n < maxCallsPerFile
	})
	var out []*Caller
	for _, k := range order {
		out = append(out, byCaller[k])
	}
	return out
}

func matchRanges(line string, re *stdregexp.Regexp) [][2]int {
	var r [][2]int
	for _, m := range re.FindAllStringIndex(line, 20) {
		r = append(r, [2]int{m[0], m[1]})
	}
	return r
}

// A Callee is a function called by the symbol.
type Callee struct {
	Name  string `json:"name"`
	Line  int    `json:"line"`  // first call, in the caller's file
	Count int    `json:"count"` // number of calls
	// Definition, if one was found.
	Repo    string `json:"repo,omitempty"`
	Path    string `json:"path,omitempty"`
	DefLine int    `json:"defLine,omitempty"`
	Kind    string `json:"kind,omitempty"`
}

// CalleesResult lists the functions a function calls.
type CalleesResult struct {
	Symbol  string    `json:"symbol"`
	Repo    string    `json:"repo"`
	Path    string    `json:"path"`
	Line    int       `json:"line"` // the function's definition
	Callees []*Callee `json:"callees"`
	Elapsed float64   `json:"elapsed"`
}

var (
	callRE = stdregexp.MustCompile(`([A-Za-z_$][\w$]*)\s*\(`)
	// words followed by "(" that are not calls
	notCalls = set("if", "for", "while", "switch", "return", "catch", "sizeof", "alignof", "typeof", "decltype",
		"static_cast", "dynamic_cast", "const_cast", "reinterpret_cast", "func", "function", "def", "fn", "fun",
		"elif", "and", "or", "not", "in", "assert", "defined", "with", "except", "foreach", "using", "lock",
		"synchronized", "new", "delete", "throw", "await", "yield", "case", "do", "else", "go", "defer", "select", "range")
)

const (
	maxBodyLines = 500
	maxCallees   = 100
)

// Callees finds the calls made by the function sym defined in (repo,
// path) at line (or, if line is 0, the first definition of sym in the
// file), resolving each callee to its nearest definition.
func (e *Engine) Callees(ctx context.Context, sym, repoName, path string, line int) (*CalleesResult, error) {
	if !identRE.MatchString(sym) {
		return nil, fmt.Errorf("not an identifier: %q", sym)
	}
	start := time.Now()
	repo := e.byName[repoName]
	if repo == nil {
		return nil, fmt.Errorf("unknown repository %q", repoName)
	}
	abs, err := repo.Abs(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	langName := lang.Detect(path).Name
	syms := outline.Extract(langName, string(data))
	lines := strings.Split(string(data), "\n")

	// Find the definition and where its body ends: the next symbol at
	// the same or a shallower depth.
	di := -1
	for i, s := range syms {
		bare := s.Name[strings.LastIndexAny(s.Name, ".:")+1:]
		if bare == sym && (line == 0 || s.Line == line) {
			di = i
			break
		}
	}
	if di < 0 {
		return nil, fmt.Errorf("no definition of %s in %s", sym, path)
	}
	def := syms[di]
	end := min(len(lines), def.Line-1+maxBodyLines)
	for _, s := range syms[di+1:] {
		if s.Depth <= def.Depth {
			end = min(end, s.Line-1)
			break
		}
	}
	res := &CalleesResult{Symbol: sym, Repo: repoName, Path: path, Line: def.Line, Callees: []*Callee{}}
	regions := scanRegions(langName, data)
	offset := 0
	for i := 0; i < def.Line-1; i++ {
		offset += len(lines[i]) + 1
	}
	byName := map[string]*Callee{}
	for l := def.Line - 1; l < end; l++ {
		text := lines[l]
		for _, m := range callRE.FindAllStringSubmatchIndex(text, -1) {
			name := text[m[2]:m[3]]
			if notCalls[name] || regionAt(regions, offset+m[2]) != regionCode {
				continue
			}
			if l == def.Line-1 && name == sym {
				continue // the definition's own name
			}
			if c := byName[name]; c != nil {
				c.Count++
				continue
			}
			if len(byName) >= maxCallees {
				continue
			}
			c := &Callee{Name: name, Line: l + 1, Count: 1}
			byName[name] = c
			res.Callees = append(res.Callees, c)
		}
		offset += len(text) + 1
	}

	// Resolve definitions from the symbol tables, nearest first.
	snaps := e.acquire(&Query{})
	fromLang := langName
	for _, c := range res.Callees {
		best := -1
		for _, d := range symbolDefs(snaps, c.Name) {
			if !funcKinds[d.Kind] {
				continue
			}
			dist := distance(d.Repo, d.Path, repoName, path, fromLang)
			if best < 0 || dist < best {
				best = dist
				c.Repo, c.Path, c.DefLine, c.Kind = d.Repo, d.Path, d.Line, d.Kind
			}
		}
	}
	for _, s := range snaps {
		s.release()
	}
	res.Elapsed = time.Since(start).Seconds()
	return res, nil
}
