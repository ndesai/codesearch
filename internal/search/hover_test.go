// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package search

import (
	"context"
	"strings"
	"testing"
)

func TestSignatureAndDoc(t *testing.T) {
	tests := []struct {
		lang, src string
		line      int
		sig, doc  string
	}{
		{"Go", "package x\n\n// NewReader returns a new Reader.\n// It never fails.\nfunc NewReader(rd io.Reader) *Reader {\n", 5,
			"func NewReader(rd io.Reader) *Reader {", "NewReader returns a new Reader.\nIt never fails."},
		{"Java", "  /**\n   * Called when created.\n   *\n   * @param b state\n   */\n  @Override\n  protected void onCreate(Bundle b) {\n", 7,
			"protected void onCreate(Bundle b) {", "Called when created.\n\n@param b state"},
		{"Python", "def main(argv,\n         env):\n    \"\"\"Run the tool.\n\n    Returns an exit code.\n    \"\"\"\n", 1,
			"def main(argv,\n         env):", "Run the tool.\n\nReturns an exit code."},
		{"Python", "# Helper.\ndef f():\n    pass\n", 2, "def f():", "Helper."},
		{"C++", "#include <x>\nbool Rect::intersect(const Rect& with,\n                     Rect* result) const {\n", 2,
			"bool Rect::intersect(const Rect& with,\n                     Rect* result) const {", ""},
		{"Go", "x := 1\nfunc F() {}\n", 2, "func F() {}", ""},
	}
	for _, tt := range tests {
		sig, doc := signatureAndDoc(tt.lang, strings.Split(tt.src, "\n"), tt.line)
		if sig != tt.sig || doc != tt.doc {
			t.Errorf("%s line %d:\nsig %q\nwant %q\ndoc %q\nwant %q", tt.lang, tt.line, sig, tt.sig, doc, tt.doc)
		}
	}
}

func TestHover(t *testing.T) {
	e := newTestEngine(t)
	r, err := e.Hover(context.Background(), "NewModule", "aosp", "tools/test/build_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 1 || len(r.Defs) != 1 || r.Defs[0].Path != "tools/build.go" || r.Defs[0].Line != 7 ||
		r.Defs[0].Signature != "func NewModule(name string) *Module {" {
		t.Fatalf("Hover(NewModule) = %+v %+v", r, r.Defs)
	}
	// A type is preferred over its constructor.
	r, _ = e.Hover(context.Background(), "Activity", "aosp", "core/java/android/app/ActivityThread.java")
	if r.Total != 2 || r.Defs[0].Kind != "class" {
		t.Errorf("Hover(Activity) first def = %+v", r.Defs[0])
	}
	// Not in the symbol tables (a constant): falls back to cross references.
	r, _ = e.Hover(context.Background(), "TAG", "aosp", "core/java/android/app/Activity.java")
	if r.Total != 1 || r.Defs[0].Line != 4 {
		t.Errorf("Hover(TAG) = %+v", r)
	}
	if _, err := e.Hover(context.Background(), "not an ident", "aosp", ""); err == nil {
		t.Error("bad symbol accepted")
	}
}
