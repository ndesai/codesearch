// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndesai/codesearch/internal/search"
)

func TestValidRev(t *testing.T) {
	for rev, want := range map[string]bool{
		"main": true, "v1.2.0": true, "release/2.x": true, "HEAD~3": true, "main^": true, "abc123f": true,
		"-x": false, "--output=f": false, "a..b": false, "a b": false, "HEAD:etc/passwd": false, "": false,
		"x.lock": false,
	} {
		if got := validRev(rev); got != want {
			t.Errorf("validRev(%q) = %v, want %v", rev, got, want)
		}
	}
}

func TestRevisionTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	os.MkdirAll(filepath.Join(dir, "pkg"), 0755)
	os.WriteFile(filepath.Join(dir, "pkg", "old.go"), []byte("package pkg\n"), 0644)
	run("add", ".")
	run("commit", "-q", "-m", "one")
	run("tag", "v1")
	os.Remove(filepath.Join(dir, "pkg", "old.go"))
	os.WriteFile(filepath.Join(dir, "pkg", "new.go"), []byte("package pkg\n"), 0644)
	run("add", "-A")
	run("commit", "-q", "-m", "two")

	ctx := context.Background()
	// The old file no longer exists in the work tree but is found at v1.
	top, rel, err := gitFile(ctx, filepath.Join(dir, "pkg", "old.go"))
	if err != nil || rel != "pkg/old.go" {
		t.Fatalf("gitFile = %q, %q, %v", top, rel, err)
	}
	if typ := objectType(ctx, top, "v1", "pkg"); typ != "tree" {
		t.Errorf("objectType(v1:pkg) = %q", typ)
	}
	if typ := objectType(ctx, top, "HEAD", "pkg/old.go"); typ != "" {
		t.Errorf("objectType(HEAD:pkg/old.go) = %q, want none", typ)
	}
	ents, err := lsTree(ctx, top, "v1", "pkg")
	if err != nil || len(ents) != 1 || ents[0].Name != "old.go" || ents[0].Dir || ents[0].Size != 12 {
		t.Errorf("lsTree(v1, pkg) = %+v, %v", ents, err)
	}
	if _, rel, _ := gitFile(ctx, dir); rel != "" {
		t.Errorf("gitFile(root) rel = %q, want empty", rel)
	}
}

func TestAnchoredPrefix(t *testing.T) {
	for expr, want := range map[string]string{
		"(?i)^codesearch/":         "codesearch/",
		`^frameworks/base/core\.`:  "frameworks/base/core.",
		"^third_party/(skia|dawn)": "third_party/",
		"^ab*c":                    "a",
		`(?i)\.go$`:                "!",
	} {
		got, ok := anchoredPrefix(expr)
		if !ok {
			got = "!"
		}
		if got != want {
			t.Errorf("anchoredPrefix(%q) = %q, want %q", expr, got, want)
		}
	}
}

func TestMatchCommitNoFiles(t *testing.T) {
	q, err := search.ParseQuery("type:commit out-of-bounds")
	if err != nil {
		t.Fatal(err)
	}
	c := &commitMatch{Subject: "Merge: fix out-of-bounds read"}
	if !matchCommit(c, "", "", q) {
		t.Fatal("matchCommit = false")
	}
	if b, _ := json.Marshal(c); !strings.Contains(string(b), `"files":[]`) {
		t.Errorf("JSON = %s, want an empty files list", b)
	}
}
