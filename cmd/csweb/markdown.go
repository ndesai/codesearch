// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// renderMarkdown renders a Markdown file (GitHub-flavored) as HTML for
// the file view's preview. rel is the file's path in the repository, and
// rev the revision being browsed ("" for the working tree).
//
// The output is shown inside the app, so raw HTML in the document is
// left out (goldmark's default), and URLs are limited to safe schemes.
// Relative links are rewritten to open in the app, and relative images
// to load from /raw/. Headings get ids prefixed with "md-" (so they
// can't collide with the app's own ids) and a data-line attribute with
// their line number, so outline entries can scroll to them.
func renderMarkdown(repo, rel, rev string, src []byte) string {
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM, extension.Footnote),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
			parser.WithASTTransformers(util.Prioritized(&mdLinks{repo: repo, rel: rel, rev: rev}, 100)),
		),
		goldmark.WithRendererOptions(renderer.WithNodeRenderers(util.Prioritized(mdCode{}, 100))),
	)
	var buf bytes.Buffer
	if err := md.Convert(src, &buf); err != nil {
		return ""
	}
	return buf.String()
}

// mdLinks rewrites link and image destinations and annotates headings.
type mdLinks struct {
	repo, rel, rev string
}

func (t *mdLinks) Transform(doc *ast.Document, reader text.Reader, pc parser.Context) {
	src := reader.Source()
	var starts []int // offsets at which lines start
	starts = append(starts, 0)
	for i, c := range src {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	lineOf := func(off int) int { return sort.SearchInts(starts, off+1) }
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Heading:
			if id, ok := n.AttributeString("id"); ok {
				if b, ok := id.([]byte); ok {
					n.SetAttributeString("id", []byte("md-"+string(b)))
				}
			}
			if n.Lines().Len() > 0 {
				n.SetAttributeString("data-line", []byte(strconv.Itoa(lineOf(n.Lines().At(0).Start))))
			}
		case *ast.Link:
			n.Destination = []byte(t.link(string(n.Destination), false))
		case *ast.Image:
			n.Destination = []byte(t.link(string(n.Destination), true))
		}
		return ast.WalkContinue, nil
	})
}

// link returns the URL to use for a link (or image) destination.
func (t *mdLinks) link(dest string, image bool) string {
	if dest == "" || strings.HasPrefix(dest, "//") {
		return dest
	}
	if u, err := url.Parse(dest); err != nil || u.Scheme != "" {
		return dest // absolute: goldmark drops unsafe schemes such as javascript:
	}
	if strings.HasPrefix(dest, "#") {
		return "#md-" + dest[1:]
	}
	p, frag, _ := strings.Cut(dest, "#")
	p, _, _ = strings.Cut(p, "?")
	if up, err := url.PathUnescape(p); err == nil {
		p = up
	}
	if strings.HasPrefix(p, "/") {
		p = path.Clean(strings.TrimLeft(p, "/"))
	} else {
		p = path.Join(path.Dir(t.rel), p)
	}
	if p == ".." || strings.HasPrefix(p, "../") {
		return dest // outside the repository
	}
	if p == "." {
		p = ""
	}
	var esc []string
	for _, s := range strings.Split(p, "/") {
		esc = append(esc, url.PathEscape(s))
	}
	ep := strings.Join(esc, "/")
	if image {
		return "/raw/" + url.PathEscape(t.repo) + "/" + ep
	}
	u := "/f/" + url.PathEscape(t.repo)
	if ep != "" {
		u += "/" + ep
	}
	if t.rev != "" {
		u += "?rev=" + url.QueryEscape(t.rev)
	}
	if frag != "" {
		if strings.HasPrefix(frag, "L") {
			u += "#" + frag // a line in a source file
		} else {
			u += "#md-" + frag
		}
	}
	return u
}

// mdCode renders code blocks with the same syntax highlighting as the
// file view.
type mdCode struct{}

func (mdCode) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindFencedCodeBlock, renderMDCode)
	reg.Register(ast.KindCodeBlock, renderMDCode)
}

func renderMDCode(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	var code strings.Builder
	for i := 0; i < n.Lines().Len(); i++ {
		seg := n.Lines().At(i)
		code.Write(seg.Value(src))
	}
	var lx chroma.Lexer
	if f, ok := n.(*ast.FencedCodeBlock); ok {
		if name := string(f.Language(src)); name != "" {
			lx = lexers.Get(name)
		}
	}
	lines := tokenizeLines(lx, code.String())
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	w.WriteString(`<pre class="md-code"><code>`)
	for i, l := range lines {
		if i > 0 {
			w.WriteByte('\n')
		}
		w.WriteString(renderLine(l, nil))
	}
	w.WriteString("</code></pre>\n")
	return ast.WalkSkipChildren, nil
}
