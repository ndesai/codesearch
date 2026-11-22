// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package search

import (
	"fmt"
	stdregexp "regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"unicode"

	"github.com/ndesai/codesearch/index"
	"github.com/ndesai/codesearch/internal/lang"
)

// Query is a parsed search query.
//
// Syntax (modeled on Google's Code Search):
//
//	foo bar         files containing both foo and bar (each a regexp)
//	foo OR bar      files containing either (AND is also accepted)
//	-foo, NOT foo   files not containing foo
//	(a OR b) c      grouping
//	"foo bar"       the literal string "foo bar"
//	file:\.go$      path matches regexp (also f:, path:; -file: excludes)
//	lang:java       language (also l:, -lang:)
//	repo:aosp       repository name matches regexp (also r:, -repo:)
//	case:yes        case-sensitive (the default is case-insensitive;
//	                case:auto is case-sensitive only for terms with an
//	                upper-case letter)
//	sym:Foo         definitions of the symbol Foo
//	usage:foo       foo outside comments and string literals (also code:);
//	                comment:foo and string:foo match only inside them
//
// file: and lang: can be combined with OR and NOT like content terms.
// repo:, case: and sym: apply to the whole query.
// Parentheses group only when the group contains an operator (or the
// parenthesis stands alone), so code like "foo(x)" is still searched
// as written. A term that is not a valid regular expression is searched
// literally.
type Query struct {
	Raw string
	// Expr is the boolean expression over content and path terms,
	// or nil if every file matches.
	Expr *Node
	// Terms are the content terms that count toward a match (those not
	// under NOT), used for showing and highlighting matching lines.
	Terms []*Term
	// AllTerms are the content terms in Expr, indexed by Node.Term.
	AllTerms []*Term
	// Files are path regexps that every matching file must match.
	Files    []*stdregexp.Regexp
	Repos    []*stdregexp.Regexp
	NotRepos []*stdregexp.Regexp
	Case     string // "yes", "auto", or "" or "no" (case-insensitive)
	Symbol   string // sym: query
	// Simple reports whether Expr is a conjunction without OR or NOT.
	Simple bool

	positive []bool // positive[i] reports whether AllTerms[i] is in Terms
}

// A Term is one content pattern.
type Term struct {
	Text     string // as written
	Pattern  string // regexp syntax, including case flags
	Literal  bool   // Text is matched literally
	FoldCase bool
	Region   region            // part of the file a match must be in (usage:, comment:, string:)
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

// An Op is a query expression operator.
type Op int

const (
	OpAnd Op = iota
	OpOr
	OpNot
	OpTerm // content term AllTerms[Term]
	OpFile // path matches File
	OpLang // path is in language Lang
)

// A Node is a query expression.
type Node struct {
	Op   Op
	Kids []*Node
	Term int
	File *stdregexp.Regexp
	Lang *lang.Filter
}

// tri is a three-valued truth value, used to evaluate path filters
// before file contents are known.
type tri int8

const (
	tFalse tri = iota
	tTrue
	tUnknown
)

func (n *Node) eval(path string, term func(int) tri) tri {
	switch n.Op {
	case OpAnd:
		v := tTrue
		for _, k := range n.Kids {
			switch k.eval(path, term) {
			case tFalse:
				return tFalse
			case tUnknown:
				v = tUnknown
			}
		}
		return v
	case OpOr:
		v := tFalse
		for _, k := range n.Kids {
			switch k.eval(path, term) {
			case tTrue:
				return tTrue
			case tUnknown:
				v = tUnknown
			}
		}
		return v
	case OpNot:
		switch n.Kids[0].eval(path, term) {
		case tTrue:
			return tFalse
		case tFalse:
			return tTrue
		}
		return tUnknown
	case OpTerm:
		return term(n.Term)
	case OpFile:
		if n.File.MatchString(path) {
			return tTrue
		}
		return tFalse
	case OpLang:
		if n.Lang.Match(path) {
			return tTrue
		}
		return tFalse
	}
	return tUnknown
}

// PathMatch reports whether a file with the given path could match the
// query, considering only the path filters.
func (q *Query) PathMatch(path string) bool {
	if q.Expr == nil {
		return true
	}
	return q.Expr.eval(path, func(int) tri { return tUnknown }) != tFalse
}

// Match reports whether a file matches, given which content terms
// occur in it (has(i) for AllTerms[i]).
func (q *Query) Match(path string, has func(i int) bool) bool {
	if q.Expr == nil {
		return true
	}
	return q.Expr.eval(path, func(i int) tri {
		if has(i) {
			return tTrue
		}
		return tFalse
	}) == tTrue
}

// IsPositive reports whether AllTerms[i] is one of q.Terms.
func (q *Query) IsPositive(i int) bool { return q.positive[i] }

// IndexQuery returns the trigram query for the expression, given the
// syntax trees of AllTerms (as compiled by the caller).
func (q *Query) IndexQuery(terms []*syntax.Regexp) *index.Query {
	if q.Expr == nil {
		return &index.Query{Op: index.QAll}
	}
	return indexQuery(q.Expr, terms)
}

func indexQuery(n *Node, terms []*syntax.Regexp) *index.Query {
	all := &index.Query{Op: index.QAll}
	switch n.Op {
	case OpTerm:
		return index.RegexpQuery(terms[n.Term])
	case OpAnd:
		var subs []*index.Query
		for _, k := range n.Kids {
			if sq := indexQuery(k, terms); sq.Op != index.QAll {
				subs = append(subs, sq)
			}
		}
		switch len(subs) {
		case 0:
			return all
		case 1:
			return subs[0]
		}
		return &index.Query{Op: index.QAnd, Sub: subs}
	case OpOr:
		var subs []*index.Query
		for _, k := range n.Kids {
			sq := indexQuery(k, terms)
			if sq.Op == index.QAll {
				return all
			}
			subs = append(subs, sq)
		}
		return &index.Query{Op: index.QOr, Sub: subs}
	}
	// NOT, file: and lang: can't narrow the trigram search.
	return all
}

// ParseQuery parses a query string.
func ParseQuery(s string) (*Query, error) {
	q := &Query{Raw: s}
	toks, err := tokenize(s)
	if err != nil {
		return nil, err
	}
	// case: affects how every term is compiled, so find it first.
	for _, t := range toks {
		if k, v, ok := strings.Cut(t.text, ":"); ok && !t.quoted && strings.EqualFold(k, "case") {
			switch strings.ToLower(v) {
			case "yes", "y", "true", "1", "sensitive":
				q.Case = "yes"
			case "no", "n", "false", "0", "insensitive":
				q.Case = "no"
			case "auto", "smart":
				q.Case = "auto"
			default:
				return nil, fmt.Errorf("case: must be yes, no or auto")
			}
		}
	}
	p := &parser{q: q, items: groupParens(toks)}
	expr, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.items) {
		return nil, fmt.Errorf("unexpected )")
	}
	if q.Symbol != "" {
		term, _ := newTerm(`\b`+q.Symbol+`\b`, false, "yes")
		term.Text = q.Symbol
		expr = and(expr, q.leaf(term))
	}
	q.Expr = expr
	q.Simple = true
	q.walk(expr, true, true)
	return q, nil
}

func and(a, b *Node) *Node {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case a.Op == OpAnd:
		a.Kids = append(a.Kids, b)
		return a
	}
	return &Node{Op: OpAnd, Kids: []*Node{a, b}}
}

func (q *Query) leaf(t *Term) *Node {
	q.AllTerms = append(q.AllTerms, t)
	return &Node{Op: OpTerm, Term: len(q.AllTerms) - 1}
}

// walk records term polarity, the top-level path filters, and
// whether the expression is a simple conjunction.
func (q *Query) walk(n *Node, positive, top bool) {
	if n == nil {
		return
	}
	switch n.Op {
	case OpAnd:
		for _, k := range n.Kids {
			q.walk(k, positive, top)
		}
	case OpOr:
		q.Simple = false
		for _, k := range n.Kids {
			q.walk(k, positive, false)
		}
	case OpNot:
		q.Simple = false
		q.walk(n.Kids[0], !positive, false)
	case OpTerm:
		for len(q.positive) <= n.Term {
			q.positive = append(q.positive, false)
		}
		q.positive[n.Term] = positive
		if positive && !slices.ContainsFunc(q.Terms, func(t *Term) bool { return t.Pattern == q.AllTerms[n.Term].Pattern }) {
			q.Terms = append(q.Terms, q.AllTerms[n.Term])
		}
	case OpFile:
		if top && positive {
			q.Files = append(q.Files, n.File)
		}
	}
}

// An item is a token after parenthesis handling.
type item struct {
	tok   token
	paren byte   // '(' or ')' for a grouping parenthesis
	op    string // "AND", "OR" or "NOT"
}

// groupParens splits grouping parentheses off tokens. A matched pair of
// parentheses at the start and end of tokens groups if a parenthesis
// stands alone or an operator appears between them; otherwise the
// parentheses are part of the search text, as in "func (b *Reader)".
func groupParens(toks []token) []item {
	type mark struct{ tok, n int }
	lead := make([]int, len(toks))  // leading '(' count
	trail := make([]int, len(toks)) // trailing ')' count
	for i, t := range toks {
		if t.quoted {
			continue
		}
		for lead[i] < len(t.text) && t.text[lead[i]] == '(' {
			lead[i]++
		}
		for trail[i] < len(t.text)-lead[i] && t.text[len(t.text)-1-trail[i]] == ')' {
			trail[i]++
		}
	}
	isOp := func(i int) bool {
		t := toks[i]
		return !t.quoted && (t.text == "OR" || t.text == "AND" || t.text == "NOT")
	}
	open := make([]int, len(toks))  // grouping '(' per token
	close := make([]int, len(toks)) // grouping ')' per token
	var stack []int
	for i := range toks {
		for range lead[i] {
			stack = append(stack, i)
		}
		for range trail[i] {
			if len(stack) == 0 {
				break
			}
			a := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			group := len(toks[a].text) == lead[a] || len(toks[i].text) == trail[i]
			for j := a + 1; j < i && !group; j++ {
				group = isOp(j)
			}
			if a != i && group {
				open[a]++
				close[i]++
			}
		}
	}
	var items []item
	for i, t := range toks {
		for range open[i] {
			items = append(items, item{paren: '('})
		}
		core := t
		core.text = t.text[open[i] : len(t.text)-close[i]]
		switch {
		case core.text == "" && !t.quoted:
			// only parentheses
		case isOp(i):
			items = append(items, item{op: t.text})
		default:
			items = append(items, item{tok: core})
		}
		for range close[i] {
			items = append(items, item{paren: ')'})
		}
	}
	return items
}

type parser struct {
	q     *Query
	items []item
	pos   int
}

func (p *parser) peek() *item {
	if p.pos < len(p.items) {
		return &p.items[p.pos]
	}
	return nil
}

func (p *parser) parseOr() (*Node, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	var kids []*Node
	for it := p.peek(); it != nil && it.op == "OR"; it = p.peek() {
		p.pos++
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		if left == nil || right == nil {
			return nil, fmt.Errorf("OR needs search terms on both sides")
		}
		if kids == nil {
			kids = []*Node{left}
		}
		kids = append(kids, right)
	}
	if kids != nil {
		return &Node{Op: OpOr, Kids: kids}, nil
	}
	return left, nil
}

func (p *parser) parseAnd() (*Node, error) {
	var n *Node
	for {
		it := p.peek()
		if it == nil || it.paren == ')' || it.op == "OR" {
			return n, nil
		}
		if it.op == "AND" {
			p.pos++
			continue
		}
		k, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		if k != nil {
			n = and(n, k)
		}
	}
}

func (p *parser) parseUnary() (*Node, error) {
	it := p.peek()
	p.pos++
	switch {
	case it.op == "NOT":
		if p.peek() == nil {
			return nil, fmt.Errorf("NOT needs a search term")
		}
		k, err := p.parseUnary()
		if err != nil || k == nil {
			return nil, err
		}
		return &Node{Op: OpNot, Kids: []*Node{k}}, nil
	case it.paren == '(':
		n, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if c := p.peek(); c == nil || c.paren != ')' {
			return nil, fmt.Errorf("missing )")
		}
		p.pos++
		if n == nil {
			return nil, fmt.Errorf("empty ( )")
		}
		return n, nil
	}
	return p.atom(it.tok)
}

// atom parses a single search term or filter. Filters that apply to
// the whole query (repo:, case:, sym:) are recorded in the Query and
// return a nil node.
func (p *parser) atom(t token) (*Node, error) {
	q := p.q
	neg := false
	text := t.text
	if !t.quoted && len(text) > 1 && text[0] == '-' {
		neg = true
		text = text[1:]
	}
	wrap := func(n *Node) (*Node, error) {
		if neg {
			n = &Node{Op: OpNot, Kids: []*Node{n}}
		}
		return n, nil
	}
	key, val, ok := "", "", false
	if !t.quoted || t.keyed {
		if i := strings.IndexByte(text, ':'); i > 0 && isKey(text[:i]) {
			key, val, ok = strings.ToLower(text[:i]), text[i+1:], true
		}
	}
	if !ok {
		term, err := newTerm(text, t.quoted, q.Case)
		if err != nil {
			return nil, err
		}
		return wrap(q.leaf(term))
	}
	if val == "" {
		return nil, fmt.Errorf("missing value for %s:", key)
	}
	switch key {
	case "file", "f", "path":
		pat := val
		if isGlob(val) {
			pat = globToRegexp(val)
		}
		re, err := stdregexp.Compile(smartCase(val, q.Case) + pat)
		if err != nil {
			re = stdregexp.MustCompile(smartCase(val, q.Case) + stdregexp.QuoteMeta(val))
		}
		return wrap(&Node{Op: OpFile, File: re})
	case "lang", "l", "language":
		f := lang.NewFilter(val)
		if f == nil {
			return nil, fmt.Errorf("unknown language %q", val)
		}
		return wrap(&Node{Op: OpLang, Lang: f})
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
		return nil, nil
	case "case":
		return nil, nil // handled in ParseQuery
	case "sym", "symbol", "def":
		if !identRE.MatchString(val) {
			return nil, fmt.Errorf("sym: requires an identifier")
		}
		q.Symbol = val
		return nil, nil
	case "content", "c":
		term, err := newTerm(val, t.quoted, q.Case)
		if err != nil {
			return nil, err
		}
		return wrap(q.leaf(term))
	case "usage", "code", "comment", "string", "str":
		term, err := newTerm(val, t.quoted, q.Case)
		if err != nil {
			return nil, err
		}
		term.Region = map[string]region{"usage": regionCode, "code": regionCode, "comment": regionComment, "string": regionString, "str": regionString}[key]
		return wrap(q.leaf(term))
	}
	return nil, nil
}

// isGlob reports whether a file: value is a glob like "*.kt" or
// "src/**/*.go" rather than a regular expression: it contains * but no
// regexp-only syntax (such as ".*", "\\", "^", "$", "(", "|" or "+").
func isGlob(v string) bool {
	return strings.Contains(v, "*") && !strings.Contains(v, ".*") && !strings.ContainsAny(v, `\^$()|+{}[`)
}

// globToRegexp converts a path glob to a regexp. "*" matches within a
// path element, "**" across elements and "?" one character. A leading
// "/" anchors the glob at the repository root; otherwise it may match
// any trailing part of the path made of whole elements.
func globToRegexp(g string) string {
	var b strings.Builder
	if strings.HasPrefix(g, "/") {
		b.WriteString("^")
		g = g[1:]
	} else {
		b.WriteString("(?:^|/)")
	}
	for i := 0; i < len(g); i++ {
		switch c := g[i]; {
		case strings.HasPrefix(g[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(g[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(stdregexp.QuoteMeta(string(c)))
		}
	}
	if strings.HasSuffix(g, "/") {
		return b.String() // a directory: match everything inside
	}
	b.WriteString("$")
	return b.String()
}

// smartCase returns the regexp flag prefix for text under the case
// mode: case-insensitive by default, case-sensitive for "yes", and for
// "auto" case-sensitive only if the text has an upper-case letter.
func smartCase(text, mode string) string {
	switch mode {
	case "yes":
		return ""
	case "auto":
	default:
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
	case "file", "f", "path", "lang", "l", "language", "repo", "r", "case", "sym", "symbol", "def", "content", "c",
		"usage", "code", "comment", "string", "str":
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
