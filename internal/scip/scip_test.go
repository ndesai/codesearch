// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scip

import (
	"os"
	"path/filepath"
	"testing"
)

var (
	Occ        = EncodeOccurrence
	Doc        = EncodeDocument
	IndexBytes = EncodeIndex
)

const merge = "scip-go gomod example.com/m v1 `example.com/m/index`/Merge()."

func TestLoad(t *testing.T) {
	repo := t.TempDir()
	data := IndexBytes("file://"+filepath.ToSlash(repo)+"/sub",
		Doc("index/merge.go",
			Occ([]int{50, 5, 10}, merge, RoleDefinition),
			Occ([]int{60, 1, 2}, "local 1", RoleDefinition),
			Occ([]int{61, 3, 4}, "local 1", 0)),
		Doc("cmd/main.go",
			Occ([]int{9, 7, 12}, merge, 0),
			Occ([]int{9, 0, 3}, "scip-go gomod example.com/m v1 `example.com/m/x`/Foo#", 0),
			Occ([]int{20, 1, 2, 6}, "local 1", 0)),
	)
	f := filepath.Join(t.TempDir(), "index.scip")
	os.WriteFile(f, data, 0644)
	x, err := Load(f, repo)
	if err != nil {
		t.Fatal(err)
	}
	if d := x.Definitions(merge); len(d) != 1 || d[0] != (Location{"sub/index/merge.go", 50, 5, 10}) {
		t.Errorf("definitions: %+v", d)
	}
	if r := x.References(merge); len(r) != 1 || r[0].Path != "sub/cmd/main.go" || r[0].Line != 9 {
		t.Errorf("references: %+v", r)
	}
	// Local symbols are per document.
	if r := x.References("sub/index/merge.go#local 1"); len(r) != 1 || r[0].Line != 61 {
		t.Errorf("local references: %+v", r)
	}
	if r := x.References("sub/cmd/main.go#local 1"); len(r) != 1 || r[0].EndCol != 6 {
		t.Errorf("4-element range: %+v", r)
	}
	at := x.At("sub/cmd/main.go", 9)
	if len(at) != 2 || at[0].StartCol != 0 || at[1].Symbol != merge {
		t.Errorf("At: %+v", at)
	}
	if SymbolName(merge) != "Merge" || SymbolName("scip-go gomod m v1 `m/x`/Foo#Bar().") != "Bar" || SymbolName("a/b.go#local 3") != "" {
		t.Errorf("SymbolName: %q", SymbolName(merge))
	}

	// A project root outside the repository is an error; corrupt data too.
	os.WriteFile(f, IndexBytes("file:///elsewhere"), 0644)
	if _, err := Load(f, repo); err == nil {
		t.Error("outside project root accepted")
	}
	os.WriteFile(f, []byte{0x12, 0xff}, 0644)
	if _, err := Load(f, repo); err == nil {
		t.Error("corrupt index accepted")
	}
}
