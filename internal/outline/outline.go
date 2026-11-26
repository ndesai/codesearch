// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package outline extracts an outline (types, functions, methods,
// headings) from source files using per-language patterns. It needs no
// parser or build, so it works on any file, at the cost of occasionally
// missing or misclassifying a declaration.
package outline

import (
	"regexp"
	"strings"
)

// A Symbol is one entry in a file's outline.
type Symbol struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"` // class, struct, interface, enum, func, method, type, module, heading, ...
	Line  int    `json:"line"`
	Depth int    `json:"depth"` // nesting level, from indentation
}

const maxSymbols = 5000

type rule struct {
	need []string // the line must contain one of these to be worth matching
	re   *regexp.Regexp
	kind string // "" means use the captured keyword
	name int    // capture group with the name
	kw   int    // capture group with the keyword (if kind == "")
	recv int    // capture group with a receiver/qualifier, or 0
}

var mods = `(?:(?:export|default|public|private|protected|internal|abstract|final|sealed|static|data|open|inner|inline|value|enum|annotation|partial|readonly|declare|async|unsafe|extern|virtual|override|suspend|tailrec|operator|infix|external|actual|expect|const|pub(?:\([^)]*\))?)\s+)*`

func r(pat, kind string, name, kw, recv int, need ...string) rule {
	return rule{need, regexp.MustCompile(pat), kind, name, kw, recv}
}

// worth reports whether line could match the rule, a cheap test that
// avoids running regular expressions on most lines.
func (ru *rule) worth(line string) bool {
	if len(ru.need) == 0 {
		return true
	}
	for _, n := range ru.need {
		if strings.Contains(line, n) {
			return true
		}
	}
	return false
}

var (
	// class Foo, struct Foo, interface Foo, enum class Foo, message Foo, ...
	typeRule = r(`^\s*`+mods+`(class|struct|interface|enum|trait|protocol|object|record|union|message|service|module|namespace|impl|extension|actor|defmodule|mixin|typealias)\s+(?:class\s+|struct\s+)?(?:\[\[[^\]]*\]\]\s*|[A-Z_]+_EXPORT\s+)?([A-Za-z_$][\w$.:]*)`, "", 2, 1, 0, "class", "struct", "interface", "enum", "trait", "protocol", "object", "record", "union", "message", "service", "module", "namespace", "impl", "extension", "actor", "mixin", "typealias")
	// def foo, fn foo, fun Foo.bar, function foo, func foo, rpc Foo, sub foo
	funcRule = r(`^\s*`+mods+`(?:def|defp|fn|fun|func|function\*?|sub|proc|rpc|macro|defmacro)\s+(?:<[^>]*>\s*)?(?:([\w$]+)[.:])?([A-Za-z_$][\w$]*[!?]?)`, "func", 2, 0, 1, "def", "fn ", "fun ", "func", "sub ", "proc ", "rpc ", "macro")
	// Go: func (r *Recv) Name(  /  type Name ...
	goFunc = r(`^func\s+(?:\(\s*\w*\s*\*?\s*(\w+)(?:\[[^\]]*\])?\s*\)\s*)?(\w+)`, "func", 2, 0, 1, "func")
	goType = r(`^type\s+(\w+)\s+(struct|interface)?`, "", 1, 2, 0, "type")
	// C-family function or method definition/declaration: type name(...)
	cFunc = r(`^\s*(?:template\s*<[^>]*>\s*)?(?:[\w:<>,\*&\[\]~]+\s+)+[\*&]*((?:[\w~]+(?:<[^<>]*>)?::)*)([~\w]+)\s*\([^;]*$`, "func", 2, 0, 1, "(")
	cDecl = r(`^\s*(?:[\w:<>,\*&\[\]~]+\s+)+[\*&]*((?:[\w~]+::)*)([~\w]+)\s*\([^;{]*\)\s*(?:const\s*)?(?:override\s*|final\s*|noexcept\s*|=\s*0\s*|=\s*default\s*|=\s*delete\s*)*;`, "func", 2, 0, 1, "(")
	cCtor = r(`^\s*((?:\w+::)+)(~?\w+)\s*\(`, "func", 2, 0, 1, "::")
	// JS/TS class method:   name(args) {   /   async name(args): T {
	jsMethod = r(`^\s+`+mods+`(?:get\s+|set\s+|\*\s*)?([A-Za-z_$][\w$]*)\s*(?:<[^>]*>)?\s*\([^)]*\)\s*(?::\s*[^{=]+)?\{\s*$`, "method", 1, 0, 0, "{")
	// const foo = (...) => / function
	jsArrow  = r(`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*(?::[^=]+)?=\s*(?:async\s*)?(?:function\b|\([^)]*\)\s*(?::[^=]+)?=>|[A-Za-z_$][\w$]*\s*=>)`, "func", 1, 0, 0, "=>", "function")
	tsType   = r(`^\s*(?:export\s+)?(?:declare\s+)?type\s+([A-Za-z_$][\w$]*)\s*(?:<[^>]*>)?\s*=`, "type", 1, 0, 0, "type")
	shFunc   = r(`^\s*(?:function\s+)?([A-Za-z_][\w:.-]*)\s*\(\s*\)\s*\{?\s*$`, "func", 1, 0, 0, "()")
	mdHead   = r(`^(#{1,6})\s+(.+?)\s*#*\s*$`, "heading", 2, 1, 0, "#")
	bpModule = r(`^([a-z_][\w]*)\s*\{\s*$`, "module", 1, 0, 0, "{")
	bpName   = r(`^\s+name\s*:\s*"([^"]+)"`, "name", 1, 0, 0, "name")
	makeRule = r(`^([A-Za-z_][\w./%-]*)\s*:(?:[^=]|$)`, "target", 1, 0, 0, ":")
	sqlRule  = r(`(?i)^\s*create\s+(?:or\s+replace\s+)?(?:temp\w*\s+)?(table|view|index|function|procedure|trigger|type)\s+(?:if\s+not\s+exists\s+)?([\w."]+)`, "", 2, 1, 0)
)

// statements that look like C function declarations
var notFunc = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "return": true, "catch": true, "else": true,
	"sizeof": true, "do": true, "case": true, "new": true, "delete": true, "throw": true, "using": true,
	"typedef": true, "goto": true, "assert": true, "elif": true, "foreach": true, "with": true,
	"synchronized": true, "defined": true, "decltype": true, "static_assert": true, "alignof": true,
}

var stmtStart = regexp.MustCompile(`^\s*(?:return|else|throw|delete|new|case|goto|co_return|co_yield|co_await|await|yield)\b`)

var families = map[string][]rule{
	"Go":                     {goFunc, goType},
	"Python":                 {funcRule, typeRule},
	"Starlark":               {funcRule},
	"Ruby":                   {funcRule, typeRule},
	"Rust":                   {funcRule, typeRule},
	"Kotlin":                 {funcRule, typeRule},
	"Swift":                  {funcRule, typeRule},
	"Scala":                  {funcRule, typeRule},
	"Elixir":                 {funcRule, typeRule},
	"PHP":                    {funcRule, typeRule},
	"Lua":                    {funcRule},
	"Perl":                   {funcRule},
	"JavaScript":             {funcRule, typeRule, jsArrow, jsMethod},
	"TypeScript":             {funcRule, typeRule, tsType, jsArrow, jsMethod},
	"TSX":                    {funcRule, typeRule, tsType, jsArrow, jsMethod},
	"vue":                    {funcRule, typeRule, jsArrow, jsMethod},
	"Svelte":                 {funcRule, typeRule, jsArrow, jsMethod},
	"Protocol Buffer":        {typeRule, funcRule},
	"Thrift":                 {typeRule, funcRule},
	"Bash":                   {shFunc},
	"Zsh":                    {shFunc},
	"Markdown":               {mdHead},
	"markdown":               {mdHead}, // Chroma's name for .md files
	"Blueprint":              {bpModule, bpName},
	"Makefile":               {makeRule},
	"SQL":                    {sqlRule},
	"MySQL":                  {sqlRule},
	"PostgreSQL SQL dialect": {sqlRule},
}

var cLike = map[string]bool{
	"C": true, "C++": true, "C/C++ Header": true, "Objective-C": true, "Objective-C++": true,
	"Java": true, "C#": true, "Dart": true, "AIDL": true, "HIDL": true, "Groovy": true,
	"GLSL": true, "SkSL": true, "CUDA": true, "HLSL": true, "Arduino": true, "D": true, "Vala": true,
}

// Extract returns the outline of src, a file in the named language
// (as reported by package lang).
func Extract(langName, src string) []Symbol {
	rules, ok := families[langName]
	c := cLike[langName]
	if !ok && !c {
		return nil
	}
	if c {
		rules = []rule{typeRule, cCtor, cFunc}
		if langName == "C/C++ Header" || langName == "AIDL" || langName == "HIDL" || langName == "Java" || langName == "C#" {
			rules = append(rules, cDecl) // declarations without bodies
		}
	}
	hashComment := !strings.EqualFold(langName, "markdown") && !c

	var out []Symbol
	var indents []int // indentation of enclosing entries
	inBlock := false  // inside /* */
	for i, line := range strings.Split(src, "\n") {
		if len(out) >= maxSymbols {
			break
		}
		t := strings.TrimSpace(line)
		if inBlock {
			if strings.Contains(t, "*/") {
				inBlock = false
			}
			continue
		}
		if t == "" || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") ||
			hashComment && strings.HasPrefix(t, "#") || strings.HasPrefix(t, "--") ||
			c && (t[0] == '#' || t[0] == ':' || t[0] == ',') || t[0] == '@' && !strings.Contains(t, " ") {
			continue
		}
		if strings.HasPrefix(t, "/*") {
			inBlock = !strings.Contains(t, "*/")
			continue
		}
		for _, ru := range rules {
			if !ru.worth(line) {
				continue
			}
			m := ru.re.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			name := m[ru.name]
			kind := ru.kind
			if kind == "" {
				kind = strings.ToLower(m[ru.kw])
				if kind == "" {
					kind = "type"
				}
			}
			// Rules without an introducing keyword can match statements.
			if notFunc[name] && (ru.re == jsMethod.re || ru.re == shFunc.re || ru.re == makeRule.re || ru.re == cFunc.re || ru.re == cDecl.re || ru.re == cCtor.re) {
				continue
			}
			if ru.re == cFunc.re || ru.re == cDecl.re || ru.re == cCtor.re {
				if stmtStart.MatchString(line) || strings.Contains(line, "=") && !strings.Contains(line, "operator") && !strings.Contains(line, "= 0") && !strings.Contains(line, "= default") && !strings.Contains(line, "= delete") {
					continue
				}
			}
			if ru.re == typeRule.re && strings.HasSuffix(t, ";") && cLike[langName] {
				continue // forward declaration
			}
			if ru.re == mdHead.re {
				kind = "h" + string(rune('0'+len(m[1])))
			}
			if ru.recv > 0 && m[ru.recv] != "" {
				name = strings.TrimSuffix(m[ru.recv], "::") + "." + name
				if kind == "func" {
					kind = "method"
				}
			}

			indent := indentOf(line)
			if ru.re == mdHead.re {
				indent = len(m[1]) // heading level
			}
			for len(indents) > 0 && indents[len(indents)-1] >= indent {
				indents = indents[:len(indents)-1]
			}
			depth := len(indents)
			indents = append(indents, indent)
			if kind == "func" && depth > 0 {
				kind = "method"
			}
			out = append(out, Symbol{Name: name, Kind: kind, Line: i + 1, Depth: depth})
			break
		}
	}
	return out
}

func indentOf(line string) int {
	n := 0
	for _, c := range line {
		switch c {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}
