// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package scip reads SCIP code intelligence indexes (see
// https://github.com/sourcegraph/scip), as produced by scip-go,
// scip-java, scip-typescript, scip-clang, scip-python and others, for
// precise definitions and references.
//
// Only the parts needed for navigation are decoded: each document's path
// and its symbol occurrences (range, symbol, roles). The protobuf wire
// format is decoded directly, without generated code.
package scip

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Symbol roles (a bit set).
const (
	RoleDefinition        = 0x1
	RoleImport            = 0x2
	RoleForwardDefinition = 0x40
)

// An Occurrence is one use or definition of a symbol in a document.
type Occurrence struct {
	Line     int // 0-based, as in SCIP
	StartCol int
	EndLine  int
	EndCol   int
	Symbol   string
	Roles    int32
}

// IsDefinition reports whether the occurrence defines its symbol.
func (o *Occurrence) IsDefinition() bool { return o.Roles&RoleDefinition != 0 }

// A Location is a place in a document.
type Location struct {
	Path     string // slash-separated, relative to the repository root
	Line     int    // 0-based
	StartCol int
	EndCol   int
}

// An Index holds the occurrences of one or more SCIP index files,
// with paths relative to a repository root.
type Index struct {
	Files []string                // the .scip files loaded
	Built time.Time               // modification time of the newest .scip file
	Docs  map[string][]Occurrence // path -> occurrences, sorted by position
	defs  map[string][]Location
	refs  map[string][]Location
}

// Definitions returns the definitions of symbol.
func (x *Index) Definitions(symbol string) []Location { return x.defs[symbol] }

// References returns the non-definition occurrences of symbol.
func (x *Index) References(symbol string) []Location { return x.refs[symbol] }

// At returns the occurrences on a 0-based line of a document.
func (x *Index) At(path string, line int) []Occurrence {
	occs := x.Docs[path]
	i, _ := slices.BinarySearchFunc(occs, line, func(o Occurrence, l int) int { return o.Line - l })
	var out []Occurrence
	for ; i < len(occs) && occs[i].Line == line; i++ {
		out = append(out, occs[i])
	}
	return out
}

// Load reads the SCIP index files at p (a .scip file, or a directory of
// them) for the repository rooted at repoRoot. Documents outside the
// repository are skipped.
func Load(p, repoRoot string) (*Index, error) {
	var files []string
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		ents, err := os.ReadDir(p)
		if err != nil {
			return nil, err
		}
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".scip") {
				files = append(files, filepath.Join(p, e.Name()))
			}
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("%s: no .scip files", p)
		}
	} else {
		files = []string{p}
	}
	x := &Index{Files: files, Docs: map[string][]Occurrence{}, defs: map[string][]Location{}, refs: map[string][]Location{}}
	for _, f := range files {
		fst, err := os.Stat(f)
		if err != nil {
			return nil, err
		}
		if fst.ModTime().After(x.Built) {
			x.Built = fst.ModTime()
		}
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		if err := x.add(data, repoRoot); err != nil {
			return nil, fmt.Errorf("%s: %v", f, err)
		}
	}
	for p := range x.Docs {
		occs := x.Docs[p]
		slices.SortFunc(occs, func(a, b Occurrence) int {
			if a.Line != b.Line {
				return a.Line - b.Line
			}
			return a.StartCol - b.StartCol
		})
	}
	return x, nil
}

var errCorrupt = errors.New("malformed SCIP index")

// add decodes one serialized scip.Index message.
func (x *Index) add(data []byte, repoRoot string) error {
	prefix := ""
	var docs [][]byte
	err := fields(data, func(num int, wire int, v uint64, b []byte) error {
		switch {
		case num == 1 && wire == 2: // metadata
			return fields(b, func(num, wire int, v uint64, b []byte) error {
				if num == 3 && wire == 2 { // project_root
					p, err := projectPrefix(string(b), repoRoot)
					if err != nil {
						return err
					}
					prefix = p
				}
				return nil
			})
		case num == 2 && wire == 2: // documents (decoded after metadata)
			docs = append(docs, b)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, d := range docs {
		if err := x.addDocument(d, prefix); err != nil {
			return err
		}
	}
	return nil
}

// projectPrefix returns the path of the index's project root relative to
// the repository root ("" if they are the same).
func projectPrefix(rootURI, repoRoot string) (string, error) {
	if rootURI == "" {
		return "", nil
	}
	u, err := url.Parse(rootURI)
	if err != nil || u.Scheme != "file" {
		return "", fmt.Errorf("unsupported project_root %q", rootURI)
	}
	rel, err := filepath.Rel(repoRoot, filepath.FromSlash(u.Path))
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("project_root %s is outside the repository %s", u.Path, repoRoot)
	}
	if rel == "." {
		return "", nil
	}
	return filepath.ToSlash(rel), nil
}

func (x *Index) addDocument(data []byte, prefix string) error {
	var rel string
	var occs []Occurrence
	err := fields(data, func(num, wire int, v uint64, b []byte) error {
		switch {
		case num == 1 && wire == 2: // relative_path
			rel = string(b)
		case num == 2 && wire == 2: // occurrences
			o, err := decodeOccurrence(b)
			if err != nil {
				return err
			}
			if o.Symbol != "" {
				occs = append(occs, o)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") {
		return nil
	}
	p := path.Clean(path.Join(prefix, rel))
	for i := range occs {
		o := &occs[i]
		if strings.HasPrefix(o.Symbol, "local ") {
			o.Symbol = p + "#" + o.Symbol // local symbols are per document
		}
		loc := Location{Path: p, Line: o.Line, StartCol: o.StartCol, EndCol: o.EndCol}
		switch {
		case o.IsDefinition():
			x.defs[o.Symbol] = append(x.defs[o.Symbol], loc)
		case o.Roles&RoleForwardDefinition != 0:
			// Declarations ahead of the definition (C++ headers): neither.
		default:
			x.refs[o.Symbol] = append(x.refs[o.Symbol], loc)
		}
	}
	x.Docs[p] = append(x.Docs[p], occs...)
	return nil
}

func decodeOccurrence(data []byte) (Occurrence, error) {
	var o Occurrence
	var rng []int
	err := fields(data, func(num, wire int, v uint64, b []byte) error {
		switch num {
		case 1: // range: packed (wire 2) or not (wire 0)
			if wire == 0 {
				rng = append(rng, int(int32(v)))
				return nil
			}
			for len(b) > 0 {
				n, k := uvarint(b)
				if k <= 0 {
					return errCorrupt
				}
				rng = append(rng, int(int32(n)))
				b = b[k:]
			}
		case 2:
			o.Symbol = string(b)
		case 3:
			o.Roles = int32(v)
		}
		return nil
	})
	if err != nil {
		return o, err
	}
	switch len(rng) {
	case 3:
		o.Line, o.StartCol, o.EndLine, o.EndCol = rng[0], rng[1], rng[0], rng[2]
	case 4:
		o.Line, o.StartCol, o.EndLine, o.EndCol = rng[0], rng[1], rng[2], rng[3]
	default:
		return o, errCorrupt
	}
	return o, nil
}

// fields calls fn for each field of a protobuf message. For varint
// fields v holds the value; for length-delimited fields b holds the bytes.
func fields(data []byte, fn func(num, wire int, v uint64, b []byte) error) error {
	for len(data) > 0 {
		tag, k := uvarint(data)
		if k <= 0 {
			return errCorrupt
		}
		data = data[k:]
		num, wire := int(tag>>3), int(tag&7)
		var v uint64
		var b []byte
		switch wire {
		case 0:
			v, k = uvarint(data)
			if k <= 0 {
				return errCorrupt
			}
			data = data[k:]
		case 1:
			if len(data) < 8 {
				return errCorrupt
			}
			data = data[8:]
		case 2:
			n, k := uvarint(data)
			if k <= 0 || uint64(len(data)-k) < n {
				return errCorrupt
			}
			b = data[k : k+int(n)]
			data = data[k+int(n):]
		case 5:
			if len(data) < 4 {
				return errCorrupt
			}
			data = data[4:]
		default:
			return errCorrupt
		}
		if err := fn(num, wire, v, b); err != nil {
			return err
		}
	}
	return nil
}

func uvarint(b []byte) (uint64, int) {
	var x uint64
	var s uint
	for i, c := range b {
		if i == 10 {
			return 0, -1
		}
		if c < 0x80 {
			return x | uint64(c)<<s, i + 1
		}
		x |= uint64(c&0x7f) << s
		s += 7
	}
	return 0, 0
}

var symbolNameRE = regexp.MustCompile("([A-Za-z_$][\\w$]*)`?[^\\w$`]*$")

// SymbolName returns the name a SCIP symbol ends with, such as "Merge"
// for "scip-go gomod example.com/m v1 `example.com/m/pkg`/Merge().".
func SymbolName(symbol string) string {
	if strings.Contains(symbol, "#local ") || strings.HasPrefix(symbol, "local ") {
		return ""
	}
	m := symbolNameRE.FindStringSubmatch(symbol)
	if m == nil {
		return ""
	}
	return m[1]
}
