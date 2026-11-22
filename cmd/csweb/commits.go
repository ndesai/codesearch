// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

// Searching git history: type:commit searches commit messages and
// type:diff the lines commits added or removed, in every git repository
// inside the configured folders (including nested ones, as in AOSP).
//
// git's own pattern matching uses POSIX regular expressions, while
// queries use Go's RE2 syntax, so git is only given the longest literal
// in the pattern as a prefilter; every message and diff line it returns
// is then checked with the query's own regular expressions.

import (
	"bufio"
	"context"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ndesai/codesearch/internal/lang"
	"github.com/ndesai/codesearch/internal/search"
)

const (
	maxCommitsPerRepo = 30
	maxCommits        = 200
	maxGitRoots       = 2000
	maxRootDepth      = 4
	maxDiffFiles      = 10 // per commit, shown
	maxDiffLines      = 12 // per file, shown
	historyTimeout    = 20 * time.Second
)

type commitLine struct {
	Kind string `json:"kind"` // "+" or "-"
	Line int    `json:"line"`
	HTML string `json:"html"`
}

type commitFile struct {
	Path  string       `json:"path"` // relative to the configured repository
	Lines []commitLine `json:"lines,omitempty"`
}

type commitMatch struct {
	Repo     string       `json:"repo"`
	Root     string       `json:"root"` // git work tree, relative to the configured repository
	Hash     string       `json:"hash"`
	Author   string       `json:"author"`
	Email    string       `json:"email"`
	Time     int64        `json:"time"`
	Subject  string       `json:"subject"`
	Body     string       `json:"body,omitempty"`
	Files    []commitFile `json:"files"`
	NumFiles int          `json:"numFiles"`
}

// --- finding git repositories ---

type rootsEntry struct {
	at    time.Time
	roots []string
}

var (
	rootsMu    sync.Mutex
	rootsCache = map[string]rootsEntry{}
)

// gitRoots returns the git work trees inside a configured repository,
// as slash-separated paths relative to its root ("" for the root
// itself). An AOSP-style .repo/project.list is used when present;
// otherwise directories are scanned a few levels deep.
func gitRoots(repo *search.Repo) []string {
	rootsMu.Lock()
	if e, ok := rootsCache[repo.Name]; ok && time.Since(e.at) < 10*time.Minute {
		rootsMu.Unlock()
		return e.roots
	}
	rootsMu.Unlock()

	isRepo := func(rel string) bool {
		_, err := os.Stat(filepath.Join(repo.Root, filepath.FromSlash(rel), ".git"))
		return err == nil
	}
	var roots []string
	if data, err := os.ReadFile(filepath.Join(repo.Root, ".repo", "project.list")); err == nil {
		sc := bufio.NewScanner(strings.NewReader(string(data)))
		for sc.Scan() && len(roots) < maxGitRoots {
			if p := strings.TrimSpace(sc.Text()); p != "" && !strings.Contains(p, "..") && isRepo(p) {
				roots = append(roots, p)
			}
		}
	} else {
		level := []string{""}
		for depth := 0; depth <= maxRootDepth && len(level) > 0 && len(roots) < maxGitRoots; depth++ {
			var next []string
			for _, dir := range level {
				if isRepo(dir) {
					roots = append(roots, dir)
				}
				ents, _ := os.ReadDir(filepath.Join(repo.Root, filepath.FromSlash(dir)))
				for _, e := range ents {
					if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
						continue
					}
					rel := path.Join(dir, e.Name())
					if !repo.Excluded(rel) {
						next = append(next, rel)
					}
				}
			}
			level = next
		}
	}
	rootsMu.Lock()
	rootsCache[repo.Name] = rootsEntry{time.Now(), roots}
	rootsMu.Unlock()
	return roots
}

// --- running the search ---

var ereSpecial = regexp.MustCompile(`[.\[\]{}()\\*+?|^$]`)

func ereEscape(s string) string { return ereSpecial.ReplaceAllString(s, `\$0`) }

func (s *server) handleHistorySearch(w http.ResponseWriter, r *http.Request, q *search.Query) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), historyTimeout)
	defer cancel()

	// The prefilter for git: the longest literal of the first term.
	var lit string
	var fold bool
	if len(q.Terms) > 0 {
		lit, fold = q.Terms[0].RequiredLiteral()
		if lit == "" {
			writeError(w, http.StatusBadRequest, errorString("the first search term needs some literal text for history search"))
			return
		}
	}

	type job struct {
		repo *search.Repo
		root string
	}
	var jobs []job
	for _, repo := range s.engine.Repos() {
		if !repoSelected(q, repo.Name) {
			continue
		}
		for _, root := range gitRoots(repo) {
			if rootCanMatch(q, root) {
				jobs = append(jobs, job{repo, root})
			}
		}
	}

	var (
		mu      sync.Mutex
		results []*commitMatch
		wg      sync.WaitGroup
		sem     = make(chan struct{}, min(16, max(4, runtime.NumCPU())))
		partial bool
	)
	for _, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				mu.Lock()
				partial = true
				mu.Unlock()
				return
			}
			found, err := searchHistory(ctx, j.repo, j.root, q, lit, fold)
			mu.Lock()
			defer mu.Unlock()
			// Other failures (such as a repository without commits)
			// just mean no results there.
			if err != nil && (ctx.Err() != nil || err.Error() == "git timed out") {
				partial = true
			}
			results = append(results, found...)
		}()
	}
	wg.Wait()

	slices.SortFunc(results, func(a, b *commitMatch) int {
		if a.Time != b.Time {
			if a.Time > b.Time {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Hash, b.Hash)
	})
	truncated := false
	if len(results) > maxCommits {
		results, truncated = results[:maxCommits], true
	}
	if results == nil {
		results = []*commitMatch{}
	}
	writeJSON(w, map[string]any{
		"type": "commits", "kind": q.Type, "query": q.Raw, "commits": results, "numCommits": len(results),
		"repos": len(jobs), "truncated": truncated, "timedOut": partial || ctx.Err() != nil,
		"elapsed": time.Since(start).Seconds(),
	})
}

// rootCanMatch reports whether files in the git work tree root (a path
// relative to the configured repository) could pass the query's anchored
// file: filters (such as file:^frameworks/base/), so that work trees
// they rule out are not searched at all.
func rootCanMatch(q *search.Query, root string) bool {
	dir := strings.ToLower(root + "/")
	if root == "" {
		return true
	}
	for _, re := range q.Files {
		prefix, ok := anchoredPrefix(re.String())
		if !ok {
			continue
		}
		p := strings.ToLower(prefix)
		if !strings.HasPrefix(p, dir) && !strings.HasPrefix(dir, p) {
			return false
		}
	}
	return true
}

// pathspecs returns the folders, relative to the git work tree root,
// that the query's anchored file: filters limit the search to.
func pathspecs(q *search.Query, root string) []string {
	var specs []string
	for _, re := range q.Files {
		prefix, ok := anchoredPrefix(re.String())
		if !ok {
			continue
		}
		dir := prefix[:strings.LastIndex(prefix, "/")+1] // whole folders only
		if root != "" {
			if !strings.HasPrefix(strings.ToLower(dir), strings.ToLower(root+"/")) {
				continue
			}
			dir = dir[len(root)+1:]
		}
		dir = strings.TrimSuffix(dir, "/")
		if dir == "" || strings.HasPrefix(dir, "-") || strings.ContainsAny(dir, "*?[:") {
			continue
		}
		// One filter's folder is enough: all filters must hold.
		return append(specs, ":(icase)"+dir)
	}
	return specs
}

// anchoredPrefix returns the literal text a ^-anchored path regexp
// starts with.
func anchoredPrefix(expr string) (string, bool) {
	expr = strings.TrimPrefix(expr, "(?i)")
	if !strings.HasPrefix(expr, "^") {
		return "", false
	}
	var b strings.Builder
	for i := 1; i < len(expr); i++ {
		c := expr[i]
		if c == '\\' && i+1 < len(expr) && strings.IndexByte(".-/_", expr[i+1]) >= 0 {
			b.WriteByte(expr[i+1])
			i++
			continue
		}
		if strings.IndexByte(`.[]{}()\*+?|^$`, c) >= 0 {
			// A quantifier applies to the previous character, which
			// then isn't certain.
			if strings.IndexByte("*?{", c) >= 0 && b.Len() > 0 {
				s := b.String()
				return s[:len(s)-1], true
			}
			break
		}
		b.WriteByte(c)
	}
	return b.String(), true
}

type errorString string

func (e errorString) Error() string { return string(e) }

func repoSelected(q *search.Query, name string) bool {
	for _, re := range q.Repos {
		if !re.MatchString(name) {
			return false
		}
	}
	for _, re := range q.NotRepos {
		if re.MatchString(name) {
			return false
		}
	}
	return true
}

const (
	recSep   = "\x1e"
	fieldSep = "\x1f"
)

// searchHistory runs git log in one work tree and checks its results.
func searchHistory(ctx context.Context, repo *search.Repo, root string, q *search.Query, lit string, fold bool) ([]*commitMatch, error) {
	dir := filepath.Join(repo.Root, filepath.FromSlash(root))
	args := []string{"log", "--no-color", "--no-ext-diff", "-n", strconv.Itoa(maxCommitsPerRepo),
		"--format=" + recSep + "%H" + fieldSep + "%an" + fieldSep + "%ae" + fieldSep + "%at" + fieldSep + "%s" + fieldSep + "%b" + fieldSep}
	if fold {
		args = append(args, "-i")
	}
	if q.Author != "" {
		args = append(args, "--author="+q.Author)
	}
	if q.Before != "" {
		args = append(args, "--before="+q.Before)
	}
	if q.After != "" {
		args = append(args, "--after="+q.After)
	}
	if q.Type == "diff" {
		args = append(args, "-G"+ereEscape(lit), "-p", "--unified=0")
	} else {
		if lit != "" {
			args = append(args, "--grep="+ereEscape(lit))
		}
		args = append(args, "--name-only")
	}
	// Limit git to the folders that anchored file: filters allow.
	if spec := pathspecs(q, root); len(spec) > 0 {
		args = append(append(args, "--"), spec...)
	}
	out, err := runGit(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	prefix := ""
	if root != "" {
		prefix = root + "/"
	}
	var found []*commitMatch
	for _, rec := range strings.Split(string(out), recSep) {
		f := strings.SplitN(rec, fieldSep, 7)
		if len(f) != 7 {
			continue
		}
		t, _ := strconv.ParseInt(f[3], 10, 64)
		c := &commitMatch{Repo: repo.Name, Root: root, Hash: f[0], Author: f[1], Email: f[2], Time: t,
			Subject: f[4], Body: strings.TrimSpace(f[5])}
		var ok bool
		if q.Type == "diff" {
			ok = matchDiff(c, f[6], prefix, q)
		} else {
			ok = matchCommit(c, f[6], prefix, q)
		}
		if ok {
			found = append(found, c)
		}
	}
	return found, nil
}

// matchCommit checks a commit message (and the changed files, for path
// filters) against the query.
func matchCommit(c *commitMatch, names, prefix string, q *search.Query) bool {
	msg := c.Subject + "\n" + c.Body
	has := func(i int) bool { return q.AllTerms[i].Std.MatchString(msg) }
	var files []string
	for _, n := range strings.Split(strings.TrimSpace(names), "\n") {
		if n = strings.TrimSpace(n); n != "" {
			files = append(files, prefix+n)
		}
	}
	c.NumFiles = len(files)
	c.Files = []commitFile{} // never null in JSON: merges change no files
	matched := false
	if len(files) == 0 {
		matched = q.Match("", has, nil)
	}
	for _, f := range files {
		if q.Match(f, has, nil) {
			matched = true
			if len(c.Files) < maxDiffFiles {
				c.Files = append(c.Files, commitFile{Path: f})
			}
		}
	}
	return matched
}

var hunkHeaderRE = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// matchDiff keeps the files and changed lines of a patch that match the
// query; the commit matches if any file does.
func matchDiff(c *commitMatch, patch, prefix string, q *search.Query) bool {
	type change struct {
		kind string
		line int
		text string
	}
	type file struct {
		path    string
		changes []change
	}
	var files []*file
	var cur *file
	oldN, newN := 0, 0
	for _, l := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			cur = &file{path: prefix + l[strings.LastIndex(l, " b/")+3:]}
			files = append(files, cur)
		case strings.HasPrefix(l, "@@"):
			if m := hunkHeaderRE.FindStringSubmatch(l); m != nil {
				oldN, _ = strconv.Atoi(m[1])
				newN, _ = strconv.Atoi(m[2])
			}
		case cur == nil || strings.HasPrefix(l, "+++ ") || strings.HasPrefix(l, "--- "):
		case strings.HasPrefix(l, "+"):
			cur.changes = append(cur.changes, change{"+", newN, l[1:]})
			newN++
		case strings.HasPrefix(l, "-"):
			cur.changes = append(cur.changes, change{"-", oldN, l[1:]})
			oldN++
		}
	}
	c.NumFiles = len(files)
	for _, f := range files {
		has := func(i int) bool {
			for _, ch := range f.changes {
				if q.AllTerms[i].Std.MatchString(ch.text) {
					return true
				}
			}
			return false
		}
		if !q.Match(f.path, has, nil) {
			continue
		}
		cf := commitFile{Path: f.path}
		lx := lang.Detect(f.path).Lexer
		for _, ch := range f.changes {
			var ranges [][2]int
			for _, t := range q.Terms {
				for _, m := range t.Std.FindAllStringIndex(ch.text, 20) {
					if m[1] > m[0] {
						ranges = append(ranges, [2]int{m[0], m[1]})
					}
				}
			}
			if len(ranges) == 0 {
				continue
			}
			if len(cf.Lines) < maxDiffLines {
				slices.SortFunc(ranges, func(a, b [2]int) int { return a[0] - b[0] })
				text := ch.text
				if len(text) > 500 {
					text, ranges = text[:500], nil
				}
				cf.Lines = append(cf.Lines, commitLine{Kind: ch.kind, Line: ch.line, HTML: highlightSnippet(lx, text, ranges)})
			}
		}
		if len(cf.Lines) > 0 && len(c.Files) < maxDiffFiles {
			c.Files = append(c.Files, cf)
		}
	}
	return len(c.Files) > 0
}
