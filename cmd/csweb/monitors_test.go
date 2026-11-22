// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndesai/codesearch/internal/config"
	"github.com/ndesai/codesearch/internal/indexer"
	"github.com/ndesai/codesearch/internal/search"
)

func TestMonitors(t *testing.T) {
	t.Setenv(config.IndexDirEnv, "")
	root := t.TempDir()
	write := func(name, content string) {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0755)
		os.WriteFile(p, []byte(content), 0644)
		future := time.Now().Add(time.Hour)
		os.Chtimes(p, future, future)
	}
	write("a.go", "package a // nothing to see\n")

	hooks := make(chan map[string]any, 4)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		hooks <- body
	}))
	defer hook.Close()

	yaml := fmt.Sprintf("index_dir: %s\nmonitor_webhook: %s\nrepos:\n  - path: %s\n    name: r\n", filepath.Join(t.TempDir(), "idx"), hook.URL, root)
	cfg, err := config.Parse(filepath.Join(root, "c.yaml"), []byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	update := func() {
		if _, err := indexer.Update(cfg, indexer.Options{Logf: func(string, ...any) {}}); err != nil {
			t.Fatal(err)
		}
	}
	update()
	engine := search.New(cfg)
	s := &server{engine: engine, status: new(indexer.Status), cache: newResultCache(4, time.Minute)}
	done := make(chan bool, 4)
	engine.OnUpdate = func(repo string, changed []string) { s.runMonitors(repo, changed); done <- true }

	post := func(body string) {
		req := httptest.NewRequest(http.MethodPost, "/api/saved", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.handleSaved(w, req)
		if w.Code != 200 {
			b, _ := io.ReadAll(w.Body)
			t.Fatalf("%s: %d %s", body, w.Code, b)
		}
	}
	post(`{"action":"add","query":"dangerousCall","name":"Dangerous"}`)
	post(`{"action":"monitor","query":"dangerousCall","on":true}`)

	// A new file with a match produces exactly one alert.
	write("b.go", "package a\n\nfunc f() { dangerousCall() }\n")
	update()
	engine.Refresh()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("monitors did not run")
	}
	saved := s.loadSaved()
	if len(saved) != 1 || saved[0].Unseen != 1 || len(saved[0].Alerts) != 1 || saved[0].Alerts[0].Path != "b.go" || saved[0].Alerts[0].Lines[0].Line != 3 {
		t.Fatalf("after update: %+v", saved)
	}
	select {
	case body := <-hooks:
		if body["search"] != "Dangerous" || body["text"] != "Dangerous: 1 new match" {
			t.Errorf("webhook body: %v", body)
		}
	case <-time.After(10 * time.Second):
		t.Error("webhook not called")
	}

	// An unrelated change adds nothing; marking seen clears the count.
	write("a.go", "package a // still nothing\n")
	update()
	engine.Refresh()
	<-done
	post(`{"action":"seen","query":"dangerousCall"}`)
	saved = s.loadSaved()
	if saved[0].Unseen != 0 || len(saved[0].Alerts) != 1 || !saved[0].Monitor {
		t.Fatalf("after second update: %+v", saved)
	}
	// Saving again keeps the monitor and its alerts.
	post(`{"action":"add","query":"dangerousCall","name":"Renamed"}`)
	if saved = s.loadSaved(); !saved[0].Monitor || len(saved[0].Alerts) != 1 || saved[0].Name != "Renamed" {
		t.Fatalf("after re-saving: %+v", saved)
	}
}
