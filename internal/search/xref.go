// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package search

import (
	"context"
	"fmt"
	"os"
	"path"
	stdregexp "regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ndesai/codesearch/index"
	"github.com/ndesai/codesearch/internal/lang"
	"github.com/ndesai/codesearch/regexp"
)

func errorf(format string, args ...any) error { return fmt.Errorf(format, args...) }

// Cross references are search based: clicking an identifier searches
// the trigram index for the whole word, then classifies each matching
// line as a definition or a reference using per-language heuristics.
// This needs no compiler integration, works for every language, and is
// fast because the trigram index narrows the files to read.

// language families for definition heuristics
var (
	cLike = set("C", "C++", "C/C++ Header", "Objective-C", "Objective-C++", "Java", "C#", "Dart",
		"AIDL", "HIDL", "Groovy", "GLSL", "SkSL", "CUDA", "HLSL", "Arduino", "D", "Vala",
		"Protocol Buffer", "Thrift", "Linker Script")
	javaLike   = set("Java", "Kotlin", "C#", "Dart", "AIDL", "Groovy", "Scala")
	jsLike     = set("JavaScript", "TypeScript", "TSX", "JSX", "vue", "Svelte", "CoffeeScript")
	tsLike     = set("TypeScript", "TSX", "vue", "Svelte")
	assignLike = set("Python", "Starlark", "GN", "Ruby", "Bash", "Makefile", "CMake", "TOML", "INI",
		"Lua", "R", "Julia", "Nim", "Elixir", "Fish", "Zsh", "Properties", "Android Init", "Tcl", "Perl", "PowerShell")
	makeLike  = set("Makefile")
	shellLike = set("Bash", "Zsh", "Fish", "Android Init")
	goLike    = set("Go", "Go Module")
	protoLike = set("Protocol Buffer", "Thrift")
	rustLike  = set("Rust")
)

func set(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

type defPattern struct {
	re       *stdregexp.Regexp
	langs    map[string]bool // nil means all languages
	notLangs map[string]bool // languages the pattern does not apply to
}

// A defMatcher recognizes definition lines for one identifier.
type defMatcher struct {
	pats []defPattern
	fwd  *stdregexp.Regexp // C-family forward declarations, which are not definitions
}

var (
	defCacheMu sync.Mutex
	defCache   = map[string]*defMatcher{}
)

func defsFor(sym string) *defMatcher {
	defCacheMu.Lock()
	defer defCacheMu.Unlock()
	if d, ok := defCache[sym]; ok {
		return d
	}
	if len(defCache) > 1000 {
		clear(defCache)
	}
	d := newDefMatcher(sym)
	defCache[sym] = d
	return d
}

// leading words that mean a line is a statement, not a declaration
const notDeclWords = `return|else|if|while|for|foreach|switch|case|new|delete|throw|throws|goto|sizeof|typeof|await|yield|co_await|co_return|co_yield|in|of|not|and|or|is|as|assert|print|echo|do|using|import|package|from|include|export\s+default\s+[a-z]`

var notDeclRE = stdregexp.MustCompile(`^\s*(?:` + notDeclWords + `)\b`)

func newDefMatcher(sym string) *defMatcher {
	S := stdregexp.QuoteMeta(sym)
	d := &defMatcher{
		fwd: stdregexp.MustCompile(`^\s*(?:template\s*<[^>]*>\s*)?(?:friend\s+)?(?:@class|class|struct|union|enum(?:\s+class)?)\s+(?:[A-Z_]+_EXPORT\s+)?` + S + `\s*;`),
	}
	add := func(pat string, langs map[string]bool) {
		d.pats = append(d.pats, defPattern{re: stdregexp.MustCompile(pat), langs: langs})
	}
	// Keyword-introduced declarations in most languages:
	// class Foo, struct Foo, def foo, func (r *T) Foo, fn foo, fun T.foo, ...
	add(`\b(?:object|record|typedef|namespace|module|type|typealias|def|defp|defmacro|defmodule|func|function|fn|fun|sub|proc|macro|rpc|newtype|mod)\s+(?:mut\s+)?(?:\([^)]*\)\s*)?(?:<[^>]*>\s*)?[\*&]?(?:[\w.]+[.:])?`+S+`\b`, nil)
	// Variable declarations; in C-family languages const is a qualifier
	// ("const Foo& x"), handled by the field pattern below instead.
	d.pats = append(d.pats, defPattern{re: stdregexp.MustCompile(`\b(?:let|var|val|const)\s+(?:mut\s+)?(?:\([^)]*\)\s*)?` + S + `\b`), notLangs: cLike})
	// Type declarations: class Foo {, struct Foo;, enum class Foo : int, message Foo {
	// but not uses like "struct foo *p;".
	add(`\b(?:class|struct|interface|enum|union|trait|protocol|message|service|parcelable|extension|actor|mixin|concept|data\s+class)\s+(?:class\s+|struct\s+)?(?:\[\[[^\]]*\]\]\s*|__attribute__\S*\s*|[A-Z_]+_EXPORT\s+)?`+S+`\s*(?:$|[{:;<(\[=]|//|/\*|#|\b(?:extends|implements|final|sealed|with|where|of|is|derives|override|open)\b)`, nil)
	add(`^\s*#\s*define\s+`+S+`\b`, nil)
	add(`\btypedef\b.*[\s\*\)]`+S+`\s*(?:\[[^\]]*\]\s*)*;`, nil)
	add(`^\s*\}\s*`+S+`\s*;`, nil) // typedef struct { ... } Foo;
	// Build files and resources: name: "libfoo", name = "foo", <string name="foo">, @+id/foo
	add(`\bname\s*[:=]\s*["']`+S+`["']`, nil)
	add(`@\+id/`+S+`\b`, nil)
	add(`(?i)\bcreate\s+(?:or\s+replace\s+)?(?:temp\w*\s+)?(?:table|view|index|function|procedure|trigger|type)\s+(?:if\s+not\s+exists\s+)?[\w.]*\b`+S+`\b`, nil)

	// C-family functions and methods (definitions and declarations),
	// including qualified names and destructors:  void Foo::bar(int x) {
	add(`^\s*(?:@\w+(?:\([^)]*\))?\s+)*(?:[\w\[\]<>,:\*&~.?]+\s+)+[\*&]*(?:\w+(?:<[^>]*>)?::)*~?`+S+`\s*(?:<[^>()]*>)?\s*\(`, cLike)
	// Out-of-line constructors: Foo::Foo(...)
	add(`^\s*(?:\w+(?:<[^>]*>)?::)+~?`+S+`\s*\(`, cLike)
	// In-class constructors: Foo(int x) : ... {   (not calls, which end in ;)
	add(`^\s*(?:explicit\s+|constexpr\s+|inline\s+)*`+S+`\s*\([^;]*\)\s*(?::.*|\{.*|noexcept.*|=\s*(?:default|delete).*)?$`, cLike)
	// Fields and variables: int foo;  static final Foo FOO = ...;  Bar* foo[3];
	add(`^\s*(?:@\w+(?:\([^)]*\))?\s+)*(?:(?:public|private|protected|internal|static|final|const|constexpr|readonly|volatile|transient|mutable|extern|inline|override|abstract|virtual|unsigned|signed|struct|enum|class|union|register|thread_local|optional|required|repeated)\s+)*[\w:<>,.\[\]\?]+[\s\*&]+[\*&]*`+S+`\s*(?:=[^=]|;|\[|\{|:\s*\d|$)`, cLike)
	// Enum constants on their own line:  FOO = 1,  (but not "foo = 1;")
	add(`^\s*`+S+`\s*(?:=\s*[^=;,]+)?\s*,?\s*(?:(?://|/\*).*)?$`, cLike)
	// Proto enum values:  FOO = 1;
	add(`^\s*`+S+`\s*=\s*-?\w+\s*(?:\[[^\]]*\])?\s*;`, protoLike)
	add(`^\s*`+S+`\s*\([^)]*\)\s*[,{]?\s*(?://.*)?$`, javaLike)

	// JavaScript / TypeScript
	add(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?(?:static\s+)?(?:get\s+|set\s+)?(?:(?:public|private|protected|readonly|override)\s+)*\*?`+S+`\s*(?:<[^>]*>)?\s*\([^)]*\)\s*(?::\s*[^={]+)?\{`, jsLike)
	add(`\b`+S+`\s*[:=]\s*(?:async\s+)?(?:function\b|\([^)]*\)\s*(?::\s*[^=]+)?=>|\w+\s*=>)`, jsLike)
	add(`^\s*(?:(?:public|private|protected|readonly|static|declare|abstract|override)\s+)*`+S+`\s*[?!]?\s*:\s*[\w\[\(\{<'"]`, tsLike)

	// Python, Ruby, shell, make, ...: top-of-line assignment.
	add(`^\s*(?:export\s+|readonly\s+|local\s+|declare\s+(?:-\w+\s+)?|self\.)?`+S+`\s*(?::[^=]*)?[:+?]?=(?:[^=]|$)`, assignLike)
	add(`^`+S+`\s*:(?:[^=]|$)`, makeLike) // make target
	add(`^\s*define\s+`+S+`\b`, makeLike)
	add(`^\s*(?:function\s+)?`+S+`\s*\(\s*\)`, shellLike)

	// Go: members of type/const/var groups and struct fields.
	add(`^\s*`+S+`\s+(?:struct|interface)\b`, goLike)
	add(`^\t+`+S+`(?:\s*,\s*\w+)*\s+(?:[\w\*\[\]\.]+\s*)?=`, goLike)
	add(`^\t+`+S+`\s+[\*\[\]\w\.]+(?:\s+`+"`"+`.*)?\s*(?://.*)?$`, goLike)
	add(`^\s*`+S+`\s+[\*\[\]\w\.]+\s*=\s*iota\b`, goLike)

	// Rust: statics and fields.
	add(`\bstatic\s+(?:mut\s+)?`+S+`\b`, rustLike)
	add(`^\s*(?:pub(?:\([^)]*\))?\s+)?`+S+`\s*:\s*[\w&\[\(<]`, rustLike)
	return d
}

// isDef reports whether line (in a file of language langName)
// looks like it defines the matcher's symbol.
func (d *defMatcher) isDef(langName, line string) bool {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "* ") || t == "*" ||
		strings.HasPrefix(t, "--") || strings.HasPrefix(t, "<!--") {
		return false
	}
	if strings.HasPrefix(t, "#") && !defineRE.MatchString(t) {
		return false
	}
	if cLike[langName] && (d.fwd.MatchString(line) || t[0] == ':' || t[0] == ',') {
		// Forward declarations, and constructor initializer lists
		// such as ": Base(x)", are not definitions.
		return false
	}
	stmt := notDeclRE.MatchString(t)
	for _, p := range d.pats {
		if p.langs != nil && !p.langs[langName] || p.notLangs[langName] {
			continue
		}
		if stmt && p.langs != nil {
			// Statements like "return foo(x);" only count for keyword
			// patterns, which list no languages.
			continue
		}
		if p.re.MatchString(line) {
			return true
		}
	}
	return false
}

var defineRE = stdregexp.MustCompile(`^#\s*define\b`)

// XrefResult lists the definitions and references of a symbol.
type XrefResult struct {
	Symbol    string       `json:"symbol"`
	Defs      []*Location  `json:"defs"`
	Refs      []*FileMatch `json:"refs"`
	NumRefs   int          `json:"numRefs"`
	NumFiles  int          `json:"numFiles"`
	Truncated bool         `json:"truncated,omitempty"`
	Elapsed   float64      `json:"elapsed"`
	// Precise reports that the result comes from a SCIP index rather
	// than search; StaleFiles counts files left out because they changed
	// since that index was built.
	Precise    bool `json:"precise,omitempty"`
	StaleFiles int  `json:"staleFiles,omitempty"`
}

// A Location is a single line in a file.
type Location struct {
	Repo   string   `json:"repo"`
	Path   string   `json:"path"`
	Line   int      `json:"line"`
	Text   string   `json:"text"`
	Ranges [][2]int `json:"ranges,omitempty"`
	rank   int
}

// Xref finds definitions and references of sym, ranking results by
// their proximity to the file the user is looking at (repo, path).
func (e *Engine) Xref(ctx context.Context, sym, repo, from string) (*XrefResult, error) {
	if !identRE.MatchString(sym) {
		return nil, fmt.Errorf("not an identifier: %q", sym)
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	term, err := newTerm(`\b`+sym+`\b`, false, "yes")
	if err != nil {
		return nil, err
	}
	term.Text = sym
	q := &Query{Raw: sym, Terms: []*Term{term}}
	fromLang := lang.Detect(from).Name

	snaps := e.acquire(q)
	defer func() {
		for _, s := range snaps {
			s.release()
		}
	}()

	// Collect candidate files from every shard, then process them
	// nearest first so that the limits cut off the least relevant.
	type cand struct {
		repo *Repo
		rel  string
		dist int
	}
	var cands []cand
	re, err := regexp.Compile("(?m)" + term.Pattern)
	if err != nil {
		return nil, err
	}
	iq := index.RegexpQuery(re.Syntax)
	for _, s := range snaps {
		for _, sh := range s.shards {
			names := sh.nameTable()
			func() {
				defer func() { recover() }()
				for _, id := range sh.ix.PostingQuery(iq) {
					if id < names.len() {
						rel := names.name(id)
						cands = append(cands, cand{s.repo, rel, distance(s.repo.Name, rel, repo, from, fromLang)})
					}
				}
			}()
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].dist < cands[j].dist })

	// Definitions from the symbol tables. Files holding them are
	// searched first, so they survive the candidate limit below.
	tableDefs := map[string]map[int]bool{}
	for _, d := range symbolDefs(snaps, sym) {
		key := d.Repo + "\x00" + d.Path
		if tableDefs[key] == nil {
			tableDefs[key] = map[int]bool{}
		}
		tableDefs[key][d.Line] = true
	}
	if len(tableDefs) > 0 {
		sort.SliceStable(cands, func(i, j int) bool {
			return tableDefs[cands[i].repo.Name+"\x00"+cands[i].rel] != nil && tableDefs[cands[j].repo.Name+"\x00"+cands[j].rel] == nil
		})
	}
	const maxFiles = 4000
	res := &XrefResult{Symbol: sym}
	if len(cands) > maxFiles {
		cands = cands[:maxFiles]
		res.Truncated = true
	}

	defs := defsFor(sym)
	results := make([]*FileMatch, len(cands))
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(len(cands), runtime.GOMAXPROCS(0)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			re, _ := regexp.Compile("(?m)" + term.Pattern)
			for {
				i := int(next.Add(1) - 1)
				if i >= len(cands) || ctx.Err() != nil {
					return
				}
				c := cands[i]
				data, err := os.ReadFile(c.repo.Root + string(os.PathSeparator) + filepathFromSlash(c.rel))
				if err != nil {
					continue
				}
				l := lang.Detect(c.rel).Name
				fm := &FileMatch{Repo: c.repo.Name, Path: c.rel, Lang: l}
				fileDefs := tableDefs[c.repo.Name+"\x00"+c.rel]
				lineno, pos := 1, 0
				eachLine(re, data, func(lo, hi int) bool {
					lineno += strings.Count(string(data[pos:lo]), "\n")
					pos = lo
					fm.NumMatches++
					line := data[lo:hi]
					if len(fm.Lines) < 200 {
						lm := LineMatch{Line: lineno}
						lm.Def = fileDefs[lineno] || defs.isDef(l, string(line))
						lm.Text, lm.Ranges = snippet(line, q.Terms)
						fm.Lines = append(fm.Lines, lm)
					}
					return true
				})
				if fm.NumMatches > 0 {
					results[i] = fm
				}
			}
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		res.Truncated = true
	}

	for i, fm := range results {
		if fm == nil {
			continue
		}
		res.NumFiles++
		res.NumRefs += fm.NumMatches
		var refs []LineMatch
		for _, l := range fm.Lines {
			if l.Def {
				res.Defs = append(res.Defs, &Location{Repo: fm.Repo, Path: fm.Path, Line: l.Line, Text: l.Text, Ranges: l.Ranges, rank: cands[i].dist})
			} else {
				refs = append(refs, l)
			}
		}
		fm.Lines = refs
		if len(refs) > 0 {
			fm.Score = -float64(cands[i].dist)
			res.Refs = append(res.Refs, fm)
		}
	}
	// Nearest first. (Candidates were reordered to put files with known
	// definitions first; that must not affect the order of references.)
	slices.SortStableFunc(res.Refs, func(a, b *FileMatch) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		}
		return 0
	})
	slices.SortStableFunc(res.Defs, func(a, b *Location) int { return a.rank - b.rank })
	if len(res.Defs) > 100 {
		res.Defs = res.Defs[:100]
	}
	if len(res.Refs) > 500 {
		res.Refs = res.Refs[:500]
		res.Truncated = true
	}
	res.Elapsed = time.Since(start).Seconds()
	return res, nil
}

// distance ranks how related (repo, rel) is to the viewed file (fromRepo, from).
// Lower is closer.
func distance(repo, rel, fromRepo, from, fromLang string) int {
	if repo == fromRepo && rel == from {
		return 0
	}
	d := 1000
	if repo == fromRepo {
		d = 100
		// Shared leading directories.
		a, b := path.Dir(rel), path.Dir(from)
		common := 0
		for _, x := range strings.Split(a, "/") {
			if strings.HasPrefix(b+"/", x+"/") {
				common++
				b = strings.TrimPrefix(b, x)
				b = strings.TrimPrefix(b, "/")
			} else {
				break
			}
		}
		d -= min(common*10, 90)
		if path.Dir(rel) == path.Dir(from) {
			d = 5
		}
	}
	if fromLang != "" && !sameFamily(lang.Detect(rel).Name, fromLang) {
		d += 500
	}
	return d
}

func sameFamily(a, b string) bool {
	if a == b {
		return true
	}
	for _, fam := range []map[string]bool{cLike, javaLike, jsLike, goLike} {
		if fam[a] && fam[b] {
			return true
		}
	}
	return false
}

func filepathFromSlash(p string) string {
	if os.PathSeparator == '/' {
		return p
	}
	return strings.ReplaceAll(p, "/", string(os.PathSeparator))
}
