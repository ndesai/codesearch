// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"container/list"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/google/codesearch/internal/gitignore"
	"github.com/google/codesearch/internal/indexer"
	"github.com/google/codesearch/internal/lang"
	"github.com/google/codesearch/internal/outline"
	"github.com/google/codesearch/internal/search"
)

type server struct {
	engine  *search.Engine
	status  *indexer.Status
	reindex func() bool // starts a background index update

	cache *resultCache
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		log.Printf("writing response: %v", err)
	}
}

func writeError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// --- status ---

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	var repos []search.RepoInfo
	for _, repo := range s.engine.Repos() {
		repos = append(repos, repo.Info())
	}
	writeJSON(w, map[string]any{
		"repos":    repos,
		"indexing": s.status.Snapshot(),
		"config":   s.engine.Config().File,
		"indexDir": s.engine.Config().IndexDir,
	})
}

func (s *server) handleReindex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("POST required"))
		return
	}
	writeJSON(w, map[string]bool{"started": s.reindex()})
}

// --- search ---

type apiLine struct {
	Line int    `json:"line"`
	HTML string `json:"html"`
	Def  bool   `json:"def,omitempty"`
}

type apiFile struct {
	Repo       string    `json:"repo"`
	Path       string    `json:"path"`
	Lang       string    `json:"lang"`
	NumMatches int       `json:"numMatches"`
	Lines      []apiLine `json:"lines"`
}

const maxLinesShown = 30

func renderFiles(files []*search.FileMatch, maxLines int) []apiFile {
	out := make([]apiFile, 0, len(files))
	for _, f := range files {
		af := apiFile{Repo: f.Repo, Path: f.Path, Lang: f.Lang, NumMatches: f.NumMatches}
		lx := lang.Detect(f.Path).Lexer
		for i, l := range f.Lines {
			if i >= maxLines {
				break
			}
			af.Lines = append(af.Lines, apiLine{Line: l.Line, HTML: highlightSnippet(lx, l.Text, l.Ranges), Def: l.Def})
		}
		out = append(out, af)
	}
	return out
}

func (s *server) handleSearch(w http.ResponseWriter, r *http.Request) {
	qs := strings.TrimSpace(r.FormValue("q"))
	if qs == "" {
		writeError(w, http.StatusBadRequest, errors.New("empty query"))
		return
	}
	page, _ := strconv.Atoi(r.FormValue("page"))
	size, _ := strconv.Atoi(r.FormValue("n"))
	if size <= 0 || size > 200 {
		size = 25
	}

	res, ok := s.cache.get(qs)
	if !ok {
		q, err := search.ParseQuery(qs)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if q.Expr == nil && len(q.Repos) == 0 && len(q.NotRepos) == 0 {
			writeError(w, http.StatusBadRequest, errors.New("query has nothing to search for"))
			return
		}
		res = s.engine.Search(r.Context(), q, search.Options{})
		if r.Context().Err() != nil {
			return
		}
		s.cache.put(qs, res)
	}

	lo := min(page*size, len(res.Files))
	hi := min(lo+size, len(res.Files))
	var names []search.NameMatch
	if page == 0 {
		for _, n := range res.NameMatches {
			names = append(names, *n)
		}
	}
	writeJSON(w, map[string]any{
		"query":          res.Query,
		"numFiles":       res.NumFiles,
		"numMatches":     res.NumMatches,
		"truncated":      res.Truncated,
		"timedOut":       res.TimedOut,
		"elapsed":        res.Elapsed,
		"page":           page,
		"pageSize":       size,
		"more":           hi < len(res.Files),
		"files":          renderFiles(res.Files[lo:hi], maxLinesShown),
		"nameMatches":    names,
		"numNameMatches": res.NumNames,
		"facets":         res.Facets,
	})
}

func (s *server) handleSuggest(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"files": s.engine.Suggest(r.Context(), r.FormValue("q"), 10)})
}

// --- cross references ---

func (s *server) handleXref(w http.ResponseWriter, r *http.Request) {
	sym := r.FormValue("sym")
	res, err := s.engine.Xref(r.Context(), sym, r.FormValue("repo"), r.FormValue("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	type apiDef struct {
		Repo string `json:"repo"`
		Path string `json:"path"`
		Line int    `json:"line"`
		HTML string `json:"html"`
	}
	var defs []apiDef
	for _, d := range res.Defs {
		defs = append(defs, apiDef{d.Repo, d.Path, d.Line, highlightSnippet(lang.Detect(d.Path).Lexer, d.Text, d.Ranges)})
	}
	writeJSON(w, map[string]any{
		"symbol":    res.Symbol,
		"defs":      defs,
		"refs":      renderFiles(res.Refs, 20),
		"numRefs":   res.NumRefs,
		"numFiles":  res.NumFiles,
		"truncated": res.Truncated,
		"elapsed":   res.Elapsed,
	})
}

// --- files and directories ---

const maxViewBytes = 20 << 20

type dirEntry struct {
	Name     string `json:"name"`
	Dir      bool   `json:"dir"`
	Size     int64  `json:"size,omitempty"`
	Excluded bool   `json:"excluded,omitempty"`
}

func (s *server) handleFile(w http.ResponseWriter, r *http.Request) {
	repo := s.engine.Repo(r.FormValue("repo"))
	if repo == nil {
		writeError(w, http.StatusNotFound, errors.New("unknown repository"))
		return
	}
	rel := strings.Trim(r.FormValue("path"), "/")
	abs, err := repo.Abs(rel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	rev, err := revArg(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	info, err := os.Stat(abs)
	if err != nil && rev == "" {
		writeError(w, http.StatusNotFound, errors.New("file not found"))
		return
	}
	if rev == "" && info.IsDir() {
		ents, err := os.ReadDir(abs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		cfg := s.engine.Config()
		var ign gitignore.Stack
		if cfg.Gitignore() {
			ign = indexer.IgnoreStack(repo.Root, rel)
		}
		var out []dirEntry
		for _, e := range ents {
			name := e.Name()
			if !cfg.IncludeHidden && strings.HasPrefix(name, ".") {
				continue
			}
			child := path.Join(rel, name)
			de := dirEntry{Name: name, Dir: e.IsDir(), Excluded: repo.Excluded(child) || ign.Ignored(child, e.IsDir())}
			if e.Type()&os.ModeSymlink != 0 {
				if st, err := os.Stat(filepath.Join(abs, name)); err == nil {
					de.Dir = st.IsDir()
				}
			}
			if !de.Dir {
				if fi, err := e.Info(); err == nil {
					de.Size = fi.Size()
				}
			}
			out = append(out, de)
		}
		slices.SortFunc(out, func(a, b dirEntry) int {
			if a.Dir != b.Dir {
				if a.Dir {
					return -1
				}
				return 1
			}
			return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		})
		resp := map[string]any{"kind": "dir", "repo": repo.Name, "path": rel, "entries": out}
		if r.FormValue("readme") != "" {
			for _, e := range out {
				if !e.Dir && strings.HasPrefix(strings.ToLower(e.Name), "readme") && e.Size < 256<<10 {
					if data, err := os.ReadFile(filepath.Join(abs, e.Name)); err == nil && utf8.Valid(data) {
						resp["readme"] = map[string]any{"name": e.Name, "lines": renderSource(path.Join(rel, e.Name), string(data), nil)}
					}
					break
				}
			}
		}
		writeJSON(w, resp)
		return
	}

	langName := lang.Detect(rel).Name
	resp := map[string]any{"kind": "file", "repo": repo.Name, "path": rel, "lang": langName}
	if _, _, err := gitFile(r.Context(), abs); err == nil {
		resp["git"] = true
	}
	var data []byte
	if rev != "" {
		// An older version, from the history view.
		data, err = readAtRev(r.Context(), abs, rev, r.FormValue("at"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		resp["rev"] = rev
		resp["size"] = len(data)
	} else {
		resp["size"] = info.Size()
		resp["modified"] = info.ModTime()
		if info.Size() > maxViewBytes {
			resp["tooLarge"] = true
			writeJSON(w, resp)
			return
		}
		data, err = os.ReadFile(abs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	if isImage(rel) {
		resp["image"] = true
		writeJSON(w, resp)
		return
	}
	if !isText(data) {
		resp["binary"] = true
		writeJSON(w, resp)
		return
	}
	src := string(data)
	if !utf8.ValidString(src) {
		src = strings.ToValidUTF8(src, "�")
	}
	var terms []*search.Term
	if qs := r.FormValue("q"); qs != "" {
		if q, err := search.ParseQuery(qs); err == nil {
			terms = q.Terms
		}
	}
	resp["lines"] = renderSource(rel, src, terms)
	if len(src) <= maxHighlightBytes {
		resp["outline"] = outline.Extract(langName, src)
	}
	writeJSON(w, resp)
}

// renderSource highlights src and marks matches of terms.
func renderSource(rel, src string, terms []*search.Term) []string {
	var lx chroma.Lexer
	if len(src) <= maxHighlightBytes {
		lx = lexerFor(lang.Detect(rel).Lexer, src)
	}
	lines := tokenizeLines(lx, src)
	out := make([]string, len(lines))
	for i, segs := range lines {
		var ranges [][2]int
		if len(terms) > 0 {
			var text strings.Builder
			for _, s := range segs {
				text.WriteString(s.text)
			}
			line := text.String()
			for _, t := range terms {
				for _, m := range t.Std.FindAllStringIndex(line, 100) {
					if m[1] > m[0] {
						ranges = append(ranges, [2]int{m[0], m[1]})
					}
				}
			}
			slices.SortFunc(ranges, func(a, b [2]int) int { return a[0] - b[0] })
		}
		out[i] = renderLine(segs, ranges)
	}
	return out
}

var imageTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".bmp": "image/bmp", ".ico": "image/x-icon",
}

func isImage(name string) bool {
	_, ok := imageTypes[strings.ToLower(path.Ext(name))]
	return ok
}

// isText reports whether data looks like text: no NULs and mostly
// valid UTF-8 in its first few kilobytes.
func isText(data []byte) bool {
	head := data[:min(len(data), 8000)]
	if bytes.IndexByte(head, 0) >= 0 {
		return false
	}
	bad := 0
	for len(head) > 0 {
		r, n := utf8.DecodeRune(head)
		if r == utf8.RuneError && n == 1 && len(head) >= utf8.UTFMax {
			bad++
		}
		head = head[n:]
	}
	return bad < 8
}

// handleRaw serves a file's raw bytes. Text is always served as
// text/plain so that repository content can never run as HTML or script
// in the app's origin; only common image formats get their real type.
func (s *server) handleRaw(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/raw/")
	name, rel, _ := strings.Cut(rest, "/")
	repo := s.engine.Repo(name)
	if repo == nil {
		http.NotFound(w, r)
		return
	}
	abs, err := repo.Abs(rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	ctype := "text/plain; charset=utf-8"
	if t, ok := imageTypes[strings.ToLower(path.Ext(rel))]; ok {
		ctype = t
	} else if r.FormValue("download") != "" {
		ctype = "application/octet-stream"
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(rel)}))
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox")
	http.ServeContent(w, r, "", st.ModTime(), struct{ io.ReadSeeker }{f})
}

// --- result cache ---

// resultCache keeps recent search results so paging is instant.
// Entries expire quickly so that index updates become visible.
type resultCache struct {
	mu  sync.Mutex
	ll  *list.List
	m   map[string]*list.Element
	max int
	ttl time.Duration
}

type cacheEntry struct {
	key string
	res *search.Result
	at  time.Time
}

func newResultCache(max int, ttl time.Duration) *resultCache {
	return &resultCache{ll: list.New(), m: map[string]*list.Element{}, max: max, ttl: ttl}
}

func (c *resultCache) get(key string) (*search.Result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok {
		return nil, false
	}
	ent := e.Value.(*cacheEntry)
	if time.Since(ent.at) > c.ttl {
		c.ll.Remove(e)
		delete(c.m, key)
		return nil, false
	}
	c.ll.MoveToFront(e)
	return ent.res, true
}

func (c *resultCache) put(key string, res *search.Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[key]; ok {
		c.ll.Remove(e)
	}
	c.m[key] = c.ll.PushFront(&cacheEntry{key, res, time.Now()})
	for c.ll.Len() > c.max {
		e := c.ll.Back()
		c.ll.Remove(e)
		delete(c.m, e.Value.(*cacheEntry).key)
	}
}

func (c *resultCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	clear(c.m)
}

// handlePreview serves repository files with their real content types so
// that HTML files (and the CSS, scripts and images they reference by
// relative URL) can be previewed in the browser.
//
// The content is untrusted, so every response carries a CSP sandbox
// without allow-same-origin: the page runs in an opaque origin, and its
// scripts cannot read the app's API or anything else on this server.
// Framing is limited to the app itself.
func (s *server) handlePreview(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/preview/")
	name, rel, _ := strings.Cut(rest, "/")
	repo := s.engine.Repo(name)
	if repo == nil {
		http.NotFound(w, r)
		return
	}
	abs, err := repo.Abs(rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	ctype := mime.TypeByExtension(strings.ToLower(path.Ext(rel)))
	if ctype == "" {
		ctype = "text/plain; charset=utf-8"
	}
	h := w.Header()
	h.Del("X-Frame-Options")
	h.Set("Content-Type", ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-popups allow-modals; frame-ancestors 'self'")
	h.Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "", st.ModTime(), struct{ io.ReadSeeker }{f})
}
