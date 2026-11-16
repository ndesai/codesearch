// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package indexer

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/codesearch/index"
	"github.com/google/codesearch/internal/config"
	"github.com/google/codesearch/internal/gitignore"
)

// comparePaths orders slash-separated relative paths the same way the
// index orders names: as if '/' sorted before every other byte.
func comparePaths(a, b string) int {
	return index.MakePath(a).Compare(index.MakePath(b))
}

// walk lists the regular files under root that are not excluded,
// returning them sorted by comparePaths. Directories are read in
// parallel, which matters for very large trees on SSDs and network
// file systems. Files larger than maxSize and empty files are omitted.
// Errors reading individual directories are reported to logf and skipped.
// If useGitignore is set, files ignored by .gitignore files (and
// .git/info/exclude) in the tree are skipped too.
func walk(root string, excl *config.Matcher, useGitignore bool, maxSize int64, workers int, counter *atomic.Int64, logf func(string, ...any)) ([]FileInfo, error) {
	if _, err := os.Stat(root); err != nil {
		return nil, err
	}
	if workers < 1 {
		workers = 1
	}

	var (
		mu      sync.Mutex
		cond    = sync.NewCond(&mu)
		queue   = []walkDir{{}} // directories still to read
		pending = 1             // directories queued or being read
		results [][]FileInfo
		wg      sync.WaitGroup
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var local []FileInfo
			defer func() {
				mu.Lock()
				results = append(results, local)
				mu.Unlock()
			}()
			for {
				mu.Lock()
				for len(queue) == 0 && pending > 0 {
					cond.Wait()
				}
				if pending == 0 {
					mu.Unlock()
					cond.Broadcast()
					return
				}
				wd := queue[len(queue)-1]
				queue = queue[:len(queue)-1]
				mu.Unlock()
				dir, ign := wd.rel, wd.ignore

				var subdirs []walkDir
				abs := filepath.Join(root, filepath.FromSlash(dir))
				ents, err := os.ReadDir(abs)
				if err != nil {
					logf("%s: %v", abs, err)
				}
				if useGitignore {
					ign = loadIgnores(abs, dir, ents, ign)
				}
				for _, e := range ents {
					rel := e.Name()
					if dir != "" {
						rel = dir + "/" + rel
					}
					if excl.Excluded(rel) {
						continue
					}
					switch t := e.Type(); {
					case t.IsDir():
						if !ign.Ignored(rel, true) {
							subdirs = append(subdirs, walkDir{rel, ign})
						}
					case t.IsRegular():
						if ign.Ignored(rel, false) {
							continue
						}
						info, err := e.Info()
						if err != nil {
							continue // removed since ReadDir
						}
						if info.Size() == 0 || info.Size() > maxSize {
							continue
						}
						local = append(local, FileInfo{Path: rel, Size: info.Size(), MTime: info.ModTime().UnixNano()})
						if counter != nil {
							counter.Add(1)
						}
					}
					// Symlinks and special files are skipped:
					// following symlinks risks cycles and duplicate content.
				}

				mu.Lock()
				queue = append(queue, subdirs...)
				pending += len(subdirs) - 1
				mu.Unlock()
				cond.Broadcast()
			}
		}()
	}
	wg.Wait()

	var files []FileInfo
	for _, r := range results {
		files = append(files, r...)
	}
	slices.SortFunc(files, func(a, b FileInfo) int { return comparePaths(a.Path, b.Path) })
	return files, nil
}

type walkDir struct {
	rel    string
	ignore gitignore.Stack
}

// loadIgnores adds the ignore files in the directory abs (relative path
// rel) to the stack: .git/info/exclude for a repository root, then
// .gitignore.
func loadIgnores(abs, rel string, ents []os.DirEntry, s gitignore.Stack) gitignore.Stack {
	for _, e := range ents {
		switch {
		case e.Name() == ".git" && e.IsDir():
			if data, err := os.ReadFile(filepath.Join(abs, ".git", "info", "exclude")); err == nil {
				s = s.Push(gitignore.Parse(rel, data))
			}
		case e.Name() == ".gitignore" && e.Type().IsRegular():
			if data, err := os.ReadFile(filepath.Join(abs, ".gitignore")); err == nil {
				s = s.Push(gitignore.Parse(rel, data))
			}
		}
	}
	return s
}

// IgnoreStack returns the ignore files that apply to entries of the
// directory rel (slash-separated, relative to root).
func IgnoreStack(root, rel string) gitignore.Stack {
	var s gitignore.Stack
	dir := ""
	for {
		abs := filepath.Join(root, filepath.FromSlash(dir))
		ents, _ := os.ReadDir(abs)
		s = loadIgnores(abs, dir, ents, s)
		if dir == rel {
			return s
		}
		next, _, _ := strings.Cut(strings.TrimPrefix(rel[len(dir):], "/"), "/")
		if dir != "" {
			next = dir + "/" + next
		}
		dir = next
	}
}
