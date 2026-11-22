// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndesai/codesearch/internal/config"
	"github.com/ndesai/codesearch/internal/indexer"
	"github.com/ndesai/codesearch/internal/search"
)

// NewTestService returns a service over a small indexed repository named "demo".
func newTestService(t *testing.T) *Service {
	t.Helper()
	t.Setenv(config.IndexDirEnv, "")
	root := t.TempDir()
	files := map[string]string{
		"lib/reader.go":   "package lib\n\n// NewReader returns a reader.\nfunc NewReader() *Reader {\n\treturn &Reader{}\n}\n\ntype Reader struct{}\n",
		"cmd/main.go":     "package main\n\nimport \"demo/lib\"\n\nfunc main() {\n\tlib.NewReader()\n}\n",
		"docs/notes.txt":  "NewReader is documented here.\n",
		"assets/logo.bin": "\x00\x01\x02binary",
	}
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0755)
		os.WriteFile(p, []byte(content), 0644)
	}
	cfg, err := config.Parse(filepath.Join(root, "c.yaml"), []byte(fmt.Sprintf("index_dir: %s\nrepos:\n  - path: %s\n    name: demo\n", filepath.Join(t.TempDir(), "idx"), root)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Update(cfg, indexer.Options{Logf: func(string, ...any) {}}); err != nil {
		t.Fatal(err)
	}
	return &Service{Engine: search.New(cfg)}
}

func TestService(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	r, err := s.Search(ctx, "NewReader", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.NumFiles != 3 || r.Files[0].Path != "lib/reader.go" || !r.Files[0].Matches[0].Def {
		t.Errorf("search: %+v %+v", r, r.Files[0])
	}
	if r, _ := s.Search(ctx, "NewReader", 1, 1); len(r.Files) != 1 || r.Files[0].Path == "lib/reader.go" {
		t.Errorf("paging: %+v", r.Files)
	}
	if _, err := s.Search(ctx, "type:commit x", 0, 0); err == nil {
		t.Error("type:commit accepted")
	}

	f, err := s.ReadFile("demo", "lib/reader.go", 4, 5)
	if err != nil || f.TotalLines != 8 || f.StartLine != 4 || len(f.Lines) != 2 || f.Lines[0] != "func NewReader() *Reader {" {
		t.Errorf("read range: %+v %v", f, err)
	}
	d, err := s.ReadFile("demo", "", 0, 0)
	if err != nil || d.Kind != "dir" || len(d.Entries) != 4 || d.Entries[0].Name != "assets" || !d.Entries[0].Dir {
		t.Errorf("read dir: %+v %v", d, err)
	}
	if b, _ := s.ReadFile("demo", "assets/logo.bin", 0, 0); !b.Binary {
		t.Error("binary file not flagged")
	}
	for _, bad := range []string{"../etc/passwd", "nope.go"} {
		if _, err := s.ReadFile("demo", bad, 0, 0); err == nil {
			t.Errorf("ReadFile(%q) succeeded", bad)
		}
	}

	defs, _ := s.Definitions(ctx, "NewReader", "demo", "cmd/main.go")
	if len(defs) != 1 || defs[0].Path != "lib/reader.go" || defs[0].Line != 4 {
		t.Errorf("definitions: %+v", defs)
	}
	refs, total, _ := s.References(ctx, "NewReader", "demo", "cmd/main.go", 1)
	if total != 3 || len(refs) != 1 || refs[0].Path != "cmd/main.go" {
		t.Errorf("references: %d %+v", total, refs)
	}
	syms, _ := s.Outline("demo", "lib/reader.go")
	if len(syms) != 2 || syms[0].Name != "NewReader" || syms[1].Name != "Reader" {
		t.Errorf("outline: %+v", syms)
	}
}

func call(t *testing.T, s *Service, method string, params any) map[string]any {
	t.Helper()
	p, _ := json.Marshal(params)
	msg := fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":%q,"params":%s}`, method, p)
	var resp map[string]any
	if err := json.Unmarshal(s.HandleMCP(context.Background(), []byte(msg)), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["id"] != float64(7) || resp["jsonrpc"] != "2.0" {
		t.Fatalf("bad envelope: %v", resp)
	}
	return resp
}

func toolText(t *testing.T, s *Service, name string, args map[string]any) (string, bool) {
	t.Helper()
	resp := call(t, s, "tools/call", map[string]any{"name": name, "arguments": args})
	res := resp["result"].(map[string]any)
	return res["content"].([]any)[0].(map[string]any)["text"].(string), res["isError"].(bool)
}

func TestMCP(t *testing.T) {
	s := newTestService(t)
	init := call(t, s, "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test"}})
	res := init["result"].(map[string]any)
	if res["protocolVersion"] != "2025-03-26" || res["serverInfo"].(map[string]any)["name"] != "nd-code-search" {
		t.Errorf("initialize: %v", res)
	}
	if s.HandleMCP(context.Background(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)) != nil {
		t.Error("notification got a response")
	}
	list := call(t, s, "tools/list", nil)["result"].(map[string]any)["tools"].([]any)
	var names []string
	for _, x := range list {
		names = append(names, x.(map[string]any)["name"].(string))
	}
	if strings.Join(names, " ") != "search_code read_file find_definitions find_references get_outline list_repos" {
		t.Errorf("tools: %v", names)
	}

	text, isErr := toolText(t, s, "search_code", map[string]any{"query": "NewReader -file:docs"})
	if isErr || !strings.Contains(text, "demo:lib/reader.go") || !strings.Contains(text, "4: [definition] func NewReader()") || strings.Contains(text, "notes.txt") {
		t.Errorf("search_code:\n%s", text)
	}
	text, _ = toolText(t, s, "read_file", map[string]any{"repo": "demo", "path": "lib/reader.go", "start_line": 4, "end_line": 4})
	if !strings.Contains(text, "lines 4-4 of 8") || !strings.Contains(text, "     4\tfunc NewReader()") || !strings.Contains(text, "use start_line=5") {
		t.Errorf("read_file:\n%s", text)
	}
	text, _ = toolText(t, s, "find_definitions", map[string]any{"symbol": "NewReader"})
	if !strings.Contains(text, "demo:lib/reader.go:4:") {
		t.Errorf("find_definitions:\n%s", text)
	}
	text, _ = toolText(t, s, "find_references", map[string]any{"symbol": "NewReader", "repo": "demo", "path": "cmd/main.go"})
	if !strings.Contains(text, "3 references") || !strings.Contains(text, "demo:cmd/main.go:6:") {
		t.Errorf("find_references:\n%s", text)
	}
	text, _ = toolText(t, s, "get_outline", map[string]any{"repo": "demo", "path": "lib/reader.go"})
	if !strings.Contains(text, "func NewReader (line 4)") {
		t.Errorf("get_outline:\n%s", text)
	}
	text, _ = toolText(t, s, "list_repos", nil)
	if !strings.HasPrefix(text, "demo: ") || !strings.Contains(text, "3 files") { // logo.bin is excluded by default
		t.Errorf("list_repos:\n%s", text)
	}
	// Errors: tool failures are results; protocol errors are errors.
	if text, isErr := toolText(t, s, "read_file", map[string]any{"repo": "nope", "path": "x"}); !isErr || !strings.Contains(text, "unknown repository") {
		t.Errorf("tool error: %v %s", isErr, text)
	}
	if e := call(t, s, "tools/call", map[string]any{"name": "nope"})["error"].(map[string]any); e["code"] != float64(-32602) {
		t.Errorf("unknown tool: %v", e)
	}
	if e := call(t, s, "resources/list", nil)["error"].(map[string]any); e["code"] != float64(-32601) {
		t.Errorf("unknown method: %v", e)
	}
	var bad map[string]any
	json.Unmarshal(s.HandleMCP(context.Background(), []byte("{not json")), &bad)
	if bad["error"].(map[string]any)["code"] != float64(-32700) {
		t.Errorf("parse error: %v", bad)
	}
}
