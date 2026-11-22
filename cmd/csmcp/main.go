// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Csmcp is a Model Context Protocol (MCP) server that lets AI agents
// search and read the repositories indexed by csindex or csweb.
//
// Usage:
//
//	csmcp [-config file] [-index_dir dir]            # stdio transport
//	csmcp [-config file] -http localhost:2474        # HTTP transport
//
// With stdio, an MCP client starts csmcp as a subprocess and exchanges
// JSON-RPC messages, one per line, on its standard input and output.
// With -http, clients POST JSON-RPC messages to /mcp. csmcp reads the
// index directly; csweb does not need to be running, but something
// (csweb or csindex) must keep the index up to date.
package main

import (
	"bufio"
	"context"
	"crypto/subtle"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ndesai/codesearch/internal/api"
	"github.com/ndesai/codesearch/internal/config"
	"github.com/ndesai/codesearch/internal/search"
)

var (
	configFlag   = flag.String("config", "", "configuration `file` (default $CODESEARCH_CONFIG or ~/.config/codesearch/config.yaml)")
	indexDirFlag = flag.String("index_dir", "", "read the index from `dir` (overrides $CODESEARCH_INDEX_DIR and the config file)")
	httpFlag     = flag.String("http", "", "serve MCP over HTTP at `address` (POST /mcp) instead of stdio")
)

func main() {
	log.SetPrefix("csmcp: ")
	log.SetFlags(0)
	log.SetOutput(os.Stderr) // stdout carries the protocol
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: csmcp [-config file] [-index_dir dir] [-http addr]\n")
		flag.PrintDefaults()
		os.Exit(2)
	}
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
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
	engine := search.New(cfg)
	go engine.Watch(5*time.Second, nil)
	svc := &api.Service{Engine: engine}

	if *httpFlag != "" {
		serveHTTP(svc, *httpFlag, cfg.APIToken)
		return
	}
	serveStdio(svc, os.Stdin, os.Stdout)
}

// serveStdio handles newline-delimited JSON-RPC messages. Requests run
// concurrently; responses are written whole, one per line.
func serveStdio(svc *api.Service, in io.Reader, out io.Writer) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		msg := append([]byte(nil), sc.Bytes()...)
		if len(strings.TrimSpace(string(msg))) == 0 {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if resp := svc.HandleMCP(context.Background(), msg); resp != nil {
				mu.Lock()
				out.Write(append(resp, '\n'))
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
}

func serveHTTP(svc *api.Service, addr, token string) {
	host, _, _ := net.SplitHostPort(addr)
	loopback := host == "localhost" || strings.HasPrefix(host, "127.") || host == "::1"
	http.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if loopback && !isLoopbackHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "missing or wrong API token", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "POST JSON-RPC messages to this endpoint", http.StatusMethodNotAllowed)
			return
		}
		msg, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp := svc.HandleMCP(r.Context(), msg)
		if resp == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(resp)
	})
	log.Printf("serving MCP at http://%s/mcp", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func isLoopbackHost(h string) bool {
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	return h == "localhost" || strings.HasPrefix(h, "127.") || h == "::1"
}
