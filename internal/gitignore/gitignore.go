// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package gitignore implements .gitignore pattern matching.
//
// It supports the syntax described in gitignore(5): comments, negation
// with "!", directory-only patterns ending in "/", patterns anchored by a
// slash, and the wildcards *, ?, [...] and **.
package gitignore

import (
	"bufio"
	"bytes"
	"regexp"
	"strings"
)

// A Rule is one pattern from an ignore file.
type Rule struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
}

// A File is the parsed contents of one ignore file, whose patterns are
// relative to the directory Base (slash-separated, "" for the root).
type File struct {
	Base  string
	Rules []Rule
}

// Parse parses ignore file data for the directory base.
func Parse(base string, data []byte) *File {
	f := &File{Base: base}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if r, ok := parseLine(sc.Text()); ok {
			f.Rules = append(f.Rules, r)
		}
	}
	return f
}

func parseLine(line string) (Rule, bool) {
	line = strings.TrimSuffix(line, "\r")
	// Trailing spaces are ignored unless escaped.
	for strings.HasSuffix(line, " ") && !strings.HasSuffix(line, `\ `) {
		line = line[:len(line)-1]
	}
	if line == "" || line[0] == '#' {
		return Rule{}, false
	}
	var r Rule
	if line[0] == '!' {
		r.negate = true
		line = line[1:]
	} else if strings.HasPrefix(line, `\!`) || strings.HasPrefix(line, `\#`) {
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = strings.TrimRight(line, "/")
	}
	if line == "" {
		return Rule{}, false
	}
	// A slash at the beginning or middle anchors the pattern to the
	// ignore file's directory; otherwise it matches at any depth.
	anchored := strings.Contains(line, "/")
	line = strings.TrimPrefix(line, "/")

	var b strings.Builder
	b.WriteString("^")
	if !anchored {
		b.WriteString("(?:.*/)?")
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '*' && strings.HasPrefix(line[i:], "**"):
			// "**/" matches zero or more directories; "/**" matches
			// everything inside; any other ** acts like *.
			atStart := i == 0 || line[i-1] == '/'
			j := i + 2
			switch {
			case atStart && j < len(line) && line[j] == '/':
				b.WriteString("(?:.*/)?")
				i = j
			case atStart && j == len(line):
				b.WriteString(".*")
				i = j - 1
			default:
				b.WriteString("[^/]*")
				i = j - 1
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '[':
			end := strings.IndexByte(line[i+1:], ']')
			if end < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := line[i+1 : i+1+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += end + 1
		case c == '\\' && i+1 < len(line):
			i++
			b.WriteString(regexp.QuoteMeta(string(line[i])))
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return Rule{}, false
	}
	r.re = re
	return r, true
}

// match reports whether the file's rules decide rel (relative to the
// repository root): ignored is the decision and ok whether any rule matched.
func (f *File) match(rel string, isDir bool) (ignored, ok bool) {
	p := rel
	if f.Base != "" {
		if !strings.HasPrefix(rel, f.Base+"/") {
			return false, false
		}
		p = rel[len(f.Base)+1:]
	}
	// The last matching rule wins.
	for i := len(f.Rules) - 1; i >= 0; i-- {
		r := f.Rules[i]
		if r.dirOnly && !isDir {
			continue
		}
		if r.re.MatchString(p) {
			return !r.negate, true
		}
	}
	return false, false
}

// A Stack is the list of ignore files that apply to a directory, from
// the root down. Rules in deeper files take precedence.
type Stack []*File

// Push returns a stack with f added. The receiver is not modified.
func (s Stack) Push(f *File) Stack {
	if f == nil || len(f.Rules) == 0 {
		return s
	}
	out := make(Stack, len(s), len(s)+1)
	copy(out, s)
	return append(out, f)
}

// Ignored reports whether the slash-separated repository-relative path
// rel is ignored. Callers walking a tree should not descend into ignored
// directories, as git does not.
func (s Stack) Ignored(rel string, isDir bool) bool {
	for i := len(s) - 1; i >= 0; i-- {
		if ignored, ok := s[i].match(rel, isDir); ok {
			return ignored
		}
	}
	return false
}
