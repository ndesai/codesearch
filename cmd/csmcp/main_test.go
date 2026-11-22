// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndesai/codesearch/internal/api"
	"github.com/ndesai/codesearch/internal/config"
	"github.com/ndesai/codesearch/internal/indexer"
	"github.com/ndesai/codesearch/internal/search"
)

func TestStdio(t *testing.T) {
	t.Setenv(config.IndexDirEnv, "")
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nfunc Hello() {}\n"), 0644)
	cfg, _ := config.Parse(filepath.Join(root, "c.yaml"), []byte(fmt.Sprintf("index_dir: %s\nrepos:\n  - path: %s\n    name: r\n", filepath.Join(t.TempDir(), "idx"), root)))
	indexer.Update(cfg, indexer.Options{Logf: func(string, ...any) {}})
	svc := &api.Service{Engine: search.New(cfg)}

	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		``,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_code","arguments":{"query":"Hello"}}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	serveStdio(svc, strings.NewReader(in), &out)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d responses:\n%s", len(lines), out.String())
	}
	byID := map[float64]map[string]any{}
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad line %q: %v", l, err)
		}
		byID[m["id"].(float64)] = m
	}
	if byID[1]["result"].(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Errorf("initialize: %v", byID[1])
	}
	text := byID[2]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "r:a.go") {
		t.Errorf("search_code over stdio: %s", text)
	}
}
