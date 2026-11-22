// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package symbols

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	files := []File{
		{"b/b.go", []Symbol{{"Reader", "struct", 3, 0}, {"Read", "method", 10, 1}}},
		{"a/a.go", []Symbol{{"Reader", "interface", 5, 0}, {"helper", "func", 20, 0}}},
		{"c/empty.go", nil},
		{"a/ab.go", []Symbol{{"Zeta", "func", 1, 0}}},
	}
	name := filepath.Join(t.TempDir(), "x.sym")
	if err := Write(name, files); err != nil {
		t.Fatal(err)
	}
	tab, err := Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer tab.Close()
	if tab.Len() != 5 {
		t.Fatalf("Len = %d", tab.Len())
	}
	got := tab.Lookup("Reader")
	want := []Entry{{"a/a.go", "Reader", "interface", 5, 0}, {"b/b.go", "Reader", "struct", 3, 0}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Lookup(Reader) = %+v", got)
	}
	if len(tab.Lookup("Rea")) != 0 || len(tab.Lookup("Zz")) != 0 || len(tab.Lookup("")) != 0 {
		t.Error("lookup of a missing name returned results")
	}
	var matched []string
	tab.Match(func(n string) bool { return strings.HasPrefix(n, "Re") }, func(e Entry) bool {
		matched = append(matched, fmt.Sprintf("%s@%s:%d", e.Name, e.Path, e.Line))
		return true
	})
	if strings.Join(matched, " ") != "Read@b/b.go:10 Reader@a/a.go:5 Reader@b/b.go:3" {
		t.Errorf("Match = %v", matched)
	}
	// Files round-trips (minus empty files).
	back := tab.Files()
	if len(back) != 3 || back[0].Path != "a/a.go" || len(back[0].Symbols) != 2 || back[0].Symbols[0].Name != "Reader" {
		t.Errorf("Files = %+v", back)
	}
}

func TestEmptyAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "e.sym")
	if err := Write(name, nil); err != nil {
		t.Fatal(err)
	}
	tab, err := Open(name)
	if err != nil {
		t.Fatal(err)
	}
	if tab.Len() != 0 || tab.Lookup("x") != nil {
		t.Error("empty table")
	}
	tab.Close()
	data, _ := os.ReadFile(name)
	os.WriteFile(name, data[:len(data)-5], 0644)
	if _, err := Open(name); err == nil {
		t.Error("truncated table opened")
	}
	var nilTab *Table
	if nilTab.Lookup("x") != nil || nilTab.Files() != nil || nilTab.Close() != nil {
		t.Error("nil table")
	}
}
