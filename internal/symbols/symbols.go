// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package symbols stores the symbol definitions (types, functions,
// methods, ...) of the files in an index shard, sorted by name, so that
// definitions can be looked up without reading any source files.
//
// A table file has the format:
//
//	"codesearch symbols 1\n"
//	kinds: count, then each kind name             (varint length + bytes)
//	paths: count, then each path, prefix-compressed (prefix len, suffix)
//	names: the de-duplicated symbol names, each followed by a NUL byte
//	entries: count × 12-byte records sorted by (name, path, line):
//	    nameOff uint32, path uint24, kind uint8, line uint24, depth uint8
//	    (little-endian)
//	trailer: offsets of the kinds, paths, names and entries sections
//	    and the entry count, as uint64s, then "\nend symbols\n"
//
// Entries are fixed size so a lookup is a binary search over the
// memory-mapped file, with no per-table heap allocation.
package symbols

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
)

const (
	magic     = "codesearch symbols 2\n"
	trailer   = "\nend symbols\n"
	entrySize = 12
	maxU24    = 1<<24 - 1
)

// A Symbol is one definition in a file.
type Symbol struct {
	Name  string
	Kind  string
	Line  int
	Depth int
}

// A File is the symbols of one file (path relative to the repository).
type File struct {
	Path    string
	Symbols []Symbol
}

// An Entry is a lookup result.
type Entry struct {
	Path  string
	Name  string
	Kind  string
	Line  int
	Depth int
}

// Write writes a table for files to the named file. Files may be in any
// order; files without symbols are omitted.
func Write(name string, files []File) error {
	files = slices.DeleteFunc(slices.Clone(files), func(f File) bool { return len(f.Symbols) == 0 })
	slices.SortFunc(files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })

	type rec struct {
		name        string
		kind, depth uint8
		path, line  uint32
	}
	var (
		recs     []rec
		kinds    []string
		kindIdx  = map[string]uint8{}
		pathList []string
	)
	for pi, f := range files {
		pathList = append(pathList, f.Path)
		for _, s := range f.Symbols {
			k, ok := kindIdx[s.Kind]
			if !ok {
				if len(kinds) == 255 {
					s.Kind = "other"
					if k, ok = kindIdx["other"]; !ok {
						k = uint8(len(kinds))
						kindIdx["other"] = k
						kinds = append(kinds, "other")
					}
				} else {
					k = uint8(len(kinds))
					kindIdx[s.Kind] = k
					kinds = append(kinds, s.Kind)
				}
			}
			if s.Name == "" || len(s.Name) > 0xFFFF || strings.IndexByte(s.Name, 0) >= 0 || s.Line > maxU24 || pi > maxU24 {
				continue
			}
			recs = append(recs, rec{s.Name, k, uint8(min(max(s.Depth, 0), 255)), uint32(pi), uint32(max(s.Line, 0))})
		}
	}
	slices.SortFunc(recs, func(a, b rec) int {
		if c := strings.Compare(a.name, b.name); c != 0 {
			return c
		}
		if a.path != b.path {
			return int(a.path) - int(b.path)
		}
		return int(a.line) - int(b.line)
	})

	tmp := name + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	off := 0
	write := func(b []byte) { w.Write(b); off += len(b) }
	var vb [binary.MaxVarintLen64]byte
	writeN := func(n int) { write(vb[:binary.PutUvarint(vb[:], uint64(n))]) }
	writeS := func(s string) { writeN(len(s)); write([]byte(s)) }

	write([]byte(magic))
	kindsOff := off
	writeN(len(kinds))
	for _, k := range kinds {
		writeS(k)
	}
	pathsOff := off
	writeN(len(pathList))
	last := ""
	for _, p := range pathList {
		n := 0
		for n < len(last) && n < len(p) && last[n] == p[n] {
			n++
		}
		writeN(n)
		writeS(p[n:])
		last = p
	}
	namesOff := off
	nameOff := make([]uint32, len(recs))
	for i, r := range recs {
		if i > 0 && r.name == recs[i-1].name {
			nameOff[i] = nameOff[i-1]
			continue
		}
		if off-namesOff+len(r.name) > 1<<32-1 {
			f.Close()
			os.Remove(tmp)
			return errors.New("symbol table too large")
		}
		nameOff[i] = uint32(off - namesOff)
		write([]byte(r.name))
		write([]byte{0})
	}
	// Align entries for tidiness (not required).
	for off%8 != 0 {
		write([]byte{0})
	}
	entriesOff := off
	var e [entrySize]byte
	for i, r := range recs {
		binary.LittleEndian.PutUint32(e[0:], nameOff[i])
		putU24(e[4:], r.path)
		e[7] = r.kind
		putU24(e[8:], r.line)
		e[11] = r.depth
		write(e[:])
	}
	var t [8]byte
	for _, v := range []int{kindsOff, pathsOff, namesOff, entriesOff, len(recs)} {
		binary.LittleEndian.PutUint64(t[:], uint64(v))
		write(t[:])
	}
	write([]byte(trailer))
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, name)
}

// A Table is an open, read-only symbol table.
type Table struct {
	data     []byte
	unmap    func() error
	kinds    []string
	paths    []string
	names    []byte
	entries  []byte
	numEntry int
}

var errCorrupt = errors.New("corrupt symbol table")

// Open opens a symbol table file.
func Open(name string) (*Table, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	data, unmap, err := mapFile(f, int(st.Size()))
	if err != nil {
		return nil, err
	}
	t := &Table{data: data, unmap: unmap}
	if err := t.parse(); err != nil {
		unmap()
		return nil, fmt.Errorf("%s: %v", name, err)
	}
	return t, nil
}

func (t *Table) parse() (err error) {
	defer func() {
		if recover() != nil {
			err = errCorrupt
		}
	}()
	d := t.data
	if len(d) < len(magic)+len(trailer)+40 || string(d[:len(magic)]) != magic || string(d[len(d)-len(trailer):]) != trailer {
		return errCorrupt
	}
	tr := d[len(d)-len(trailer)-40:]
	u := func(i int) int { return int(binary.LittleEndian.Uint64(tr[i*8:])) }
	kindsOff, pathsOff, namesOff, entriesOff, n := u(0), u(1), u(2), u(3), u(4)
	if entriesOff+n*entrySize != len(d)-len(trailer)-40 {
		return errCorrupt
	}
	readN := func(b *[]byte) int {
		v, k := binary.Uvarint(*b)
		if k <= 0 {
			panic(errCorrupt)
		}
		*b = (*b)[k:]
		return int(v)
	}
	readS := func(b *[]byte) string {
		l := readN(b)
		s := string((*b)[:l])
		*b = (*b)[l:]
		return s
	}
	b := d[kindsOff:pathsOff]
	for range readN(&b) {
		t.kinds = append(t.kinds, readS(&b))
	}
	b = d[pathsOff:namesOff]
	last := ""
	np := readN(&b)
	t.paths = make([]string, 0, np)
	for range np {
		pre := readN(&b)
		p := last[:pre] + readS(&b)
		t.paths = append(t.paths, p)
		last = p
	}
	t.names = d[namesOff:entriesOff]
	t.entries = d[entriesOff : entriesOff+n*entrySize]
	t.numEntry = n
	return nil
}

// Close releases the table's memory mapping.
func (t *Table) Close() error {
	if t == nil || t.unmap == nil {
		return nil
	}
	err := t.unmap()
	t.unmap = nil
	t.data, t.entries, t.names = nil, nil, nil
	return err
}

// Len returns the number of symbols in the table.
func (t *Table) Len() int { return t.numEntry }

func putU24(b []byte, v uint32) {
	b[0], b[1], b[2] = byte(v), byte(v>>8), byte(v>>16)
}

func u24(b []byte) int {
	return int(b[0]) | int(b[1])<<8 | int(b[2])<<16
}

func (t *Table) nameAt(i int) []byte {
	off := binary.LittleEndian.Uint32(t.entries[i*entrySize:])
	n := t.names[off:]
	return n[:bytes.IndexByte(n, 0)]
}

func (t *Table) entry(i int) Entry {
	e := t.entries[i*entrySize:]
	return Entry{
		Path:  t.paths[u24(e[4:])],
		Name:  string(t.nameAt(i)),
		Kind:  t.kinds[e[7]],
		Depth: int(e[11]),
		Line:  u24(e[8:]),
	}
}

// Lookup returns the symbols named exactly name.
func (t *Table) Lookup(name string) []Entry {
	if t == nil {
		return nil
	}
	b := []byte(name)
	i := sort.Search(t.numEntry, func(i int) bool { return bytes.Compare(t.nameAt(i), b) >= 0 })
	var out []Entry
	for ; i < t.numEntry && bytes.Equal(t.nameAt(i), b); i++ {
		out = append(out, t.entry(i))
	}
	return out
}

// Match calls fn for each symbol whose name satisfies match, stopping
// if fn returns false. Each distinct name is tested once.
func (t *Table) Match(match func(name string) bool, fn func(Entry) bool) {
	if t == nil {
		return
	}
	var lastName []byte
	ok := false
	for i := 0; i < t.numEntry; i++ {
		n := t.nameAt(i)
		if i == 0 || !bytes.Equal(n, lastName) {
			lastName = n
			ok = match(string(n))
		}
		if ok && !fn(t.entry(i)) {
			return
		}
	}
}

// Files returns the symbols grouped by file, for rewriting a table.
func (t *Table) Files() []File {
	if t == nil {
		return nil
	}
	byPath := make([][]Symbol, len(t.paths))
	for i := 0; i < t.numEntry; i++ {
		e := t.entries[i*entrySize:]
		p := u24(e[4:])
		byPath[p] = append(byPath[p], Symbol{
			Name:  string(t.nameAt(i)),
			Kind:  t.kinds[e[7]],
			Depth: int(e[11]),
			Line:  u24(e[8:]),
		})
	}
	files := make([]File, 0, len(t.paths))
	for i, p := range t.paths {
		syms := byPath[i]
		slices.SortFunc(syms, func(a, b Symbol) int { return a.Line - b.Line })
		files = append(files, File{Path: p, Symbols: syms})
	}
	return files
}
