// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

// Git integration: blame, file history, commit diffs, and viewing a
// file at an older revision. Files may live in nested repositories
// (as in AOSP), so every operation starts by finding the git work tree
// that contains the file.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ndesai/codesearch/internal/lang"
)

var (
	hashRE      = regexp.MustCompile(`^[0-9a-f]{4,64}$`)
	errNoGit    = errors.New("git is not installed")
	errNotInGit = errors.New("this file is not in a git repository")
)

const gitTimeout = 30 * time.Second

// gitFile locates abs within its git work tree, returning the work
// tree root and the file's slash-separated path relative to it.
func gitFile(ctx context.Context, abs string) (top, rel string, err error) {
	dir := abs
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		dir = filepath.Dir(abs)
	}
	out, err := runGit(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", errNotInGit
	}
	top = strings.TrimSpace(string(out))
	realTop, err1 := filepath.EvalSymlinks(top)
	realAbs, err2 := filepath.EvalSymlinks(abs)
	if err1 != nil || err2 != nil {
		realTop, realAbs = top, abs
	}
	r, err := filepath.Rel(realTop, realAbs)
	if err != nil || strings.HasPrefix(r, "..") {
		return "", "", errNotInGit
	}
	return top, filepath.ToSlash(r), nil
}

func runGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, errNoGit
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.quotepath=off", "--no-pager"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, errors.New("git timed out")
		}
		msg := strings.TrimSpace(stderr.String())
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	return out, nil
}

// fileArgs resolves the repo and path parameters of a request
// to the file's git work tree and path within it.
func (s *server) fileArgs(w http.ResponseWriter, r *http.Request) (top, rel string, ok bool) {
	repo := s.engine.Repo(r.FormValue("repo"))
	if repo == nil {
		writeError(w, http.StatusNotFound, errors.New("unknown repository"))
		return "", "", false
	}
	abs, err := repo.Abs(r.FormValue("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return "", "", false
	}
	top, rel, err = gitFile(r.Context(), abs)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return "", "", false
	}
	return top, rel, true
}

// revArg returns the validated rev parameter ("" for the work tree).
func revArg(r *http.Request) (string, error) {
	rev := r.FormValue("rev")
	if rev != "" && !hashRE.MatchString(rev) {
		return "", errors.New("invalid revision")
	}
	return rev, nil
}

type blameCommit struct {
	Hash    string `json:"hash"`
	Author  string `json:"author"`
	Time    int64  `json:"time"`
	Summary string `json:"summary"`
}

// handleBlame returns, for each line of the file, the index of the
// commit that last changed it.
func (s *server) handleBlame(w http.ResponseWriter, r *http.Request) {
	top, rel, ok := s.fileArgs(w, r)
	if !ok {
		return
	}
	rev, err := revArg(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	args := []string{"blame", "--porcelain"}
	if rev != "" {
		args = append(args, rev)
	}
	out, err := runGit(r.Context(), top, append(args, "--", rel)...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var (
		commits []blameCommit
		index   = map[string]int{}
		lines   []int
		cur     = -1
	)
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "\t"):
			// Content line: belongs to the current commit.
			lines = append(lines, cur)
		case len(line) >= 40 && hashRE.MatchString(strings.SplitN(line, " ", 2)[0]):
			h := line[:strings.IndexByte(line+" ", ' ')]
			i, ok := index[h]
			if !ok {
				i = len(commits)
				index[h] = i
				commits = append(commits, blameCommit{Hash: h})
			}
			cur = i
		case cur >= 0:
			k, v, _ := strings.Cut(line, " ")
			c := &commits[cur]
			switch k {
			case "author":
				c.Author = v
			case "author-time":
				c.Time, _ = strconv.ParseInt(v, 10, 64)
			case "summary":
				c.Summary = v
			}
		}
	}
	for i := range commits {
		if strings.Trim(commits[i].Hash, "0") == "" {
			commits[i].Author = "Not committed yet"
			commits[i].Summary = "Local changes"
			commits[i].Hash = ""
		}
	}
	writeJSON(w, map[string]any{"commits": commits, "lines": lines})
}

type historyEntry struct {
	Hash    string `json:"hash"`
	Author  string `json:"author"`
	Email   string `json:"email"`
	Time    int64  `json:"time"`
	Subject string `json:"subject"`
	Path    string `json:"path"` // file's path at this commit (it may have been renamed)
}

// handleHistory lists the commits that changed the file, following renames.
func (s *server) handleHistory(w http.ResponseWriter, r *http.Request) {
	top, rel, ok := s.fileArgs(w, r)
	if !ok {
		return
	}
	n, _ := strconv.Atoi(r.FormValue("n"))
	if n <= 0 || n > 1000 {
		n = 200
	}
	out, err := runGit(r.Context(), top, "log", "--follow", "--name-only",
		"--format=%x1e%H%x1f%an%x1f%ae%x1f%at%x1f%s", "-n", strconv.Itoa(n), "--", rel)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var hist []historyEntry
	for _, rec := range strings.Split(string(out), "\x1e") {
		header, files, _ := strings.Cut(rec, "\n")
		f := strings.Split(header, "\x1f")
		if len(f) != 5 {
			continue
		}
		t, _ := strconv.ParseInt(f[3], 10, 64)
		e := historyEntry{Hash: f[0], Author: f[1], Email: f[2], Time: t, Subject: f[4], Path: rel}
		for _, p := range strings.Split(files, "\n") {
			if p = strings.TrimSpace(p); p != "" {
				e.Path = p
				break
			}
		}
		hist = append(hist, e)
	}
	writeJSON(w, map[string]any{"commits": hist, "path": rel})
}

type diffLine struct {
	Kind string `json:"kind"` // "+", "-", " ", "@" (hunk header) or "f" (file header)
	Old  int    `json:"old,omitempty"`
	New  int    `json:"new,omitempty"`
	HTML string `json:"html"`
}

var hunkRE = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// handleCommit shows a commit's metadata and its diff for the file
// (or, with all=1, for every file it changed).
func (s *server) handleCommit(w http.ResponseWriter, r *http.Request) {
	top, rel, ok := s.fileArgs(w, r)
	if !ok {
		return
	}
	hash := r.FormValue("hash")
	if !hashRE.MatchString(hash) {
		writeError(w, http.StatusBadRequest, errors.New("invalid commit"))
		return
	}
	meta, err := runGit(r.Context(), top, "show", "-s", "--format=%H%x1f%an%x1f%ae%x1f%at%x1f%P%x1f%B", hash)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	f := strings.SplitN(strings.TrimRight(string(meta), "\n"), "\x1f", 6)
	if len(f) != 6 {
		writeError(w, http.StatusInternalServerError, errors.New("unexpected git output"))
		return
	}
	t, _ := strconv.ParseInt(f[3], 10, 64)

	args := []string{"show", "--format=", "--no-color", "--no-ext-diff", "-M", "-U3", hash}
	if r.FormValue("all") == "" {
		// The file's path at this commit, from the history view.
		p := r.FormValue("at")
		if p == "" || strings.HasPrefix(p, "-") || strings.Contains(p, "..") {
			p = rel
		}
		args = append(args, "--", p)
	}
	out, err := runGit(r.Context(), top, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	const maxDiffLines = 20000
	var lines []diffLine
	lx := lang.Detect(rel).Lexer
	oldN, newN := 0, 0
	truncated := false
	for _, l := range strings.Split(string(out), "\n") {
		if len(lines) >= maxDiffLines {
			truncated = true
			break
		}
		switch {
		case strings.HasPrefix(l, "diff --git "):
			name := l[strings.LastIndex(l, " b/")+3:]
			lines = append(lines, diffLine{Kind: "f", HTML: htmlEscape(name)})
			lx = lang.Detect(name).Lexer
		case strings.HasPrefix(l, "index "), strings.HasPrefix(l, "--- "), strings.HasPrefix(l, "+++ "),
			strings.HasPrefix(l, "new file mode"), strings.HasPrefix(l, "deleted file mode"),
			strings.HasPrefix(l, "similarity index"), strings.HasPrefix(l, "rename from"), strings.HasPrefix(l, "rename to"),
			strings.HasPrefix(l, "old mode"), strings.HasPrefix(l, "new mode"):
			if strings.HasPrefix(l, "rename") || strings.HasPrefix(l, "new file") || strings.HasPrefix(l, "deleted file") {
				lines = append(lines, diffLine{Kind: "i", HTML: htmlEscape(l)})
			}
		case strings.HasPrefix(l, "Binary files"):
			lines = append(lines, diffLine{Kind: "i", HTML: htmlEscape(l)})
		case strings.HasPrefix(l, "@@"):
			if m := hunkRE.FindStringSubmatch(l); m != nil {
				oldN, _ = strconv.Atoi(m[1])
				newN, _ = strconv.Atoi(m[2])
			}
			lines = append(lines, diffLine{Kind: "@", HTML: htmlEscape(l)})
		case strings.HasPrefix(l, "+"):
			lines = append(lines, diffLine{Kind: "+", New: newN, HTML: highlightSnippet(lx, l[1:], nil)})
			newN++
		case strings.HasPrefix(l, "-"):
			lines = append(lines, diffLine{Kind: "-", Old: oldN, HTML: highlightSnippet(lx, l[1:], nil)})
			oldN++
		case strings.HasPrefix(l, " "):
			lines = append(lines, diffLine{Kind: " ", Old: oldN, New: newN, HTML: highlightSnippet(lx, l[1:], nil)})
			oldN++
			newN++
		}
	}
	parents := strings.Fields(f[4])
	writeJSON(w, map[string]any{
		"hash": f[0], "author": f[1], "email": f[2], "time": t, "parents": parents,
		"message": strings.TrimSpace(f[5]), "diff": lines, "truncated": truncated,
	})
}

// readAtRev returns the contents of the file at revision rev. If at
// is set, it is the file's path within the work tree at that revision
// (which differs from the current path if the file was renamed).
func readAtRev(ctx context.Context, abs, rev, at string) ([]byte, error) {
	top, rel, err := gitFile(ctx, abs)
	if err != nil {
		return nil, err
	}
	if at != "" && !strings.HasPrefix(at, "-") && !strings.Contains(at, "..") && !strings.Contains(at, ":") {
		rel = at
	}
	out, err := runGit(ctx, top, "show", fmt.Sprintf("%s:%s", rev, rel))
	if err != nil {
		return nil, fmt.Errorf("file not found at %s", rev[:min(len(rev), 12)])
	}
	return out, nil
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;").Replace(s)
}
