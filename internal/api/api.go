// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package api provides code search as plain data, for the versioned
// JSON API served by csweb (/api/v1/...) and for the MCP server csmcp.
// Unlike the web UI's internal endpoints, results contain plain text,
// not highlighted HTML.
package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ndesai/codesearch/internal/lang"
	"github.com/ndesai/codesearch/internal/outline"
	"github.com/ndesai/codesearch/internal/search"
)

// A Service answers API requests from an engine.
type Service struct {
	Engine *search.Engine
}

// Repo describes an indexed repository.
type Repo = search.RepoInfo

// Repos lists the configured repositories.
func (s *Service) Repos() []Repo {
	var out []Repo
	for _, r := range s.Engine.Repos() {
		out = append(out, r.Info())
	}
	return out
}

// A Match is one matching line.
type Match struct {
	Line   int      `json:"line"`
	Text   string   `json:"text"`
	Ranges [][2]int `json:"ranges,omitempty"` // byte offsets of matches within Text
	Def    bool     `json:"definition,omitempty"`
}

// A FileMatches is a file with matching lines.
type FileMatches struct {
	Repo       string  `json:"repo"`
	Path       string  `json:"path"`
	Lang       string  `json:"language"`
	NumMatches int     `json:"numMatches"`
	Matches    []Match `json:"matches"`
}

// SearchResult is the result of a code search.
type SearchResult struct {
	Query      string         `json:"query"`
	NumFiles   int            `json:"numFiles"`   // files found (before limit/offset)
	NumMatches int            `json:"numMatches"` // matching lines in those files
	Truncated  bool           `json:"truncated"`  // the search stopped early
	Files      []*FileMatches `json:"files"`
}

const (
	DefaultLimit = 20
	MaxLimit     = 200
)

// Search runs a query (the same syntax as the web UI) and returns files
// [offset, offset+limit) in rank order.
func (s *Service) Search(ctx context.Context, query string, limit, offset int) (*SearchResult, error) {
	q, err := search.ParseQuery(query)
	if err != nil {
		return nil, err
	}
	if q.Type != "" {
		return nil, errors.New("type:commit and type:diff are not supported by this API")
	}
	if q.Expr == nil && len(q.Repos) == 0 && len(q.NotRepos) == 0 && len(q.SymFilters) == 0 {
		return nil, errors.New("query has nothing to search for")
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	offset = max(offset, 0)
	res := s.Engine.Search(ctx, q, search.Options{})
	out := &SearchResult{Query: query, NumFiles: res.NumFiles, NumMatches: res.NumMatches, Truncated: res.Truncated, Files: []*FileMatches{}}
	for _, f := range res.Files[min(offset, len(res.Files)):min(offset+limit, len(res.Files))] {
		fm := &FileMatches{Repo: f.Repo, Path: f.Path, Lang: f.Lang, NumMatches: f.NumMatches, Matches: []Match{}}
		for _, l := range f.Lines {
			fm.Matches = append(fm.Matches, Match{Line: l.Line, Text: l.Text, Ranges: l.Ranges, Def: l.Def})
		}
		out.Files = append(out.Files, fm)
	}
	return out, nil
}

// A File is a file's contents (or a directory's entries).
type File struct {
	Repo       string   `json:"repo"`
	Path       string   `json:"path"`
	Kind       string   `json:"kind"` // "file" or "dir"
	Lang       string   `json:"language,omitempty"`
	Size       int64    `json:"size,omitempty"`
	TotalLines int      `json:"totalLines,omitempty"`
	StartLine  int      `json:"startLine,omitempty"` // first line in Lines
	Lines      []string `json:"lines,omitempty"`
	Entries    []Entry  `json:"entries,omitempty"`
	Binary     bool     `json:"binary,omitempty"`
}

// An Entry is a directory entry.
type Entry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size,omitempty"`
}

const (
	maxReadBytes = 20 << 20
	MaxReadLines = 2000
)

// ReadFile returns lines [start, end] (1-based, inclusive; 0 means the
// beginning or end) of a file, at most MaxReadLines, or lists a directory.
func (s *Service) ReadFile(repoName, relPath string, start, end int) (*File, error) {
	repo := s.Engine.Repo(repoName)
	if repo == nil {
		return nil, fmt.Errorf("unknown repository %q", repoName)
	}
	relPath = strings.Trim(relPath, "/")
	abs, err := repo.Abs(relPath)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("%s: not found", relPath)
	}
	out := &File{Repo: repoName, Path: relPath}
	if st.IsDir() {
		ents, err := os.ReadDir(abs)
		if err != nil {
			return nil, err
		}
		out.Kind = "dir"
		out.Entries = []Entry{}
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".") || repo.Excluded(path.Join(relPath, e.Name())) {
				continue
			}
			en := Entry{Name: e.Name(), Dir: e.IsDir()}
			if info, err := e.Info(); err == nil && !e.IsDir() {
				en.Size = info.Size()
			}
			out.Entries = append(out.Entries, en)
		}
		slices.SortFunc(out.Entries, func(a, b Entry) int {
			if a.Dir != b.Dir {
				if a.Dir {
					return -1
				}
				return 1
			}
			return strings.Compare(a.Name, b.Name)
		})
		return out, nil
	}
	out.Kind = "file"
	out.Size = st.Size()
	out.Lang = lang.Detect(relPath).Name
	if st.Size() > maxReadBytes {
		return nil, fmt.Errorf("%s: file too large (%d bytes)", relPath, st.Size())
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(data[:min(len(data), 8000)]) || strings.ContainsRune(string(data[:min(len(data), 8000)]), 0) {
		out.Binary = true
		return out, nil
	}
	lines := strings.Split(strings.TrimSuffix(strings.ToValidUTF8(string(data), "�"), "\n"), "\n")
	out.TotalLines = len(lines)
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	if end-start+1 > MaxReadLines {
		end = start + MaxReadLines - 1
	}
	if start > len(lines) {
		return nil, fmt.Errorf("%s has %d lines", relPath, len(lines))
	}
	out.StartLine = start
	for _, l := range lines[start-1 : end] {
		out.Lines = append(out.Lines, strings.TrimSuffix(l, "\r"))
	}
	return out, nil
}

// A Location is a line in a file.
type Location struct {
	Repo string `json:"repo"`
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
	Kind string `json:"kind,omitempty"`
}

// Definitions returns the definitions of an identifier, nearest to the
// file (repo, path) first if given.
func (s *Service) Definitions(ctx context.Context, sym, repo, path string) ([]Location, error) {
	r, err := s.Engine.Xref(ctx, sym, repo, path)
	if err != nil {
		return nil, err
	}
	out := []Location{}
	for _, d := range r.Defs {
		out = append(out, Location{Repo: d.Repo, Path: d.Path, Line: d.Line, Text: d.Text})
	}
	return out, nil
}

// References returns up to limit references (uses, not definitions) of
// an identifier, nearest first, and the total number found.
func (s *Service) References(ctx context.Context, sym, repo, path string, limit int) ([]Location, int, error) {
	r, err := s.Engine.Xref(ctx, sym, repo, path)
	if err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = 50
	}
	out := []Location{}
	total := 0
	for _, f := range r.Refs {
		for _, l := range f.Lines {
			total++
			if len(out) < limit {
				out = append(out, Location{Repo: f.Repo, Path: f.Path, Line: l.Line, Text: l.Text})
			}
		}
	}
	return out, total, nil
}

// Outline returns the symbols defined in a file.
func (s *Service) Outline(repoName, relPath string) ([]outline.Symbol, error) {
	f, err := s.ReadFile(repoName, relPath, 1, 1<<30)
	if err != nil {
		return nil, err
	}
	if f.Kind != "file" || f.Binary {
		return nil, fmt.Errorf("%s is not a text file", relPath)
	}
	repo := s.Engine.Repo(repoName)
	abs, _ := repo.Abs(relPath)
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	syms := outline.Extract(lang.Detect(relPath).Name, string(data))
	if syms == nil {
		syms = []outline.Symbol{}
	}
	return syms, nil
}
