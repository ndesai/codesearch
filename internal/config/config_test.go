// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Parse(filepath.Join(dir, "c.yaml"), []byte(`
index_dir: idx
update_interval: 15m
max_file_size: 512KB
repos:
  - path: src/proj
  - name: aosp
    path: /abs/aosp
    exclude: [/out]
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IndexDir != filepath.Join(dir, "idx") {
		t.Errorf("index_dir = %s", cfg.IndexDir)
	}
	if time.Duration(cfg.UpdateInterval) != 15*time.Minute || cfg.MaxFileSize != 512<<10 {
		t.Errorf("interval %v size %d", cfg.UpdateInterval, cfg.MaxFileSize)
	}
	if r := cfg.Repos[0]; r.Name != "proj" || r.Path != filepath.Join(dir, "src/proj") {
		t.Errorf("repo 0: %+v", r)
	}
	aosp := cfg.Repo("aosp")
	if aosp == nil || !aosp.Excludes.Excluded("out") || aosp.Excludes.Excluded("frameworks/out") {
		t.Error("/out should exclude only the top-level out directory")
	}
	if cfg.Fingerprint(cfg.Repos[0]) == cfg.Fingerprint(aosp) {
		t.Error("fingerprints should differ")
	}

	for _, bad := range []string{
		"repos: []",
		"repos: [{name: x}]",
		"repos: [{path: /a, name: x}, {path: /b, name: x}]",
		"repos: [{path: /a, name: 'bad name'}]",
		"repos: [{path: /a}]\nupdate_interval: soon",
		"repos: [{path: /a, exclude: ['[']}]",
	} {
		if _, err := Parse("c.yaml", []byte(bad)); err == nil {
			t.Errorf("Parse(%q) succeeded", bad)
		}
	}
}

func TestMatcher(t *testing.T) {
	m, err := NewMatcher([]string{"node_modules", "*.min.js", "/out", "docs/gen", "vendor/*/testdata"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{
		"node_modules":            true,
		"a/b/node_modules":        true,
		"a/node_modules_x":        false,
		"x/app.min.js":            true,
		"x/app.js":                false,
		"out":                     true,
		"out/x.c":                 true,
		"src/out":                 false,
		"docs/gen":                true,
		"docs/generated":          false,
		"vendor/foo/testdata":     true,
		"vendor/foo/src":          false,
		".git":                    true,
		"a/.hidden":               true,
		"a/file~":                 true,
		"a/#autosave#":            true,
		"frameworks/base/core.cc": false,
	} {
		if got := m.Excluded(path); got != want {
			t.Errorf("Excluded(%q) = %v, want %v", path, got, want)
		}
	}
	m, _ = NewMatcher(nil, true)
	if m.Excluded(".github") {
		t.Error("include_hidden should allow dot files")
	}
}

func TestParseSize(t *testing.T) {
	for s, want := range map[string]int64{"100": 100, "4KB": 4096, "2MB": 2 << 20, "1.5MiB": 3 << 19, "1g": 1 << 30} {
		got, err := ParseSize(s)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", s, got, err, want)
		}
	}
	if _, err := ParseSize("big"); err == nil || !strings.Contains(err.Error(), "bad size") {
		t.Errorf("ParseSize(big) err = %v", err)
	}
}
