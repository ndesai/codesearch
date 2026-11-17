// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Csindex builds and incrementally updates the index for the
// repositories listed in a codesearch configuration file.
//
// Usage:
//
//	csindex [-config file] [-full] [-v] [repo...]
//	csindex [-config file] -status
//
// The first run indexes every configured repository. Later runs only
// re-read files whose size or modification time changed, so they are
// fast even for very large trees. With repo names, only those
// repositories are updated. The -full flag rebuilds from scratch.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/ndesai/codesearch/internal/config"
	"github.com/ndesai/codesearch/internal/indexer"
)

var (
	configFlag  = flag.String("config", "", "configuration `file` (default $CODESEARCH_CONFIG or ~/.config/codesearch/config.yaml)")
	fullFlag    = flag.Bool("full", false, "rebuild the index from scratch")
	statusFlag  = flag.Bool("status", false, "print the state of the index and exit")
	verboseFlag = flag.Bool("v", false, "verbose output")
	waitFlag    = flag.Bool("wait", true, "wait for a concurrent indexer (such as csweb) to finish")
)

func usage() {
	fmt.Fprintf(os.Stderr, "usage: csindex [-config file] [-full] [-v] [repo...]\n       csindex [-config file] -status\n")
	flag.PrintDefaults()
	os.Exit(2)
}

func main() {
	log.SetPrefix("csindex: ")
	log.SetFlags(0)
	flag.Usage = usage
	flag.Parse()

	file := *configFlag
	if file == "" {
		file = config.DefaultFile()
	}
	cfg, err := config.Load(file)
	if err != nil {
		log.Fatal(err)
	}

	if *statusFlag {
		printStatus(cfg)
		return
	}

	st := new(indexer.Status)
	done := make(chan struct{})
	if *verboseFlag || isTerminal() {
		go progress(st, done)
	}
	start := time.Now()
	results, err := indexer.Update(cfg, indexer.Options{
		Full:    *fullFlag,
		Repos:   flag.Args(),
		Wait:    *waitFlag,
		Verbose: *verboseFlag,
		Status:  st,
		Logf: func(f string, a ...any) {
			clearLine()
			log.Printf(f, a...)
		},
		OnRepo: func(r *indexer.Result) {
			clearLine()
			fmt.Println(r)
		},
	})
	close(done)
	clearLine()
	if errors.Is(err, indexer.ErrLocked) {
		log.Fatal("another indexer (csindex or csweb) is running; try again later or use -wait")
	}
	if err != nil {
		log.Fatal(err)
	}
	failed := false
	for _, r := range results {
		failed = failed || r.Err != nil
	}
	if *verboseFlag {
		fmt.Printf("done in %.1fs\n", time.Since(start).Seconds())
	}
	if failed {
		os.Exit(1)
	}
}

func printStatus(cfg *config.Config) {
	fmt.Printf("config:    %s\nindex dir: %s\n\n", cfg.File, cfg.IndexDir)
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "REPO\tFILES\tSIZE\tSHARDS\tUPDATED\tPATH\n")
	for _, r := range cfg.Repos {
		m, err := indexer.ReadManifestHeader(indexer.ManifestFile(cfg, r.Name))
		switch {
		case err != nil:
			fmt.Fprintf(w, "%s\terror: %v\t\t\t\t%s\n", r.Name, err, r.Path)
		case m == nil:
			fmt.Fprintf(w, "%s\tnot indexed\t\t\t\t%s\n", r.Name, r.Path)
		default:
			fmt.Fprintf(w, "%s\t%d\t%s\t%d\t%s\t%s\n", r.Name, m.NumFiles(), humanBytes(m.TotalBytes()),
				len(m.Shards), m.Updated.Format("2006-01-02 15:04"), r.Path)
		}
	}
	w.Flush()
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func isTerminal() bool {
	st, err := os.Stderr.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

var (
	outMu           sync.Mutex
	showingProgress bool
)

func clearLine() {
	outMu.Lock()
	defer outMu.Unlock()
	if showingProgress {
		fmt.Fprint(os.Stderr, "\r\033[K")
		showingProgress = false
	}
}

// progress shows a live status line on the terminal.
func progress(st *indexer.Status, done chan struct{}) {
	if !isTerminal() {
		return
	}
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			s := st.Snapshot()
			if !s.Running || s.Repo == "" {
				continue
			}
			line := fmt.Sprintf("%s: scanning (%d files)", s.Repo, s.Walked)
			if s.Phase == "indexing" {
				line = fmt.Sprintf("%s: indexing %d/%d files", s.Repo, s.Indexed, s.ToIndex)
			}
			outMu.Lock()
			fmt.Fprintf(os.Stderr, "\r\033[K%s", line)
			showingProgress = true
			outMu.Unlock()
		}
	}
}
