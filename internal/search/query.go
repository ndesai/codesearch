// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package search

import (
	"fmt"
	stdregexp "regexp"
	"regexp/syntax"
	"strings"
	"unicode"

	"github.com/google/codesearch/internal/lang"
)

// Query is a parsed search query.
//
// Syntax (modeled on Google's Code Search):
//
//	foo bar         files containing both foo and bar (each a regexp)
//	"foo bar"       the literal string "foo bar"
//	-foo            files not containing foo
//	file:\.go$      path matches regexp (also f:, path:)
//	-file:test      path does not match regexp
//	lang:java       language (also l:, -lang:)
//	repo:aosp       repository name matches regexp (also r:, -repo:)
//	case:yes        case-sensitive (case:no for insensitive; default is
//	                case-insensitive unless the term has an upper-case letter)
//	sym:Foo         definitions of the symbol Foo
//
// A term that is not a valid regular expression is searched literally.
type Query struct {
	Raw      string
	Terms    []*Term // content terms that must all appear in a file
	NotTerms []*Term // content terms that must not appear
	Files    []*stdregexp.Regexp
	NotFiles []*stdregexp.Regexp
	Langs    []*lang.Filter
	NotLangs []*lang.Filter
	Repos    []*stdregexp.Regexp
	NotRepos []*stdregexp.Regexp
	Case     string // "yes", "no" or "" (auto)
	Symbol   string // sym: query
}

// A Term is one content pattern.
type Term struct {
	Text     string // as written
	Pattern  string // regexp syntax, including case flags
	Literal  bool   // Text is matched literally
	FoldCase bool
	Std      *stdregexp.Regexp // for finding match ranges within lines
	syntax   *syntax.Regexp
}

// IsIdent reports whether the term is a plain identifier.
func (t *Term) IsIdent() bool {
	return identRE.MatchString(t.Text)
}

var (
	identRE  = stdregexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)
	escapeRE = stdregexp.MustCompile(`\\[A-Za-z]`)
)

// ParseQuery parses a query string.
func ParseQuery(s string) (*Query, error) {
	q := &Query{Raw: s}
	toks, err := tokenize(s)
	if err != nil {
		return nil, err
	}
	type pending struct {
		text   string
		quoted bool
		neg    bool
	}
	var terms []pending
	for _, t := range toks {
		neg := false
		text := t.text
		if !t.quoted && len(text) > 1 && text[0] == '-' {
			neg = true
			text = text[1:]
		}
		key, val, ok := "", "", false
		if !t.quoted || t.keyed {
			if i := strings.IndexByte(text, ':'); i > 0 && isKey(text[:i]) {
				key, val, ok = strings.ToLower(text[:i]), text[i+1:], true
			}
		}
		if !ok {
			terms = append(terms, pending{text, t.quoted, neg})
			continue
		}
		if val == "" {
			return nil, fmt.Errorf("missing value for %s:", key)
		}
		switch key {
		case "file", "f", "path":
			re, err := stdregexp.Compile(smartCase(val, "") + val)
			if err != nil {
				re = stdregexp.MustCompile("(?i)" + stdregexp.QuoteMeta(val))
			}
			if neg {
				q.NotFiles = append(q.NotFiles, re)
			} else {
				q.Files = append(q.Files, re)
			}
		case "lang", "l", "language":
			f := lang.NewFilter(val)
			if f == nil {
				return nil, fmt.Errorf("unknown language %q", val)
			}
			if neg {
				q.NotLangs = append(q.NotLangs, f)
			} else {
				q.Langs = append(q.Langs, f)
			}
		case "repo", "r":
			re, err := stdregexp.Compile("(?i)" + val)
			if err != nil {
				re = stdregexp.MustCompile("(?i)" + stdregexp.QuoteMeta(val))
			}
			if neg {
				q.NotRepos = append(q.NotRepos, re)
			} else {
				q.Repos = append(q.Repos, re)
			}
		case "case":
			switch strings.ToLower(val) {
			case "yes", "y", "true", "1", "sensitive":
				q.Case = "yes"
			case "no", "n", "false", "0", "insensitive":
				q.Case = "no"
			case "auto":
				q.Case = ""
			default:
				return nil, fmt.Errorf("case: must be yes, no or auto")
			}
		case "sym", "symbol", "def":
			if !identRE.MatchString(val) {
				return nil, fmt.Errorf("sym: requires an identifier")
			}
			q.Symbol = val
		case "content", "c":
			terms = append(terms, pending{val, t.quoted, neg})
		}
	}
	for _, p := range terms {
		term, err := newTerm(p.text, p.quoted, q.Case)
		if err != nil {
			return nil, err
		}
		if p.neg {
			q.NotTerms = append(q.NotTerms, term)
		} else {
			q.Terms = append(q.Terms, term)
		}
	}
	if q.Symbol != "" {
		term, _ := newTerm(`\b`+q.Symbol+`\b`, false, "yes")
		term.Text = q.Symbol
		q.Terms = append(q.Terms, term)
	}
	return q, nil
}

// smartCase returns the regexp flag prefix for text: case-insensitive
// unless the text contains an upper-case letter, or as forced by mode.
func smartCase(text, mode string) string {
	switch mode {
	case "yes":
		return ""
	case "no":
		return "(?i)"
	}
	for _, r := range text {
		if unicode.IsUpper(r) {
			return ""
		}
	}
	return "(?i)"
}

func newTerm(text string, quoted bool, mode string) (*Term, error) {
	t := &Term{Text: text}
	pat := text
	if quoted {
		pat = stdregexp.QuoteMeta(text)
		t.Literal = true
	} else if _, err := syntax.Parse(text, syntax.Perl); err != nil {
		pat = stdregexp.QuoteMeta(text)
		t.Literal = true
	}
	caseText := text
	if !t.Literal {
		// Escapes such as \S or \W don't count as upper-case letters.
		caseText = escapeRE.ReplaceAllString(text, "")
	}
	flags := smartCase(caseText, mode)
	t.FoldCase = flags != ""
	t.Pattern = flags + pat
	re, err := syntax.Parse(t.Pattern, syntax.Perl)
	if err != nil {
		return nil, fmt.Errorf("bad pattern %q: %v", text, err)
	}
	t.syntax = re
	t.Std, err = stdregexp.Compile("(?m)" + t.Pattern)
	if err != nil {
		return nil, fmt.Errorf("bad pattern %q: %v", text, err)
	}
	return t, nil
}

func isKey(k string) bool {
	switch strings.ToLower(k) {
	case "file", "f", "path", "lang", "l", "language", "repo", "r", "case", "sym", "symbol", "def", "content", "c":
		return true
	}
	return false
}

type token struct {
	text   string
	quoted bool
	keyed  bool // key:"quoted value"
}

// tokenize splits s on spaces, honoring double quotes and backslash
// escapes inside quotes. A token like file:"a b" is a single keyed token.
func tokenize(s string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		var b strings.Builder
		quoted, keyed := false, false
		for i < len(s) && s[i] != ' ' && s[i] != '\t' {
			if s[i] == '"' {
				if b.Len() > 0 {
					keyed = strings.HasSuffix(b.String(), ":")
				}
				quoted = true
				i++
				for i < len(s) && s[i] != '"' {
					if s[i] == '\\' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\') {
						i++
					}
					b.WriteByte(s[i])
					i++
				}
				if i >= len(s) {
					return nil, fmt.Errorf("unterminated quote")
				}
				i++ // closing quote
				continue
			}
			b.WriteByte(s[i])
			i++
		}
		toks = append(toks, token{b.String(), quoted, keyed})
	}
	return toks, nil
}

// String returns a canonical form of the query, for caching.
func (q *Query) String() string { return q.Raw }
