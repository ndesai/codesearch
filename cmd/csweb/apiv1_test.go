// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ndesai/codesearch/internal/config"
	"github.com/ndesai/codesearch/internal/indexer"
	"github.com/ndesai/codesearch/internal/search"
)

func TestAPIv1(t *testing.T) {
	t.Setenv(config.IndexDirEnv, "")
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nfunc Hello() {}\n\nfunc x() { Hello() }\n"), 0644)
	cfg, _ := config.Parse(filepath.Join(root, "c.yaml"), []byte(fmt.Sprintf("index_dir: %s\napi_token: s3cret\nrepos:\n  - path: %s\n    name: r\n", filepath.Join(t.TempDir(), "idx"), root)))
	indexer.Update(cfg, indexer.Options{Logf: func(string, ...any) {}})
	s := &server{engine: search.New(cfg)}
	mux := http.NewServeMux()
	s.registerAPIv1(mux)

	get := func(url, token string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		var body map[string]any
		json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}
	if code, _ := get("/api/v1/repos", ""); code != http.StatusUnauthorized {
		t.Errorf("no token: %d", code)
	}
	if code, _ := get("/api/v1/repos", "wrong"); code != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", code)
	}
	code, body := get("/api/v1/search?q=Hello", "s3cret")
	files, _ := body["files"].([]any)
	if code != 200 || len(files) != 1 {
		t.Fatalf("search: %d %v", code, body)
	}
	m := files[0].(map[string]any)["matches"].([]any)[0].(map[string]any)
	if m["line"] != float64(3) || m["text"] != "func Hello() {}" || m["definition"] != true {
		t.Errorf("search match: %v", m)
	}
	if code, body := get("/api/v1/file?repo=r&path=a.go&start=3&end=3", "s3cret"); code != 200 || body["lines"].([]any)[0] != "func Hello() {}" {
		t.Errorf("file: %d %v", code, body)
	}
	if code, body := get("/api/v1/definitions?symbol=Hello", "s3cret"); code != 200 || len(body["definitions"].([]any)) != 1 {
		t.Errorf("definitions: %d %v", code, body)
	}
	if code, body := get("/api/v1/references?symbol=Hello", "s3cret"); code != 200 || body["total"] != float64(1) {
		t.Errorf("references: %d %v", code, body)
	}
	if code, body := get("/api/v1/outline?repo=r&path=a.go", "s3cret"); code != 200 || len(body["symbols"].([]any)) != 2 {
		t.Errorf("outline: %d %v", code, body)
	}
	if code, _ := get("/api/v1/file?repo=r&path=../x", "s3cret"); code != http.StatusBadRequest {
		t.Errorf("path escape: %d", code)
	}
}
