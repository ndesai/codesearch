// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package search

import (
	"context"
	"os"
	stdregexp "regexp"
	"slices"
	"strings"
	"time"

	"github.com/ndesai/codesearch/internal/lang"
	"github.com/ndesai/codesearch/internal/symbols"
)

// A SymFilter is a class:, function: or symbol: query: definitions whose
// names match a regular expression, optionally of certain kinds and
// inside a named scope (Reader.ReadSlice, Rect::intersect).
type SymFilter struct {
	Kinds map[string]bool // nil means any kind
	Name  *stdregexp.Regexp
	Scope *stdregexp.Regexp // nil if unqualified
	Text  string            // as written, for ranking exact names first
}

var (
	typeKinds = set("class", "struct", "interface", "enum", "trait", "protocol", "object", "record", "union",
		"message", "service", "extension", "actor", "mixin", "typealias", "type", "impl", "namespace", "module", "defmodule")
	funcKinds = set("func", "method", "macro", "rpc")
)

// qualifiedRE matches Scope.name or Scope::name, where name starts with
// an identifier character (so "on.*" is a regexp, not scope "on").
var qualifiedRE = stdregexp.MustCompile(`^([A-Za-z_$][\w$]*)(?:::|\.)([A-Za-z_$].*)$`)

// newSymFilter parses the value of a class:, function: or symbol: filter.
func newSymFilter(key, val, caseMode string) (*SymFilter, error) {
	f := &SymFilter{Text: val}
	switch key {
	case "class":
		f.Kinds = typeKinds
	case "function", "func":
		f.Kinds = funcKinds
	}
	name := val
	if m := qualifiedRE.FindStringSubmatch(val); m != nil {
		f.Scope = stdregexp.MustCompile(smartCase(m[1], caseMode) + `^` + stdregexp.QuoteMeta(m[1]) + `$`)
		name = m[2]
	}
	re, err := stdregexp.Compile(smartCase(name, caseMode) + `^(?:` + name + `)$`)
	if err != nil {
		re = stdregexp.MustCompile(smartCase(name, caseMode) + `^` + stdregexp.QuoteMeta(name) + `$`)
	}
	f.Name = re
	return f, nil
}

const maxSymbolResults = 2000

// symbolSearch answers a query with class:, function: or symbol:
// filters from the symbol tables.
func (e *Engine) symbolSearch(ctx context.Context, q *Query, opt Options) *Result {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, opt.Timeout)
	defer cancel()
	res := &Result{Query: q.Raw}
	snaps := e.acquire(q)
	defer func() {
		for _, s := range snaps {
			s.release()
		}
	}()

	type hit struct {
		repo *Repo
		symbols.Entry
		rank int
	}
	var hits []hit
	for _, s := range snaps {
		for _, sh := range s.shards {
			if ctx.Err() != nil || len(hits) >= maxSymbolResults {
				break
			}
			var files map[string][]symbols.Symbol // lazily, for scope checks
			sh.syms.Match(func(name string) bool {
				for _, f := range q.SymFilters {
					if f.Name.MatchString(name) {
						return true
					}
				}
				return false
			}, func(en symbols.Entry) bool {
				if !q.PathMatch(en.Path) {
					return true
				}
				for _, f := range q.SymFilters {
					if !f.Name.MatchString(en.Name) || f.Kinds != nil && !f.Kinds[en.Kind] {
						continue
					}
					if f.Scope != nil {
						if files == nil {
							files = map[string][]symbols.Symbol{}
							for _, sf := range sh.syms.Files() {
								files[sf.Path] = sf.Symbols
							}
						}
						if !inScope(s.repo, en, files[en.Path], f.Scope) {
							continue
						}
					}
					rank := 2
					switch {
					case strings.EqualFold(en.Name, f.Text) || f.Scope != nil:
						rank = 0
					case strings.HasPrefix(strings.ToLower(en.Name), strings.ToLower(strings.Trim(f.Text, "^$"))):
						rank = 1
					}
					hits = append(hits, hit{s.repo, en, rank})
					break
				}
				return len(hits) < maxSymbolResults
			})
		}
	}
	res.Truncated = len(hits) >= maxSymbolResults

	// Group by file, reading each file once for the definition lines.
	byFile := map[string]*FileMatch{}
	var order []string
	best := map[string]int{}
	for _, h := range hits {
		key := h.repo.Name + "\x00" + h.Path
		fm := byFile[key]
		if fm == nil {
			fm = &FileMatch{Repo: h.repo.Name, Path: h.Path, Lang: lang.Detect(h.Path).Name}
			classifyFile(fm, nil)
			byFile[key] = fm
			order = append(order, key)
			best[key] = h.rank
		}
		best[key] = min(best[key], h.rank)
		fm.Lines = append(fm.Lines, LineMatch{Line: h.Line, Def: true, Text: h.Name})
	}
	nameTerm := &Term{}
	for _, key := range order {
		if ctx.Err() != nil {
			res.TimedOut = true
			break
		}
		fm := byFile[key]
		repo := e.byName[fm.Repo]
		data, err := os.ReadFile(repo.Root + string(os.PathSeparator) + filepathFromSlash(fm.Path))
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		slices.SortFunc(fm.Lines, func(a, b LineMatch) int { return a.Line - b.Line })
		for i := range fm.Lines {
			l := &fm.Lines[i]
			if l.Line < 1 || l.Line > len(lines) {
				continue
			}
			nameTerm.Std = stdregexp.MustCompile(`\b` + stdregexp.QuoteMeta(l.Text) + `\b`)
			l.Text, l.Ranges = snippet([]byte(lines[l.Line-1]), []*Term{nameTerm})
		}
		fm.NumMatches = len(fm.Lines)
		// Exact names first, then shallower paths.
		fm.Score = float64(10-5*best[key]) - 0.25*float64(strings.Count(fm.Path, "/"))
		res.Files = append(res.Files, fm)
		res.NumMatches += fm.NumMatches
	}
	slices.SortStableFunc(res.Files, func(a, b *FileMatch) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Path, b.Path)
	})
	res.NumFiles = len(res.Files)
	res.Facets = facets(res.Files)
	res.Elapsed = time.Since(start).Seconds()
	return res
}

// inScope reports whether the definition en is inside scope: the
// nearest enclosing symbol (by outline depth) has that name, or the
// definition line names it (a Go receiver, Foo::bar, Foo.bar).
func inScope(repo *Repo, en symbols.Entry, fileSyms []symbols.Symbol, scope *stdregexp.Regexp) bool {
	if en.Depth > 0 {
		for i := len(fileSyms) - 1; i >= 0; i-- {
			s := fileSyms[i]
			if s.Line < en.Line && s.Depth < en.Depth {
				return scope.MatchString(s.Name)
			}
		}
	}
	data, err := os.ReadFile(repo.Root + string(os.PathSeparator) + filepathFromSlash(en.Path))
	if err != nil {
		return false
	}
	lines := strings.Split(string(data), "\n")
	if en.Line < 1 || en.Line > len(lines) {
		return false
	}
	line := lines[en.Line-1]
	for _, w := range wordRE.FindAllString(line[:max(0, strings.Index(line, en.Name))], -1) {
		if scope.MatchString(w) {
			return true
		}
	}
	return false
}

var wordRE = stdregexp.MustCompile(`[A-Za-z_$][\w$]*`)
