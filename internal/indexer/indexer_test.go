// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package indexer

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndesai/codesearch/index"
	"github.com/ndesai/codesearch/internal/config"
	"github.com/ndesai/codesearch/internal/symbols"
)

func testConfig(t *testing.T, root string, shardFiles int) *config.Config {
	t.Helper()
	t.Setenv(config.IndexDirEnv, "") // don't let the environment redirect test indexes
	yaml := fmt.Sprintf("index_dir: %s\nshard_files: %d\nparallelism: 3\nrepos:\n  - path: %s\n    name: test\n    exclude: [skipme, /top_only]\n",
		filepath.Join(t.TempDir(), "idx"), shardFiles, root)
	cfg, err := config.Parse(filepath.Join(root, "cfg.yaml"), []byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	// Make sure the mtime changes even on coarse-grained file systems.
	future := time.Now().Add(time.Duration(rand.Intn(1000)+1) * time.Second)
	os.Chtimes(p, future, future)
}

func update(t *testing.T, cfg *config.Config, full bool) *Result {
	t.Helper()
	res, err := Update(cfg, Options{Full: full, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Err != nil {
		t.Fatalf("update: %v", res)
	}
	return res[0]
}

// contents returns, for every shard, the file names and for each
// sampled trigram the matching file names: a canonical view of the index.
func contents(t *testing.T, cfg *config.Config) (names []string, posts map[string][]string, syms []string) {
	t.Helper()
	m, err := ReadManifest(ManifestFile(cfg, "test"))
	if err != nil || m == nil {
		t.Fatalf("manifest: %v", err)
	}
	posts = map[string][]string{}
	for _, f := range m.ShardPaths(RepoDir(cfg, "test")) {
		ix, err := index.OpenFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := ix.Check(); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		var local []string
		for p := range ix.Names(0, ix.NumNames()) {
			local = append(local, p.String())
		}
		names = append(names, local...)
		for _, tri := range []string{"foo", "bar", "baz", "new", "zzz", "abc"} {
			tv := uint32(tri[0])<<16 | uint32(tri[1])<<8 | uint32(tri[2])
			for _, id := range ix.PostingList(tv) {
				posts[tri] = append(posts[tri], ix.Name(id).String())
			}
		}
		ix.Close()
		tab, err := symbols.Open(filepath.Join(RepoDir(cfg, "test"), SymbolFile(filepath.Base(f))))
		if err != nil {
			t.Fatal(err)
		}
		for _, sf := range tab.Files() {
			for _, sym := range sf.Symbols {
				syms = append(syms, fmt.Sprintf("%s:%s:%d:%s", sf.Path, sym.Name, sym.Line, sym.Kind))
			}
		}
		tab.Close()
	}
	return
}

func TestIncremental(t *testing.T) {
	root := t.TempDir()
	words := []string{"foo", "bar", "baz", "abc"}
	for i := range 40 {
		write(t, root, fmt.Sprintf("d%d/f%02d.go", i%4, i), fmt.Sprintf("package x // %s %d\n\nfunc F%d() {}\n", words[i%4], i, i))
	}
	write(t, root, "skipme/a.go", "foo\n")
	write(t, root, "top_only/a.go", "foo\n")
	write(t, root, "d1/top_only/a.go", "foo bar\n") // only the top-level one is excluded
	write(t, root, ".hidden/a.go", "foo\n")
	write(t, root, "bin.dat2", "foo\x00bar")
	write(t, root, ".gitignore", "/generated/\n*.tmp\n")
	write(t, root, "generated/x.go", "foo\n")
	write(t, root, "d2/scratch.tmp", "foo\n")
	write(t, root, "d2/sub/.gitignore", "!keep.tmp\n")
	write(t, root, "d2/sub/keep.tmp", "foo\n")

	cfg := testConfig(t, root, 10)
	r := update(t, cfg, false)
	if !r.Full || r.Shards != 5 || r.Files != 43 {
		t.Fatalf("first update: %v", r)
	}
	names, _, syms := contents(t, cfg)
	if !slices.Contains(syms, "d0/f00.go:F0:3:func") || len(syms) != 40 {
		t.Fatalf("symbols after first update: %d %v", len(syms), syms[:min(3, len(syms))])
	}
	if len(names) != 42 { // binary file not indexed
		t.Fatalf("indexed %d names, want 42", len(names))
	}
	if !slices.Contains(names, filepath.Join(root, "d2/sub/keep.tmp")) {
		t.Errorf("file re-included by nested .gitignore not indexed")
	}
	for _, n := range names {
		if strings.Contains(n, "skipme") || strings.Contains(n, ".hidden") || strings.Contains(n, root+"/top_only") ||
			strings.Contains(n, "/generated/") || strings.HasSuffix(n, "scratch.tmp") {
			t.Errorf("excluded file indexed: %s", n)
		}
	}

	// No changes: nothing rebuilt.
	r = update(t, cfg, false)
	if r.Changed() || r.ShardsKept != 5 {
		t.Fatalf("no-op update: %v", r)
	}

	check := func(what string) {
		t.Helper()
		gotNames, gotPosts, gotSyms := contents(t, cfg)
		// Rebuild from scratch in a second index dir and compare.
		cfg2 := *cfg
		cfg2.IndexDir = filepath.Join(t.TempDir(), "idx2")
		update(t, &cfg2, true)
		wantNames, wantPosts, wantSyms := contents(t, &cfg2)
		slices.Sort(gotSyms)
		slices.Sort(wantSyms)
		if !slices.Equal(gotSyms, wantSyms) {
			t.Fatalf("%s: symbols differ:\nincremental: %v\nfull:        %v", what, gotSyms, wantSyms)
		}
		if !slices.Equal(gotNames, wantNames) {
			t.Fatalf("%s: names differ:\nincremental: %v\nfull:        %v", what, gotNames, wantNames)
		}
		for tri, want := range wantPosts {
			if !slices.Equal(gotPosts[tri], want) {
				t.Fatalf("%s: posting list %q differs:\nincremental: %v\nfull:        %v", what, tri, gotPosts[tri], want)
			}
		}
		for tri := range gotPosts {
			if _, ok := wantPosts[tri]; !ok {
				t.Fatalf("%s: unexpected posting list %q", what, tri)
			}
		}
	}

	// Modify, add, and delete a few files.
	write(t, root, "d0/f00.go", "package x // new zzz\n\nfunc Renamed() {}\n")
	write(t, root, "d2/added.go", "package x // new\n")
	write(t, root, "d0/aa_first.go", "package x // zzz\n") // before every existing path
	os.Remove(filepath.Join(root, "d3/f07.go"))
	r = update(t, cfg, false)
	if r.Full || r.Added != 2 || r.Modified != 1 || r.Deleted != 1 || r.ShardsDelta == 0 || r.ShardsKept == 0 {
		t.Fatalf("incremental update: %v", r)
	}
	slices.Sort(r.ChangedFiles)
	if strings.Join(r.ChangedFiles, " ") != "d0/aa_first.go d0/f00.go d2/added.go" {
		t.Errorf("changed files: %v", r.ChangedFiles)
	}
	if m, _ := ReadManifestHeader(ManifestFile(cfg, "test")); len(m.Changed) != 3 {
		t.Errorf("manifest Changed: %v", m.Changed)
	}
	check("modify/add/delete")

	// A file replaced by a directory of the same name.
	os.Remove(filepath.Join(root, "d1/f01.go"))
	write(t, root, "d1/f01.go/inner.go", "package x // new foo\n")
	update(t, cfg, false)
	check("file to dir")

	// Delete an entire shard's worth of files.
	os.RemoveAll(filepath.Join(root, "d0"))
	r = update(t, cfg, false)
	check("delete shard")

	// Grow one range enough to force a rebuild and split.
	for i := range 30 {
		write(t, root, fmt.Sprintf("d2/grow/g%02d.go", i), "package x // bar new\n")
	}
	r = update(t, cfg, false)
	if r.ShardsRebuilt == 0 {
		t.Fatalf("expected a rebuild: %v", r)
	}
	check("split")

	// Changing the exclude list forces a full rebuild.
	cfg.Repos[0].Excludes, _ = config.NewMatcher([]string{"d2"}, false)
	r = update(t, cfg, false)
	if !r.Full {
		t.Fatalf("expected full rebuild after config change: %v", r)
	}
	names, _, _ = contents(t, cfg)
	for _, n := range names {
		if strings.Contains(n, "/d2/") {
			t.Fatalf("excluded file still indexed: %s", n)
		}
	}

	// Delete everything.
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		os.RemoveAll(filepath.Join(root, e.Name()))
	}
	r = update(t, cfg, false)
	if r.Files != 0 {
		t.Fatalf("empty repo: %v", r)
	}
	names, _, _ = contents(t, cfg)
	if len(names) != 0 {
		t.Fatalf("empty repo still has names %v", names)
	}
}

func TestManifestRoundTrip(t *testing.T) {
	m := &Manifest{
		Root: "/src", Fingerprint: "fp", Generation: 7, Updated: time.Unix(0, 12345),
		Shards:  []Shard{{Start: "", File: "a.idx", Files: 2, Bytes: 10}, {Start: "b/c", File: "b.idx", Files: 1, Bytes: 5}},
		Changed: []string{"a/b.go"},
		Files:   []FileInfo{{"a/b.go", 1, 100}, {"a/bc.go", 2, -5}, {"b/c", 3, 1 << 60}},
	}
	f := filepath.Join(t.TempDir(), "m")
	if err := m.Write(f); err != nil {
		t.Fatal(err)
	}
	m2, err := ReadManifest(f)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(m) != fmt.Sprint(m2) {
		t.Fatalf("round trip:\n%v\n%v", m, m2)
	}
	h, err := ReadManifestHeader(f)
	if err != nil || h.NumFiles() != 3 || len(h.Files) != 0 || len(h.Changed) != 1 {
		t.Fatalf("header: %v %v", h, err)
	}
	// Version 1 manifests (without Changed) still read.
	m.Changed = nil
	m.Write(f)
	m1, err := encodeV1ForTest(m)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(f, m1, 0644)
	if back, err := ReadManifest(f); err != nil || fmt.Sprint(back.Files) != fmt.Sprint(m.Files) || back.Changed != nil {
		t.Fatalf("v1 manifest: %v %v", back, err)
	}
	// Truncation is detected.
	data, _ := os.ReadFile(f)
	os.WriteFile(f, data[:len(data)-3], 0644)
	if _, err := ReadManifest(f); err == nil {
		t.Fatal("truncated manifest not detected")
	}
}

func TestComparePathsMatchesIndexOrder(t *testing.T) {
	paths := []string{"a/b", "a.c", "a", "a/b/c", "a-b", "ab", "a/a"}
	slices.SortFunc(paths, comparePaths)
	want := []string{"a", "a/a", "a/b", "a/b/c", "a-b", "a.c", "ab"}
	if !slices.Equal(paths, want) {
		t.Fatalf("got %v want %v", paths, want)
	}
}

func TestIgnoreStack(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".gitignore", "*.o\n/out/\n")
	write(t, root, "a/b/.gitignore", "gen/\n!keep.o\n")
	write(t, root, ".git/info/exclude", "secret.txt\n")
	s := IgnoreStack(root, "a/b")
	for path, want := range map[string]bool{"a/b/x.o": true, "a/b/keep.o": false, "a/b/secret.txt": true, "a/b/c.go": false} {
		if got := s.Ignored(path, false); got != want {
			t.Errorf("Ignored(%s) = %v, want %v", path, got, want)
		}
	}
	if !s.Ignored("a/b/gen", true) || IgnoreStack(root, "").Ignored("a/out", true) || !IgnoreStack(root, "").Ignored("out", true) {
		t.Error("directory rules")
	}
}

// encodeV1ForTest encodes m in the version 1 format (no Changed list),
// as older versions of the indexer wrote it.
func encodeV1ForTest(m *Manifest) ([]byte, error) {
	f := filepath.Join(os.TempDir(), fmt.Sprintf("v1-%d", time.Now().UnixNano()))
	defer os.Remove(f)
	if err := m.Write(f); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(f)
	if err != nil {
		return nil, err
	}
	// The v2 header is the v1 header plus a varint count (0) after the
	// shards; find it by re-parsing the shard section.
	r := bufio.NewReader(bytes.NewReader(data[len(manifestMagic):]))
	readN := func() int { v, _ := binary.ReadUvarint(r); return int(v) }
	readI := func() { binary.ReadVarint(r) }
	readS := func() { n := readN(); r.Discard(n) }
	readS()
	readS()
	readN()
	readI()
	for range readN() {
		readS()
		readS()
		readN()
		readI()
	}
	pos := len(data) - len(manifestMagic) - r.Buffered()
	pos += len(manifestMagic)
	out := append([]byte(manifestMagicV1), data[len(manifestMagic):pos]...)
	return append(out, data[pos+1:]...), nil // skip the zero count
}
