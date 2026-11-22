// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package search

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndesai/codesearch/internal/config"
	"github.com/ndesai/codesearch/internal/indexer"
	"github.com/ndesai/codesearch/internal/lang"
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
	"native/libs/gfx/Canvas.h": `#pragma once
class Rect;
class Canvas {
  friend class Rect;
  Rect* bounds;
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
	t.Setenv(config.IndexDirEnv, "") // don't let the environment redirect test indexes
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
		{"ONCREATE file:thread", "aosp:core/java/android/app/ActivityThread.java"},
		{"ONCREATE case:auto", ""},
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

func TestFacets(t *testing.T) {
	e := newTestEngine(t)
	r := search(t, e, "package OR cc_library")
	if r.Facets == nil || len(r.Facets.Repos) != 2 || r.Facets.Repos[0].Filter != `repo:^aosp$` {
		t.Fatalf("repo facets: %+v", r.Facets)
	}
	// Every facet filter must parse and narrow the search to its count.
	for _, list := range [][]Facet{r.Facets.Repos, r.Facets.Langs, r.Facets.Dirs} {
		for _, f := range list {
			r2 := search(t, e, "(package OR cc_library) "+f.Filter)
			if r2.NumFiles != f.Count {
				t.Errorf("facet %s (%s): %d files, want %d", f.Label, f.Filter, r2.NumFiles, f.Count)
			}
		}
	}
	// A lang: facet for every language we detect must round-trip.
	for _, name := range []string{"x.h", "x.java", "x.go", "Android.bp", "x.py", "x.proto", "x.ts", "x.txt", "x.kt", "x.rs", "BUILD"} {
		l := lang.Detect(name).Name
		q, err := ParseQuery(langFilter(l))
		if err != nil || !q.PathMatch(name) {
			t.Errorf("lang facet for %s (%s): %v", name, langFilter(l), err)
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

func TestSymbolTables(t *testing.T) {
	e := newTestEngine(t)
	snaps := e.acquire(&Query{})
	defer func() {
		for _, s := range snaps {
			s.release()
		}
	}()
	var got []string
	for _, d := range symbolDefs(snaps, "intersect") {
		got = append(got, fmt.Sprintf("%s:%d:%s", d.Path, d.Line, d.Kind))
	}
	slices.Sort(got)
	if strings.Join(got, " ") != "native/libs/ui/Rect.cpp:5:method native/libs/ui/Rect.h:6:method" {
		t.Errorf("symbol table defs of intersect: %v", got)
	}
	// Forward declarations are not definitions, in the table or the heuristics.
	got = nil
	for _, d := range symbolDefs(snaps, "Rect") {
		got = append(got, fmt.Sprintf("%s:%d", d.Path, d.Line))
	}
	if strings.Join(got, " ") != "native/libs/ui/Rect.h:4" {
		t.Errorf("symbol table defs of Rect: %v", got)
	}
	r, _ := e.Xref(context.Background(), "Rect", "aosp", "native/libs/gfx/Canvas.h")
	for _, d := range r.Defs {
		if d.Path == "native/libs/gfx/Canvas.h" {
			t.Errorf("forward declaration reported as a definition: %s:%d %s", d.Path, d.Line, d.Text)
		}
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
		{"C++", "Foo", "class Foo;", false},
		{"C++", "Foo", "  friend class Foo;", false},
		{"C++", "Foo", "class CONTENT_EXPORT Foo;", false},
		{"C/C++ Header", "Foo", "struct Foo;", false},
		{"Rust", "Foo", "pub struct Foo;", true},
		{"C++", "Base", "    : Base(site_instance,", false},
		{"C++", "y_", "      , y_(0) {}", false},
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
	if len(q.AllTerms) != 3 || q.IsPositive(2) || len(q.Files) != 1 || len(q.Repos) != 1 || q.Simple {
		t.Errorf("parsed: %+v", q)
	}
	for _, bad := range []string{`lang:nosuchlang`, `"unterminated`, `a OR`, `OR b`, `( OR )`, `NOT`} {
		if _, err := ParseQuery(bad); err == nil {
			t.Errorf("ParseQuery(%q) succeeded", bad)
		}
	}
	q, _ = ParseQuery(`foo(`) // invalid regexp is searched literally
	if !q.Terms[0].Literal {
		t.Error("foo( not literal")
	}
	// Case-insensitive by default, even with upper-case letters.
	q, _ = ParseQuery(`\bFoo\w+ file:Rect`)
	if !q.Terms[0].FoldCase || !q.PathMatch("native/rect.h") {
		t.Error("default should be case-insensitive")
	}
	// case:auto: case-sensitive only for terms with upper-case letters.
	q, _ = ParseQuery(`\bFoo\w+ bar case:auto`)
	if q.Terms[0].FoldCase || !q.Terms[1].FoldCase {
		t.Error("case:auto")
	}
	q, _ = ParseQuery(`\w+\S case:auto`)
	if !q.Terms[0].FoldCase {
		t.Error(`escapes like \S should not make a term case-sensitive`)
	}
	q, _ = ParseQuery(`Foo case:yes file:Rect`)
	if q.Terms[0].FoldCase || q.PathMatch("native/rect.h") {
		t.Error("case:yes should be case-sensitive")
	}
	// Parentheses without operators are part of the search text.
	q, _ = ParseQuery(`func (b *Reader) Read`)
	if len(q.Terms) != 4 || q.Terms[1].Text != "(b" || q.Terms[2].Text != "*Reader)" || !q.Simple {
		t.Errorf("func (b *Reader): %d terms %q %q", len(q.Terms), q.Terms[1].Text, q.Terms[2].Text)
	}
	q, _ = ParseQuery(`(foo OR bar) baz`)
	if len(q.Terms) != 3 || q.Expr.Op != OpAnd || q.Expr.Kids[0].Op != OpOr {
		t.Errorf("(foo OR bar) baz: %+v", q.Expr)
	}
	q, _ = ParseQuery(`(a OR b`) // unmatched parentheses are search text
	if q.Terms[0].Text != "(a" || q.Expr.Op != OpOr {
		t.Errorf("(a OR b: %+v", q.Terms)
	}
	q, _ = ParseQuery(`( foo )`)
	if len(q.Terms) != 1 || q.Terms[0].Text != "foo" {
		t.Errorf("( foo ): %+v", q.Terms)
	}
	q, _ = ParseQuery(`"OR" or`) // quoted and lower-case "or" are search terms
	if len(q.Terms) != 2 || !q.Simple {
		t.Errorf(`"OR" or: %+v`, q.Terms)
	}
}

func TestFileGlobs(t *testing.T) {
	tests := []struct {
		glob string
		yes  []string
		no   []string
	}{
		{"*.java", []string{"a/B.java", "B.java"}, []string{"a/B.javax", "a/java/x.go"}},
		{"src/**/*.kt", []string{"src/a.kt", "x/src/a/b/c.kt"}, []string{"src2/a.kt", "src/a.kts"}},
		{"/tools/*", []string{"tools/build.go"}, []string{"x/tools/build.go", "tools/test/a.go"}},
		{"/tools/**", []string{"tools/test/a.go"}, []string{"x/tools/a.go"}},
		{"test/", []string{"tools/test/a.go"}, []string{"tools/testing/a.go"}},
		{"Rect.?pp", []string{"native/Rect.cpp"}, []string{"native/Rect.h"}},
	}
	for _, tt := range tests {
		// Globs only apply when the value has a "*"; force it for "?" cases.
		q, err := ParseQuery("file:" + tt.glob)
		if !isGlob(tt.glob) {
			q, err = ParseQuery("file:" + strings.ReplaceAll(globToRegexp(tt.glob), `\`, `\`))
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range tt.yes {
			if !q.PathMatch(p) {
				t.Errorf("file:%s should match %s", tt.glob, p)
			}
		}
		for _, p := range tt.no {
			if q.PathMatch(p) {
				t.Errorf("file:%s should not match %s", tt.glob, p)
			}
		}
	}
	// Regexps stay regexps.
	for _, re := range []string{`\.java$`, `.*_test\.go`, `^tools/`, `(a|b)*`} {
		if isGlob(re) {
			t.Errorf("%q treated as a glob", re)
		}
	}
}

func TestBooleanSearch(t *testing.T) {
	e := newTestEngine(t)
	tests := []struct {
		query string
		want  []string
	}{
		{"onCreate OR NewModule", []string{"Activity.java", "ActivityThread.java", "build.go", "build_test.go"}},
		{"(intersect OR NewModule) -file:test", []string{"Rect.cpp", "Rect.h", "build.go"}},
		{"onCreate NOT mWindow", []string{"ActivityThread.java"}},
		{"NOT (mWindow OR handleLaunch) onCreate", nil},
		{"file:Rect.h OR file:Android.bp", []string{"Android.bp", "Rect.h"}},
		{"(lang:go OR lang:bp) -file:test repo:aosp", []string{"Android.bp", "build.go"}},
		{"NewModule AND file:test", []string{"build_test.go"}},
		{"onCreate (file:Thread OR NOT file:app)", []string{"ActivityThread.java"}},
	}
	for _, tt := range tests {
		r := search(t, e, tt.query)
		var got []string
		for _, f := range r.Files {
			got = append(got, f.Path[strings.LastIndex(f.Path, "/")+1:])
		}
		slices.Sort(got)
		if !slices.Equal(got, tt.want) {
			t.Errorf("%q: got %v, want %v", tt.query, got, tt.want)
		}
	}
	// Lines from both sides of an OR are shown and highlighted.
	r := search(t, e, "mWindow OR handleLaunch")
	for _, f := range r.Files {
		if len(f.Lines) == 0 || len(f.Lines[0].Ranges) == 0 {
			t.Errorf("%s: no highlighted lines", f.Path)
		}
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
