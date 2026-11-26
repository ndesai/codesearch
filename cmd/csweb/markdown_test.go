// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"strings"
	"testing"
)

func TestRenderMarkdown(t *testing.T) {
	src := "# Title\n\nIntro with `code`.\n\n## Install it\n\n" +
		"See [the API](api.md#auth), [setup](../setup.md), [root](/README.md), " +
		"[space](my%20notes.md), [section](#install-it), [line](main.go#L10), " +
		"[web](https://example.com/x) and [bad](javascript:alert(1)).\n\n" +
		"![logo](img/logo.png)\n\n" +
		"<script>alert(1)</script>\n\n" +
		"| a | b |\n|---|---|\n| 1 | 2 |\n\n" +
		"- [x] done\n\n" +
		"```go\nfunc main() {}\n```\n"
	got := renderMarkdown("proj", "docs/guide/intro.md", "", []byte(src))
	for _, want := range []string{
		`<h1 id="md-title" data-line="1">Title</h1>`,
		`<h2 id="md-install-it" data-line="5">Install it</h2>`,
		`<code>code</code>`,
		`href="/f/proj/docs/guide/api.md#md-auth"`,
		`href="/f/proj/docs/setup.md"`,
		`href="/f/proj/README.md"`,
		`href="/f/proj/docs/guide/my%20notes.md"`,
		`href="#md-install-it"`,
		`href="/f/proj/docs/guide/main.go#L10"`,
		`href="https://example.com/x"`,
		`src="/raw/proj/docs/guide/img/logo.png"`,
		`<table>`,
		`type="checkbox"`,
		`<pre class="md-code"><code><span class="kd">func</span>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %s", want)
		}
	}
	for _, bad := range []string{"<script>", "javascript:"} {
		if strings.Contains(got, bad) {
			t.Errorf("output contains %s", bad)
		}
	}
	if t.Failed() {
		t.Log(got)
	}

	// Links keep the revision being browsed; links out of the repository
	// are left alone.
	got = renderMarkdown("proj", "README.md", "v1.0", []byte("[a](docs/a.md) [b](../other/b.md)"))
	for _, want := range []string{`href="/f/proj/docs/a.md?rev=v1.0"`, `href="../other/b.md"`} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %s:\n%s", want, got)
		}
	}
}
