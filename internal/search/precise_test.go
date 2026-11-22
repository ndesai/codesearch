// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package search

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ndesai/codesearch/internal/config"
	"github.com/ndesai/codesearch/internal/indexer"
	"github.com/ndesai/codesearch/internal/scip"
)

func TestPreciseXref(t *testing.T) {
	t.Setenv(config.IndexDirEnv, "")
	root := t.TempDir()
	past := time.Now().Add(-time.Hour)
	files := map[string]string{
		"a/reader.go": "package a\n\ntype Reader struct{}\n\nfunc (r *Reader) Close() error { return nil }\n",
		"a/writer.go": "package a\n\ntype Writer struct{}\n\nfunc (w *Writer) Close() error { return nil }\n",
		"cmd/main.go": "package main\n\nfunc main() {\n\tr.Close()\n\tw.Close()\n}\n",
	}
	for name, content := range files {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0755)
		os.WriteFile(p, []byte(content), 0644)
		os.Chtimes(p, past, past)
	}
	const (
		rc = "scip-go gomod m v1 `m/a`/Reader#Close()."
		wc = "scip-go gomod m v1 `m/a`/Writer#Close()."
	)
	idx := scip.EncodeIndex("file://"+filepath.ToSlash(root),
		scip.EncodeDocument("a/reader.go", scip.EncodeOccurrence([]int{4, 17, 22}, rc, scip.RoleDefinition)),
		scip.EncodeDocument("a/writer.go", scip.EncodeOccurrence([]int{4, 17, 22}, wc, scip.RoleDefinition)),
		scip.EncodeDocument("cmd/main.go", scip.EncodeOccurrence([]int{3, 3, 8}, rc, 0), scip.EncodeOccurrence([]int{4, 3, 8}, wc, 0)),
	)
	scipFile := filepath.Join(t.TempDir(), "index.scip")
	os.WriteFile(scipFile, idx, 0644)

	yaml := fmt.Sprintf("index_dir: %s\nrepos:\n  - path: %s\n    name: m\n    scip: %s\n", filepath.Join(t.TempDir(), "idx"), root, scipFile)
	cfg, err := config.Parse(filepath.Join(root, "c.yaml"), []byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	indexer.Update(cfg, indexer.Options{Logf: func(string, ...any) {}})
	e := New(cfg)
	ctx := context.Background()

	// Clicking r.Close() on line 4: only Reader.Close.
	r, err := e.XrefAt(ctx, "Close", "m", "cmd/main.go", 4)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Precise || len(r.Defs) != 1 || r.Defs[0].Path != "a/reader.go" || r.Defs[0].Line != 5 {
		t.Fatalf("precise defs: %+v %+v", r, r.Defs)
	}
	if r.NumRefs != 1 || r.Refs[0].Path != "cmd/main.go" || r.Refs[0].Lines[0].Line != 4 || r.Refs[0].Lines[0].Text != "\tr.Close()" {
		t.Errorf("precise refs: %+v", r.Refs)
	}
	// Search-based cross references can't tell the two apart.
	h, _ := e.Xref(ctx, "Close", "m", "cmd/main.go")
	if h.Precise || len(h.Defs) != 2 {
		t.Errorf("heuristic defs: %d %v", len(h.Defs), h.Precise)
	}
	// A line without an occurrence falls back to search.
	if r, _ := e.XrefAt(ctx, "Close", "m", "cmd/main.go", 3); r.Precise {
		t.Error("line without an occurrence answered precisely")
	}

	// Edit reader.go after the index was built: its definition is no
	// longer trusted, and clicks inside it fall back to search.
	os.Chtimes(filepath.Join(root, "a/reader.go"), time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	r, _ = e.XrefAt(ctx, "Close", "m", "cmd/main.go", 4)
	if !r.Precise || len(r.Defs) != 0 || r.StaleFiles != 1 {
		t.Errorf("with a stale file: defs %+v stale %d", r.Defs, r.StaleFiles)
	}
	if r, _ := e.XrefAt(ctx, "Close", "m", "a/reader.go", 5); r.Precise {
		t.Error("click in a stale file answered precisely")
	}
}
