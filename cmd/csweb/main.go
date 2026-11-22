// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Csweb serves ND Code Search, a web interface for searching the
// repositories listed in a codesearch configuration file.
//
// Usage:
//
//	csweb [-config file] [-index_dir dir] [-listen addr] [-noindex]
//
// Csweb reads the configuration (by default ~/.config/codesearch/config.yaml),
// serves the search UI, and keeps the index up to date in the background
// every update_interval. The index can also be updated separately with
// csindex; csweb notices new index data within a few seconds.
package main

import (
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ndesai/codesearch/internal/config"
	"github.com/ndesai/codesearch/internal/indexer"
	"github.com/ndesai/codesearch/internal/search"
)

var (
	configFlag   = flag.String("config", "", "configuration `file` (default $CODESEARCH_CONFIG or ~/.config/codesearch/config.yaml)")
	indexDirFlag = flag.String("index_dir", "", "store the index in `dir` (overrides $CODESEARCH_INDEX_DIR and the config file)")
	listenFlag   = flag.String("listen", "", "listen `address` (overrides the config file)")
	noIndexFlag  = flag.Bool("noindex", false, "do not update the index in the background")
	verboseFlag  = flag.Bool("verbose", false, "log details of index updates")
)

//go:embed static
var staticFS embed.FS

func usage() {
	fmt.Fprintf(os.Stderr, "usage: csweb [-config file] [-index_dir dir] [-listen addr] [-noindex]\n")
	flag.PrintDefaults()
	os.Exit(2)
}

func main() {
	log.SetPrefix("csweb: ")
	log.SetFlags(log.Ldate | log.Ltime)
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() != 0 {
		usage()
	}

	file := *configFlag
	if file == "" {
		file = config.DefaultFile()
	}
	cfg, err := config.Load(file)
	if err != nil {
		log.Fatal(err)
	}
	if *indexDirFlag != "" {
		if err := cfg.SetIndexDir(*indexDirFlag); err != nil {
			log.Fatalf("-index_dir: %v", err)
		}
	}
	addr := cfg.Listen
	if *listenFlag != "" {
		addr = *listenFlag
	}

	engine := search.New(cfg)
	go engine.Watch(3*time.Second, nil)

	s := &server{
		engine: engine,
		status: new(indexer.Status),
		cache:  newResultCache(32, 2*time.Minute),
	}
	var running atomic.Bool
	s.reindex = func() bool {
		if !running.CompareAndSwap(false, true) {
			return false
		}
		go func() {
			defer running.Store(false)
			runUpdate(cfg, s)
		}()
		return true
	}
	if !*noIndexFlag {
		go func() {
			s.reindex()
			if cfg.UpdateInterval <= 0 {
				return
			}
			for range time.Tick(time.Duration(cfg.UpdateInterval)) {
				s.reindex()
			}
		}()
	}

	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	files := http.FileServer(http.FS(static))
	mux.Handle("/static/", http.StripPrefix("/static/", cacheStatic(files)))
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/reindex", s.handleReindex)
	mux.HandleFunc("/api/search", s.handleSearch)
	mux.HandleFunc("/api/suggest", s.handleSuggest)
	mux.HandleFunc("/api/xref", s.handleXref)
	mux.HandleFunc("/api/hover", s.handleHover)
	mux.HandleFunc("/api/callers", s.handleCallers)
	mux.HandleFunc("/api/callees", s.handleCallees)
	mux.HandleFunc("/api/file", s.handleFile)
	mux.HandleFunc("/api/blame", s.handleBlame)
	mux.HandleFunc("/api/history", s.handleHistory)
	mux.HandleFunc("/api/commit", s.handleCommit)
	mux.HandleFunc("/api/refs", s.handleRefs)
	mux.HandleFunc("/api/context", s.handleContext)
	mux.HandleFunc("/api/saved", s.handleSaved)
	mux.HandleFunc("/raw/", s.handleRaw)
	mux.HandleFunc("/preview/", s.handlePreview)
	index, err := fs.ReadFile(static, "index.html")
	if err != nil {
		log.Fatal(err)
	}
	app := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(index)
	}
	mux.HandleFunc("/{$}", app)
	mux.HandleFunc("/search", app)
	mux.HandleFunc("/f/", app)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("serving %d repositories at http://%s/ (index in %s)", len(cfg.Repos), displayAddr(ln.Addr().String()), cfg.IndexDir)
	srv := &http.Server{
		Handler:           secure(addr, mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(srv.Serve(ln))
}

// runUpdate updates the index and reloads the search engine.
func runUpdate(cfg *config.Config, s *server) {
	_, err := indexer.Update(cfg, indexer.Options{
		Status:  s.status,
		Verbose: *verboseFlag,
		OnRepo: func(r *indexer.Result) {
			if r.Err != nil || r.Changed() || *verboseFlag {
				log.Print(r)
			}
			if r.Changed() {
				s.engine.Refresh()
				s.cache.clear()
			}
		},
	})
	if errors.Is(err, indexer.ErrLocked) {
		log.Printf("index update skipped: %v", err)
		return
	}
	if err != nil {
		log.Printf("index update: %v", err)
	}
}

func cacheStatic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}

func displayAddr(a string) string {
	host, port, err := net.SplitHostPort(a)
	if err != nil {
		return a
	}
	if host == "127.0.0.1" || host == "::1" || host == "" || host == "::" || host == "0.0.0.0" {
		host = "localhost"
	}
	return net.JoinHostPort(host, port)
}

// secure adds security headers and, when listening on a loopback
// address, rejects requests whose Host header is not a loopback name.
// That blocks DNS rebinding attacks, in which a malicious web page
// re-points its own domain at 127.0.0.1 to read the user's source code.
func secure(listen string, h http.Handler) http.Handler {
	host, _, _ := net.SplitHostPort(listen)
	loopback := host == "localhost" || strings.HasPrefix(host, "127.") || host == "::1"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if loopback {
			hh, _, err := net.SplitHostPort(r.Host)
			if err != nil {
				hh = r.Host
			}
			hh = strings.Trim(hh, "[]")
			if hh != "localhost" && !strings.HasPrefix(hh, "127.") && hh != "::1" {
				http.Error(w, "forbidden host", http.StatusForbidden)
				return
			}
		}
		if r.Method == http.MethodPost && r.Header.Get("X-Requested-With") == "" {
			// Simple cross-site form posts can't set custom headers.
			http.Error(w, "missing X-Requested-With header", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		h.ServeHTTP(w, r)
	})
}
