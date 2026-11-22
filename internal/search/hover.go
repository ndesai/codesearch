// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package search

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/ndesai/codesearch/internal/lang"
)

// A HoverDef is a definition shown in a hover card.
type HoverDef struct {
	Repo      string `json:"repo"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Kind      string `json:"kind,omitempty"`
	Signature string `json:"signature"` // the definition's line(s)
	Doc       string `json:"doc,omitempty"`
}

// A HoverResult describes a symbol for a hover card.
type HoverResult struct {
	Symbol string      `json:"symbol"`
	Defs   []*HoverDef `json:"defs"`  // best first, at most a few
	Total  int         `json:"total"` // number of definitions found
}

const maxHoverDefs = 3

// Hover returns the definitions of sym nearest to the file the user is
// viewing, with their signatures and doc comments. It uses the symbol
// tables, falling back to search-based cross references for symbols the
// tables don't know (fields, constants, ...).
func (e *Engine) Hover(ctx context.Context, sym, repo, from string) (*HoverResult, error) {
	if !identRE.MatchString(sym) {
		return nil, fmt.Errorf("not an identifier: %q", sym)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	q := &Query{}
	snaps := e.acquire(q)
	type loc struct {
		repo *Repo
		path string
		line int
		kind string
		dist int
	}
	fromLang := lang.Detect(from).Name
	var locs []loc
	for _, d := range symbolDefs(snaps, sym) {
		locs = append(locs, loc{e.byName[d.Repo], d.Path, d.Line, d.Kind, distance(d.Repo, d.Path, repo, from, fromLang)})
	}
	for _, s := range snaps {
		s.release()
	}
	if len(locs) == 0 {
		x, err := e.Xref(ctx, sym, repo, from)
		if err != nil {
			return nil, err
		}
		for _, d := range x.Defs {
			locs = append(locs, loc{e.byName[d.Repo], d.Path, d.Line, "", d.rank})
		}
	}
	slices.SortStableFunc(locs, func(a, b loc) int {
		if a.dist != b.dist {
			return a.dist - b.dist
		}
		// A type over its constructors and methods of the same name.
		if ta, tb := isTypeKind(a.kind), isTypeKind(b.kind); ta != tb {
			if ta {
				return -1
			}
			return 1
		}
		if a.path != b.path {
			return strings.Compare(a.path, b.path)
		}
		return a.line - b.line
	})
	res := &HoverResult{Symbol: sym, Total: len(locs), Defs: []*HoverDef{}}
	for _, l := range locs {
		if len(res.Defs) >= maxHoverDefs || ctx.Err() != nil {
			break
		}
		if l.repo == nil {
			continue
		}
		data, err := os.ReadFile(l.repo.Root + string(os.PathSeparator) + filepathFromSlash(l.path))
		if err != nil {
			continue
		}
		sig, doc := signatureAndDoc(lang.Detect(l.path).Name, strings.Split(strings.ToValidUTF8(string(data), "�"), "\n"), l.line)
		res.Defs = append(res.Defs, &HoverDef{Repo: l.repo.Name, Path: l.path, Line: l.line, Kind: l.kind, Signature: sig, Doc: doc})
	}
	return res, nil
}

const (
	maxSigLines = 4
	maxDocLines = 30
	maxDocBytes = 4000
)

// signatureAndDoc returns the definition at line (1-based) of lines,
// continued over a few lines if its parentheses are unbalanced, and the
// comment that documents it: the comment block just above it (skipping
// annotations and decorators), or a Python docstring just below it.
func signatureAndDoc(langName string, lines []string, line int) (sig, doc string) {
	if line < 1 || line > len(lines) {
		return "", ""
	}
	i := line - 1
	sigLines := []string{strings.TrimRight(lines[i], "\r \t")}
	depth := strings.Count(lines[i], "(") - strings.Count(lines[i], ")")
	for j := i + 1; depth > 0 && j < len(lines) && len(sigLines) < maxSigLines; j++ {
		sigLines = append(sigLines, strings.TrimRight(lines[j], "\r \t"))
		depth += strings.Count(lines[j], "(") - strings.Count(lines[j], ")")
	}
	sig = strings.Join(dedent(sigLines), "\n")
	if len(sig) > 600 {
		sig = sig[:600] + "…"
	}

	hash := hashComments(langName)
	var block []string
	for j := i - 1; j >= 0 && len(block) < maxDocLines; j-- {
		t := strings.TrimSpace(lines[j])
		switch {
		case strings.HasPrefix(t, "@") || (strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") && langName == "C#"):
			if len(block) == 0 {
				continue // annotation or decorator between comment and definition
			}
		case isCommentLine(t, hash):
			block = append(block, t)
			continue
		}
		break
	}
	slices.Reverse(block)
	if len(block) == 0 && (langName == "Python" || langName == "Starlark") {
		block = docstring(lines, i+len(sigLines))
	}
	doc = cleanComment(block)
	if len(doc) > maxDocBytes {
		doc = doc[:maxDocBytes] + "…"
	}
	return sig, doc
}

func hashComments(langName string) bool {
	switch langName {
	case "Python", "Starlark", "Ruby", "Bash", "Zsh", "Makefile", "CMake", "Perl", "R", "Elixir", "Nim", "TOML", "YAML", "GN", "Android Init", "SELinux Policy", "PowerShell":
		return true
	}
	return false
}

func isCommentLine(t string, hash bool) bool {
	switch {
	case t == "":
		return false
	case strings.HasPrefix(t, "//"), strings.HasPrefix(t, "/*"), strings.HasPrefix(t, "*"), strings.HasPrefix(t, "--"):
		return true
	case hash && strings.HasPrefix(t, "#") && !strings.HasPrefix(t, "#!"):
		return true
	}
	return false
}

// docstring returns a Python docstring starting at lines[i], if any.
func docstring(lines []string, i int) []string {
	if i >= len(lines) {
		return nil
	}
	t := strings.TrimSpace(lines[i])
	var q string
	for _, p := range []string{`"""`, `'''`, `r"""`} {
		if strings.HasPrefix(t, p) {
			q = p[len(p)-3:]
			t = t[len(p):]
			break
		}
	}
	if q == "" {
		return nil
	}
	var out []string
	for j := i; j < len(lines) && len(out) < maxDocLines; j++ {
		if j > i {
			t = strings.TrimSpace(lines[j])
		}
		if k := strings.Index(t, q); k >= 0 {
			if s := strings.TrimSpace(t[:k]); s != "" {
				out = append(out, s)
			}
			return out
		}
		out = append(out, t)
	}
	return out
}

// cleanComment strips comment markers from a comment block.
func cleanComment(block []string) string {
	var out []string
	for _, l := range block {
		for _, p := range []string{"///", "//!", "//", "/**", "/*!", "/*", "*/", "**", "#", "--"} {
			if strings.HasPrefix(l, p) {
				l = l[len(p):]
				break
			}
		}
		l = strings.TrimSuffix(strings.TrimSpace(l), "*/")
		l = strings.TrimPrefix(l, "* ")
		if l == "*" {
			l = ""
		}
		out = append(out, strings.TrimRight(strings.TrimPrefix(l, " "), " "))
	}
	// Trim blank lines at either end.
	for len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		out = out[1:]
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// dedent removes the common leading whitespace of lines.
func dedent(lines []string) []string {
	common := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " \t"))
		if common < 0 || n < common {
			common = n
		}
	}
	if common <= 0 {
		return lines
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		if len(l) >= common {
			out[i] = l[common:]
		}
	}
	return out
}

func isTypeKind(k string) bool {
	switch k {
	case "class", "struct", "interface", "enum", "trait", "protocol", "union", "type", "typealias", "record", "object", "message":
		return true
	}
	return false
}
