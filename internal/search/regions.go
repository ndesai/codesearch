// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package search

import (
	"bytes"
	"sort"
)

// Regions classify the bytes of a source file as code, comment or
// string literal, for the usage:, comment: and string: filters. The
// classification comes from a small per-language table of comment and
// quote syntax: much faster than full syntax highlighting, and right for
// the common cases.

// A region is the part of a file a match must fall in.
type region uint8

const (
	regionAny     region = iota // no restriction
	regionCode                  // outside comments and string literals
	regionComment               // inside a comment
	regionString                // inside a string literal
)

// A regionSpan is a comment or string occupying bytes [lo, hi).
type regionSpan struct {
	lo, hi int
	kind   region
}

type quote struct {
	open, close string
	multiline   bool // may span lines (otherwise it ends at a newline)
	raw         bool // backslash does not escape
}

type syntaxDef struct {
	line   []string    // line comment starts
	block  [][2]string // block comment delimiters
	quotes []quote     // string literal delimiters, longest first
}

var (
	dq       = quote{open: `"`, close: `"`}
	sq       = quote{open: `'`, close: `'`}
	backtick = quote{open: "`", close: "`", multiline: true}
	cBlock   = [][2]string{{"/*", "*/"}}

	cSyntax    = &syntaxDef{line: []string{"//"}, block: cBlock, quotes: []quote{dq, sq}}
	goSyntax   = &syntaxDef{line: []string{"//"}, block: cBlock, quotes: []quote{dq, sq, {open: "`", close: "`", multiline: true, raw: true}}}
	jsSyntax   = &syntaxDef{line: []string{"//"}, block: cBlock, quotes: []quote{dq, sq, backtick}}
	rustSyntax = &syntaxDef{line: []string{"//"}, block: cBlock, quotes: []quote{dq}} // ' is also a lifetime
	kotlinSyn  = &syntaxDef{line: []string{"//"}, block: cBlock, quotes: []quote{{open: `"""`, close: `"""`, multiline: true, raw: true}, dq, sq}}
	pySyntax   = &syntaxDef{line: []string{"#"}, quotes: []quote{
		{open: `"""`, close: `"""`, multiline: true}, {open: `'''`, close: `'''`, multiline: true}, dq, sq}}
	hashSyntax = &syntaxDef{line: []string{"#"}, quotes: []quote{dq, sq}}
	sqlSyntax  = &syntaxDef{line: []string{"--"}, block: cBlock, quotes: []quote{sq, dq}}
	luaSyntax  = &syntaxDef{line: []string{"--"}, block: [][2]string{{"--[[", "]]"}}, quotes: []quote{dq, sq}}
	xmlSyntax  = &syntaxDef{block: [][2]string{{"<!--", "-->"}}, quotes: []quote{dq}}
	cssSyntax  = &syntaxDef{block: cBlock, quotes: []quote{dq, sq}}
	phpSyntax  = &syntaxDef{line: []string{"//", "#"}, block: cBlock, quotes: []quote{dq, sq}}
)

var syntaxByLang = map[string]*syntaxDef{
	"Go": goSyntax, "Go Module": goSyntax,
	"JavaScript": jsSyntax, "TypeScript": jsSyntax, "TSX": jsSyntax, "JSX": jsSyntax, "vue": jsSyntax, "Svelte": jsSyntax,
	"Rust": rustSyntax, "Kotlin": kotlinSyn, "Swift": kotlinSyn, "Scala": kotlinSyn, "Groovy": kotlinSyn,
	"Python": pySyntax, "Starlark": pySyntax, "GN": hashSyntax,
	"Ruby": hashSyntax, "Bash": hashSyntax, "Zsh": hashSyntax, "Fish": hashSyntax, "Makefile": hashSyntax,
	"CMake": hashSyntax, "Perl": hashSyntax, "R": hashSyntax, "Elixir": hashSyntax, "Nim": hashSyntax,
	"YAML": hashSyntax, "TOML": hashSyntax, "Android Init": hashSyntax, "SELinux Policy": hashSyntax,
	"PowerShell": hashSyntax, "Properties": hashSyntax, "INI": hashSyntax, "Docker": hashSyntax,
	"SQL": sqlSyntax, "MySQL": sqlSyntax, "PostgreSQL SQL dialect": sqlSyntax, "Haskell": sqlSyntax,
	"Lua": luaSyntax,
	"XML": xmlSyntax, "HTML": xmlSyntax, "SVG": xmlSyntax,
	"CSS": cssSyntax, "SCSS": cSyntax, "Less": cSyntax,
	"PHP": phpSyntax,
}

func syntaxFor(langName string) *syntaxDef {
	if s := syntaxByLang[langName]; s != nil {
		return s
	}
	if cLike[langName] || javaLike[langName] || langName == "Blueprint" || langName == "Protocol Buffer" {
		return cSyntax
	}
	return nil
}

// scanRegions returns the comments and string literals in data, in order.
// It returns nil for languages without known syntax, so all of such a
// file counts as code.
func scanRegions(langName string, data []byte) []regionSpan {
	syn := syntaxFor(langName)
	if syn == nil {
		return nil
	}
	var spans []regionSpan
	hasAt := func(i int, s string) bool { return bytes.HasPrefix(data[i:], []byte(s)) }
	for i := 0; i < len(data); {
		c := data[i]
		matched := false
		for _, b := range syn.block {
			if c == b[0][0] && hasAt(i, b[0]) {
				end := bytes.Index(data[i+len(b[0]):], []byte(b[1]))
				hi := len(data)
				if end >= 0 {
					hi = i + len(b[0]) + end + len(b[1])
				}
				spans = append(spans, regionSpan{i, hi, regionComment})
				i, matched = hi, true
				break
			}
		}
		if matched {
			continue
		}
		for _, l := range syn.line {
			if c == l[0] && hasAt(i, l) {
				hi := i + bytes.IndexByte(data[i:], '\n')
				if hi < i {
					hi = len(data)
				}
				spans = append(spans, regionSpan{i, hi, regionComment})
				i, matched = hi, true
				break
			}
		}
		if matched {
			continue
		}
		for _, q := range syn.quotes {
			if c == q.open[0] && hasAt(i, q.open) {
				hi := scanString(data, i+len(q.open), q)
				spans = append(spans, regionSpan{i, hi, regionString})
				i, matched = hi, true
				break
			}
		}
		if !matched {
			i++
		}
	}
	return spans
}

// scanString returns the end of the string literal whose contents start
// at i (just after the opening quote).
func scanString(data []byte, i int, q quote) int {
	for i < len(data) {
		c := data[i]
		switch {
		case c == '\\' && !q.raw:
			i += 2
			continue
		case c == '\n' && !q.multiline:
			return i // unterminated: stop at the end of the line
		case c == q.close[0] && bytes.HasPrefix(data[i:], []byte(q.close)):
			return i + len(q.close)
		}
		i++
	}
	return len(data)
}

// regionAt returns the region of the byte at offset off.
func regionAt(spans []regionSpan, off int) region {
	i := sort.Search(len(spans), func(i int) bool { return spans[i].hi > off })
	if i < len(spans) && spans[i].lo <= off {
		return spans[i].kind
	}
	return regionCode
}
