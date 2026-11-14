// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"html"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// A seg is a run of text with a CSS class (the Chroma short class name).
type seg struct {
	text  string
	class string
}

// Limits on syntax highlighting, so huge or pathological files stay fast.
const (
	maxHighlightBytes = 2 << 20
	highlightBudget   = 1500 * time.Millisecond
)

// tokenClass returns the CSS class for a token type.
func tokenClass(t chroma.TokenType) string {
	for {
		if t == chroma.Text || t == chroma.TextWhitespace || t == chroma.Background {
			return ""
		}
		if c, ok := chroma.StandardTypes[t]; ok {
			return c
		}
		switch {
		case t.SubCategory() != t:
			t = t.SubCategory()
		case t.Category() != t:
			t = t.Category()
		default:
			return ""
		}
	}
}

// tokenizeLines splits src into lines of highlighted segments.
// If lexer is nil, or highlighting takes too long, the remaining
// text is returned unhighlighted.
func tokenizeLines(lexer chroma.Lexer, src string) [][]seg {
	lines := [][]seg{nil}
	add := func(text, class string) {
		for {
			i := strings.IndexByte(text, '\n')
			if i < 0 {
				break
			}
			if i > 0 {
				lines[len(lines)-1] = append(lines[len(lines)-1], seg{strings.TrimSuffix(text[:i], "\r"), class})
			}
			lines = append(lines, nil)
			text = text[i+1:]
		}
		if text != "" {
			lines[len(lines)-1] = append(lines[len(lines)-1], seg{text, class})
		}
	}
	consumed := 0
	if lexer != nil && len(src) <= maxHighlightBytes {
		func() {
			defer func() { recover() }() // lexer bugs must not break the page
			it, err := chroma.Coalesce(lexer).Tokenise(&chroma.TokeniseOptions{State: "root", EnsureLF: false}, src)
			if err != nil {
				return
			}
			deadline := time.Now().Add(highlightBudget)
			for n := 0; ; n++ {
				if n%512 == 0 && time.Now().After(deadline) {
					return
				}
				tok := it()
				if tok == chroma.EOF {
					return
				}
				if consumed+len(tok.Value) > len(src) || src[consumed:consumed+len(tok.Value)] != tok.Value {
					// Lexer rewrote the text (e.g. normalized newlines);
					// fall back to plain text from here on.
					return
				}
				consumed += len(tok.Value)
				add(tok.Value, tokenClass(tok.Type))
			}
		}()
	}
	add(src[consumed:], "")
	if len(lines) > 1 && len(lines[len(lines)-1]) == 0 && strings.HasSuffix(src, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// renderLine renders segments as HTML, wrapping the byte ranges
// (relative to the line's text) in <mark> elements.
func renderLine(segs []seg, ranges [][2]int) string {
	var b strings.Builder
	off := 0
	ri := 0
	for _, s := range segs {
		text := s.text
		start := off
		off += len(text)
		if s.class != "" {
			b.WriteString(`<span class="`)
			b.WriteString(s.class)
			b.WriteString(`">`)
		}
		pos := start
		for pos < off {
			for ri < len(ranges) && ranges[ri][1] <= pos {
				ri++
			}
			if ri < len(ranges) && ranges[ri][0] <= pos {
				end := min(ranges[ri][1], off)
				b.WriteString("<mark>")
				b.WriteString(html.EscapeString(text[pos-start : end-start]))
				b.WriteString("</mark>")
				pos = end
				continue
			}
			end := off
			if ri < len(ranges) && ranges[ri][0] < end {
				end = ranges[ri][0]
			}
			b.WriteString(html.EscapeString(text[pos-start : end-start]))
			pos = end
		}
		if s.class != "" {
			b.WriteString("</span>")
		}
	}
	return b.String()
}

// highlightSnippet highlights a single line of code from a file.
func highlightSnippet(lexer chroma.Lexer, text string, ranges [][2]int) string {
	lines := tokenizeLines(lexer, text)
	var segs []seg
	for i, l := range lines {
		if i > 0 {
			segs = append(segs, seg{"\n", ""})
		}
		segs = append(segs, l...)
	}
	return renderLine(segs, ranges)
}

// lexerFor returns the lexer for a file, guessing from content for
// files whose names don't identify a language (e.g. scripts).
func lexerFor(l chroma.Lexer, src string) chroma.Lexer {
	if l != nil {
		return l
	}
	if strings.HasPrefix(src, "#!") || strings.HasPrefix(src, "<?xml") || strings.HasPrefix(src, "<!DOCTYPE") {
		return lexers.Analyse(src[:min(len(src), 4096)])
	}
	return nil
}
