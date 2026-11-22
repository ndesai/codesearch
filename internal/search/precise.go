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
	"github.com/ndesai/codesearch/internal/scip"
)

// XrefAt is like Xref, but with the 1-based line where sym was clicked.
// If the repository has a SCIP index covering that position, the result
// comes from it and is precise (distinguishing overloads and same-named
// symbols); otherwise it is search based.
func (e *Engine) XrefAt(ctx context.Context, sym, repo, from string, line int) (*XrefResult, error) {
	if r := e.byName[repo]; r != nil && line > 0 {
		if res := preciseXref(r, sym, from, line, e); res != nil {
			return res, nil
		}
	}
	return e.Xref(ctx, sym, repo, from)
}

// preciseXref answers from the repository's SCIP index, or returns nil
// if the index doesn't cover the position or the file changed since the
// index was built.
func preciseXref(r *Repo, sym, from string, line int, e *Engine) *XrefResult {
	idx := r.SCIPIndex()
	if idx == nil || !identRE.MatchString(sym) {
		return nil
	}
	start := time.Now()
	files := map[string][]string{} // path -> lines, or nil if unreadable or stale
	read := func(p string) []string {
		if l, ok := files[p]; ok {
			return l
		}
		abs, err := r.Abs(p)
		var lines []string
		if err == nil {
			if st, err := os.Stat(abs); err == nil && !st.ModTime().After(idx.Built) {
				if data, err := os.ReadFile(abs); err == nil {
					lines = strings.Split(string(data), "\n")
				}
			}
		}
		files[p] = lines
		return lines
	}
	cur := read(from)
	if cur == nil || line > len(cur) {
		return nil // the viewed file changed since the index was built
	}
	// The occurrence of sym on the clicked line.
	var occ *scip.Occurrence
	for _, o := range idx.At(from, line-1) {
		if scip.SymbolName(o.Symbol) == sym || textAt(cur[line-1], o.StartCol, o.EndCol) == sym {
			occ = &o
			break
		}
	}
	if occ == nil {
		return nil
	}
	fromLang := lang.Detect(from).Name
	nameRe := stdregexp.MustCompile(`\b` + stdregexp.QuoteMeta(sym) + `\b`)
	res := &XrefResult{Symbol: sym, Precise: true, Defs: []*Location{}}
	stale := map[string]bool{}
	lineAt := func(l scip.Location) (string, [][2]int, bool) {
		lines := read(l.Path)
		if lines == nil || l.Line >= len(lines) {
			stale[l.Path] = true
			return "", nil, false
		}
		text, ranges := clipLine(strings.TrimRight(lines[l.Line], "\r"), matchRanges(lines[l.Line], nameRe))
		return text, ranges, true
	}
	for _, d := range idx.Definitions(occ.Symbol) {
		if text, ranges, ok := lineAt(d); ok {
			res.Defs = append(res.Defs, &Location{Repo: r.Name, Path: d.Path, Line: d.Line + 1, Text: text, Ranges: ranges,
				rank: distance(r.Name, d.Path, r.Name, from, fromLang)})
		}
	}
	slices.SortStableFunc(res.Defs, func(a, b *Location) int { return a.rank - b.rank })

	byFile := map[string]*FileMatch{}
	for _, ref := range idx.References(occ.Symbol) {
		text, ranges, ok := lineAt(ref)
		if !ok {
			continue
		}
		fm := byFile[ref.Path]
		if fm == nil {
			fm = &FileMatch{Repo: r.Name, Path: ref.Path, Lang: lang.Detect(ref.Path).Name,
				Score: -float64(distance(r.Name, ref.Path, r.Name, from, fromLang))}
			byFile[ref.Path] = fm
		}
		if n := len(fm.Lines); n > 0 && fm.Lines[n-1].Line == ref.Line+1 {
			continue // several occurrences on one line
		}
		fm.Lines = append(fm.Lines, LineMatch{Line: ref.Line + 1, Text: text, Ranges: ranges})
		fm.NumMatches++
		res.NumRefs++
	}
	for _, fm := range byFile {
		slices.SortFunc(fm.Lines, func(a, b LineMatch) int { return a.Line - b.Line })
		res.Refs = append(res.Refs, fm)
	}
	slices.SortStableFunc(res.Refs, func(a, b *FileMatch) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		}
		return strings.Compare(a.Path, b.Path)
	})
	res.NumFiles = len(byFile)
	res.StaleFiles = len(stale)
	res.Elapsed = time.Since(start).Seconds()
	return res
}

// textAt returns line[start:end], or "" if out of range.
func textAt(line string, start, end int) string {
	if start < 0 || end > len(line) || start >= end {
		return ""
	}
	return line[start:end]
}
