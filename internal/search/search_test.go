// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package search

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/codesearch/internal/config"
	"github.com/google/codesearch/internal/indexer"
)

var testFiles = map[string]string{
	"core/java/android/app/Activity.java": `package android.app;

public class Activity extends ContextThemeWrapper {
    private static final String TAG = "Activity";
    private Window mWindow;

    public Activity() {
    }

    protected void onCreate(Bundle savedInstanceState) {
        mWindow = new Window();
        Log.d(TAG, "onCreate");
    }
}
`,
	"core/java/android/app/ActivityThread.java": `package android.app;

public final class ActivityThread {
    void handleLaunch() {
        Activity a = new Activity();
        a.onCreate(null);
    }
}
`,
	"native/libs/ui/Rect.cpp": `#include "Rect.h"

namespace android {

bool Rect::intersect(const Rect& with, Rect* result) const {
    return result->left < with.right;
}

}  // namespace android
`,
	"native/libs/ui/Rect.h": `#pragma once
#define RECT_MAGIC 42

struct Rect {
    int left, right;
    bool intersect(const Rect& with, Rect* result) const;
};
`,
	"tools/build.go": `package build

type Module struct {
	Name string
}

func NewModule(name string) *Module {
	return &Module{Name: name}
}
`,
	"tools/Android.bp": `cc_library {
    name: "libui",
    srcs: ["Rect.cpp"],
}
`,
	"tools/test/build_test.go": "package build\n\nfunc TestNewModule() { NewModule(\"x\") }\n",
	"web/app.ts": `export function renderPage(id: string) {}
export const fetchData = async (url: string) => {};
class Store {
  count: number;
}
`,
	"scripts/run.py": "def main():\n    pass\n\nMAX_SIZE = 10\n",
}

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	root := t.TempDir()
	for name, content := range testFiles {
		p := filepath.Join(root, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0755)
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "other.go"), []byte("package other\n\n// Activity in another repo\n"), 0644)

	yaml := fmt.Sprintf("index_dir: %s\nshard_files: 3\nrepos:\n  - path: %s\n    name: aosp\n  - path: %s\n    name: other\n",
		filepath.Join(t.TempDir(), "idx"), root, other)
	cfg, err := config.Parse(filepath.Join(root, "c.yaml"), []byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Update(cfg, indexer.Options{Logf: t.Logf}); err != nil {
		t.Fatal(err)
	}
	return New(cfg)
}

func search(t *testing.T, e *Engine, query string) *Result {
	t.Helper()
	q, err := ParseQuery(query)
	if err != nil {
		t.Fatalf("ParseQuery(%q): %v", query, err)
	}
	return e.Search(context.Background(), q, Options{})
}

func paths(r *Result) string {
	var out []string
	for _, f := range r.Files {
		out = append(out, f.Repo+":"+f.Path)
	}
	return strings.Join(out, " ")
}

func TestSearch(t *testing.T) {
	e := newTestEngine(t)
	tests := []struct {
		query string
		want  string // space-separated repo:path in rank order
	}{
		{"onCreate", "aosp:core/java/android/app/Activity.java aosp:core/java/android/app/ActivityThread.java"},
		{"oncreate", "aosp:core/java/android/app/Activity.java aosp:core/java/android/app/ActivityThread.java"},
		{"onCreate case:yes file:Thread", "aosp:core/java/android/app/ActivityThread.java"},
		{"oncreate case:yes", ""},
		{"onCreate -file:Thread", "aosp:core/java/android/app/Activity.java"},
		{"onCreate mWindow", "aosp:core/java/android/app/Activity.java"},
		{"onCreate -mWindow", "aosp:core/java/android/app/ActivityThread.java"},
		{`"Rect* result"`, "aosp:native/libs/ui/Rect.cpp aosp:native/libs/ui/Rect.h"},
		{"intersect(", "aosp:native/libs/ui/Rect.cpp aosp:native/libs/ui/Rect.h"},
		{"intersect lang:c++ file:cpp", "aosp:native/libs/ui/Rect.cpp"},
		{"NewModule", "aosp:tools/build.go aosp:tools/test/build_test.go"},
		{"Activity repo:other", "other:other.go"},
		{"Activity -repo:aosp", "other:other.go"},
		{"lang:go -repo:other", "aosp:tools/build.go aosp:tools/test/build_test.go"},
		{"file:Android.bp", "aosp:tools/Android.bp"},
		{"sym:Rect", "aosp:native/libs/ui/Rect.h"},
		{"sym:NewModule", "aosp:tools/build.go"},
		{`R.ct::inter`, "aosp:native/libs/ui/Rect.cpp"},
	}
	for _, tt := range tests {
		r := search(t, e, tt.query)
		if got := paths(r); got != tt.want {
			t.Errorf("%q:\n got %s\nwant %s", tt.query, got, tt.want)
		}
	}

	r := search(t, e, "mWindow")
	if len(r.Files) != 1 || len(r.Files[0].Lines) != 2 {
		t.Fatalf("mWindow: %+v", r.Files)
	}
	l := r.Files[0].Lines[0]
	if !l.Def || l.Line != 5 || l.Text[l.Ranges[0][0]:l.Ranges[0][1]] != "mWindow" {
		t.Errorf("mWindow first line: %+v", l)
	}

	r = search(t, e, "rect")
	if len(r.NameMatches) != 2 || !strings.HasPrefix(r.NameMatches[0].Path, "native/libs/ui/Rect.") {
		t.Errorf("name matches for rect: %+v", r.NameMatches)
	}
}

func TestFolderNameMatches(t *testing.T) {
	e := newTestEngine(t)
	// "tools" names a top-level folder but no file.
	r := search(t, e, "tools")
	if len(r.NameMatches) == 0 || !r.NameMatches[0].Dir || r.NameMatches[0].Path != "tools" {
		t.Errorf("search tools: name matches %+v", r.NameMatches)
	}
	s := e.Suggest(context.Background(), "libs", 10)
	if len(s) == 0 || !s[0].Dir || s[0].Path != "native/libs" {
		t.Errorf("suggest libs: %+v", s)
	}
	// Directories have no language.
	r = search(t, e, "tools lang:go")
	for _, n := range r.NameMatches {
		if n.Dir {
			t.Errorf("lang: query returned folder %s", n.Path)
		}
	}
}

func TestSuggest(t *testing.T) {
	e := newTestEngine(t)
	s := e.Suggest(context.Background(), "activity", 10)
	if len(s) != 2 || s[0].Path != "core/java/android/app/Activity.java" {
		t.Errorf("suggest: %+v", s)
	}
}

func TestXref(t *testing.T) {
	e := newTestEngine(t)
	r, err := e.Xref(context.Background(), "intersect", "aosp", "native/libs/ui/Rect.cpp")
	if err != nil {
		t.Fatal(err)
	}
	var defs []string
	for _, d := range r.Defs {
		defs = append(defs, fmt.Sprintf("%s:%d", d.Path, d.Line))
	}
	if got := strings.Join(defs, " "); got != "native/libs/ui/Rect.cpp:5 native/libs/ui/Rect.h:6" {
		t.Errorf("intersect defs: %s", got)
	}

	r, _ = e.Xref(context.Background(), "Activity", "aosp", "core/java/android/app/ActivityThread.java")
	defs = nil
	for _, d := range r.Defs {
		defs = append(defs, fmt.Sprintf("%s:%d", d.Path, d.Line))
	}
	if got := strings.Join(defs, " "); got != "core/java/android/app/Activity.java:3 core/java/android/app/Activity.java:7" {
		t.Errorf("Activity defs: %s", got)
	}
	if r.NumFiles != 3 { // Activity.java, ActivityThread.java, other.go
		t.Errorf("Activity numFiles = %d", r.NumFiles)
	}
}

func TestIsDef(t *testing.T) {
	tests := []struct {
		lang, sym, line string
		want            bool
	}{
		{"Go", "Foo", "func Foo(x int) {", true},
		{"Go", "Foo", "func (r *T) Foo() error {", true},
		{"Go", "Foo", "type Foo struct {", true},
		{"Go", "Foo", "\tFoo string `json:\"foo\"`", true},
		{"Go", "Foo", "\tFoo = 3", true},
		{"Go", "Foo", "\tx := Foo(3)", false},
		{"Go", "Foo", "// Foo does things", false},
		{"C", "foo", "struct foo {", true},
		{"C", "foo", "struct foo *p = NULL;", false},
		{"C", "foo", "static int foo(void) {", true},
		{"C", "foo", "int foo(int);", true},
		{"C", "foo", "    return foo(x);", false},
		{"C", "foo", "    x = foo(y);", false},
		{"C", "FOO", "#define FOO 1", true},
		{"C", "foo_t", "} foo_t;", true},
		{"C", "foo_t", "typedef unsigned int foo_t;", true},
		{"C++", "bar", "void Foo::bar() const {", true},
		{"C++", "Foo", "Foo::Foo(int x) : x_(x) {}", true},
		{"C++", "Foo", "    explicit Foo(int x);", true},
		{"C++", "Foo", "    Foo(x);", false},
		{"C++", "Foo", "enum class Foo : uint8_t {", true},
		{"C++", "mCount", "    int mCount = 0;", true},
		{"C++", "mCount", "    mCount = 0;", false},
		{"C++", "Rect", "bool Rect::intersect(const Rect& with) const {", false},
		{"C++", "RED", "    RED = 1,", true},
		{"C++", "RED", "    RED", true},
		{"JavaScript", "x", "const x = 1;", true},
		{"Go", "X", "const X = 1", true},
		{"Protocol Buffer", "RED", "  RED = 1;", true},
		{"Java", "onCreate", "    @Override protected void onCreate(Bundle b) {", true},
		{"Java", "onCreate", "        super.onCreate(b);", false},
		{"Java", "RED", "    RED(0xff0000),", true},
		{"Java", "TAG", "    private static final String TAG = \"x\";", true},
		{"Java", "List", "    List<String> names = new ArrayList<>();", false},
		{"Kotlin", "foo", "fun String.foo(): Int = 1", true},
		{"Kotlin", "Foo", "data class Foo(val x: Int)", true},
		{"Python", "main", "def main():", true},
		{"Python", "MAX", "MAX = 10", true},
		{"Python", "MAX", "if x == MAX:", false},
		{"Python", "MAX", "# MAX = 10", false},
		{"Rust", "foo", "pub fn foo<T>(x: T) -> T {", true},
		{"Rust", "Foo", "pub struct Foo {", true},
		{"TypeScript", "render", "export function render(x: number) {", true},
		{"TypeScript", "fetch", "export const fetch = async (u: string) => {", true},
		{"TypeScript", "count", "  count: number;", true},
		{"Blueprint", "libui", `    name: "libui",`, true},
		{"Blueprint", "libui", `    shared_libs: ["libui"],`, false},
		{"XML", "app_name", `<string name="app_name">App</string>`, true},
		{"Bash", "build", "build() {", true},
		{"Makefile", "all", "all: build", true},
		{"Protocol Buffer", "Req", "message Req {", true},
		{"Protocol Buffer", "id", "  int32 id = 1;", true},
		{"SQL", "users", "CREATE TABLE IF NOT EXISTS users (", true},
	}
	for _, tt := range tests {
		if got := defsFor(tt.sym).isDef(tt.lang, tt.line); got != tt.want {
			t.Errorf("isDef(%s, %s, %q) = %v, want %v", tt.lang, tt.sym, tt.line, got, tt.want)
		}
	}
}

func TestParseQuery(t *testing.T) {
	q, err := ParseQuery(`foo "a b" -bar file:\.go$ -f:test lang:java repo:x case:yes`)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Terms) != 2 || q.Terms[1].Text != "a b" || !q.Terms[1].Literal || q.Terms[0].FoldCase {
		t.Errorf("terms: %+v %+v", q.Terms[0], q.Terms[1])
	}
	if len(q.NotTerms) != 1 || len(q.Files) != 1 || len(q.NotFiles) != 1 || len(q.Langs) != 1 || len(q.Repos) != 1 {
		t.Errorf("parsed: %+v", q)
	}
	if _, err := ParseQuery(`lang:nosuchlang`); err == nil {
		t.Error("unknown language accepted")
	}
	if _, err := ParseQuery(`"unterminated`); err == nil {
		t.Error("unterminated quote accepted")
	}
	q, _ = ParseQuery(`foo(`) // invalid regexp is searched literally
	if !q.Terms[0].Literal {
		t.Error("foo( not literal")
	}
	q, _ = ParseQuery(`\bFoo\w+`)
	if q.Terms[0].FoldCase {
		t.Error("upper-case term should be case-sensitive")
	}
	q, _ = ParseQuery(`\w+\S`)
	if !q.Terms[0].FoldCase {
		t.Error(`escapes like \S should not make a term case-sensitive`)
	}
}

func TestRepoAbs(t *testing.T) {
	r := &Repo{Root: t.TempDir()}
	for _, bad := range []string{"../x", "a/../../x", "/../x"} {
		if _, err := r.Abs(bad); err == nil {
			t.Errorf("Abs(%q) succeeded", bad)
		}
	}
	os.Symlink("/", filepath.Join(r.Root, "escape"))
	if _, err := r.Abs("escape/etc"); err == nil {
		t.Error("symlink escape allowed")
	}
	if p, err := r.Abs("a/b"); err != nil || p != filepath.Join(r.Root, "a/b") {
		t.Errorf("Abs(a/b) = %q, %v", p, err)
	}
}
