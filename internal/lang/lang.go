// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package lang maps file names to programming languages, using the
// Chroma lexer registry (several hundred languages) plus overrides for
// file types common in large trees like AOSP.
package lang

import (
	"path"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// A Lang is a detected language.
type Lang struct {
	Name  string       // display name, e.g. "C++"
	Lexer chroma.Lexer // lexer for highlighting (may be nil)
}

// overrides maps lowercase base names or extensions (with dot)
// to [display name, chroma lexer name].
var overrides = map[string][2]string{
	".bp":            {"Blueprint", "Go"},
	"android.bp":     {"Blueprint", "Go"},
	".aidl":          {"AIDL", "Java"},
	".hal":           {"HIDL", "Java"},
	".rc":            {"Android Init", "Bash"},
	".mm":            {"Objective-C++", "Objective-C"},
	".m":             {"Objective-C", "Objective-C"},
	".h":             {"C/C++ Header", "C++"},
	".hh":            {"C/C++ Header", "C++"},
	".inc":           {"C/C++ Header", "C++"},
	".ld":            {"Linker Script", "C"},
	".lds":           {"Linker Script", "C"},
	".bzl":           {"Starlark", "Python"},
	"build":          {"Starlark", "Python"},
	"build.bazel":    {"Starlark", "Python"},
	"workspace":      {"Starlark", "Python"},
	"module.bazel":   {"Starlark", "Python"},
	"go.mod":         {"Go Module", "Go"},
	"go.sum":         {"Text", "plaintext"},
	".sql":           {"SQL", "SQL"},
	".gn":            {"GN", "Python"},
	".gni":           {"GN", "Python"},
	".s":             {"Assembly", "GAS"},
	".asm":           {"Assembly", "NASM"},
	".te":            {"SELinux Policy", "Bash"},
	".fs":            {"F#", "FSharp"},
	".sksl":          {"SkSL", "GLSL"},
	".vts":           {"Text", "plaintext"},
	".txt":           {"Text", "plaintext"},
	"owners":         {"Text", "plaintext"},
	"license":        {"Text", "plaintext"},
	"notice":         {"Text", "plaintext"},
	"cmakelists.txt": {"CMake", "CMake"},
}

var (
	mu    sync.Mutex
	cache = map[string]*Lang{}
)

// Detect returns the language for the given file name. It never returns nil.
func Detect(name string) *Lang {
	base := path.Base(strings.ReplaceAll(name, `\`, "/"))
	lower := strings.ToLower(base)
	ext := path.Ext(lower)
	key := ext
	if ext == "" || overrides[lower] != [2]string{} || ext == ".txt" || ext == ".mod" || ext == ".sum" || ext == ".bazel" {
		key = "\x00" + lower // cache by full name
	}
	mu.Lock()
	l, ok := cache[key]
	mu.Unlock()
	if ok {
		return l
	}
	l = detect(base, lower, ext)
	mu.Lock()
	cache[key] = l
	mu.Unlock()
	return l
}

func detect(base, lower, ext string) *Lang {
	if o, ok := overrides[lower]; ok {
		return &Lang{o[0], lexers.Get(o[1])}
	}
	if o, ok := overrides[ext]; ok && ext != "" {
		return &Lang{o[0], lexers.Get(o[1])}
	}
	lx := lexers.Match(base)
	if lx == nil && ext != "" {
		lx = lexers.Match("x" + ext)
	}
	if lx == nil {
		return &Lang{"Text", nil}
	}
	name := lx.Config().Name
	if name == "plaintext" {
		name = "Text"
	}
	return &Lang{name, lx}
}

// A Filter matches file names belonging to a language named in a query.
type Filter struct {
	names map[string]bool // display names
	globs []string        // lowercase filename globs
}

// aliases maps common query spellings to display names or lexer names.
var aliases = map[string][]string{
	"c":           {"C", "C/C++ Header"},
	"cpp":         {"C++", "C/C++ Header"},
	"c++":         {"C++", "C/C++ Header"},
	"cxx":         {"C++", "C/C++ Header"},
	"cc":          {"C++", "C/C++ Header"},
	"h":           {"C/C++ Header"},
	"header":      {"C/C++ Header"},
	"objc":        {"Objective-C", "Objective-C++", "C/C++ Header"},
	"objective-c": {"Objective-C", "C/C++ Header"},
	"bp":          {"Blueprint"},
	"blueprint":   {"Blueprint"},
	"aidl":        {"AIDL"},
	"hidl":        {"HIDL"},
	"bazel":       {"Starlark"},
	"starlark":    {"Starlark"},
	"bzl":         {"Starlark"},
	"gn":          {"GN"},
	"asm":         {"Assembly", "ArmAsm", "GAS", "NASM"},
	"assembly":    {"Assembly", "ArmAsm", "GAS", "NASM"},
	"make":        {"Makefile"},
	"mk":          {"Makefile"},
	"sh":          {"Bash"},
	"shell":       {"Bash"},
	"text":        {"Text"},
	"txt":         {"Text"},
	"sql":         {"SQL", "MySQL", "PostgreSQL SQL dialect", "PL/pgSQL", "Transact-SQL"},
	"init":        {"Android Init"},
	"sepolicy":    {"SELinux Policy"},
	"ts":          {"TypeScript"},
	"js":          {"JavaScript"},
	"proto":       {"Protocol Buffer"},
	"protobuf":    {"Protocol Buffer"},
}

// NewFilter returns a filter for the language named q (case-insensitive),
// or nil if q names no known language.
func NewFilter(q string) *Filter {
	q = strings.ToLower(strings.TrimSpace(q))
	f := &Filter{names: map[string]bool{}}
	for _, n := range aliases[q] {
		f.names[n] = true
	}
	if lx := lexers.Get(q); lx != nil && (len(f.names) == 0 || q == "js" || q == "ts") {
		cfg := lx.Config()
		f.names[cfg.Name] = true
		for _, g := range append(append([]string{}, cfg.Filenames...), cfg.AliasFilenames...) {
			f.globs = append(f.globs, strings.ToLower(g))
		}
	}
	// Also accept display names directly ("java", "kotlin", "blueprint").
	for _, o := range overrides {
		if strings.ToLower(o[0]) == q {
			f.names[o[0]] = true
		}
	}
	if len(f.names) == 0 {
		return nil
	}
	return f
}

// Match reports whether the file name belongs to the filter's language.
func (f *Filter) Match(name string) bool {
	if f.names[Detect(name).Name] {
		return true
	}
	base := strings.ToLower(path.Base(name))
	for _, g := range f.globs {
		if ok, _ := path.Match(g, base); ok {
			return true
		}
	}
	return false
}
