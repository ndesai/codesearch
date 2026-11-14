// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !unix

package indexer

import (
	"os"
	"time"
)

// lockFile acquires an exclusive lock by creating file exclusively.
// A lock older than a day is assumed to be stale.
func lockFile(file string, wait bool) (unlock func(), err error) {
	for {
		f, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0644)
		if err == nil {
			f.Close()
			return func() { os.Remove(file) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if st, err := os.Stat(file); err == nil && time.Since(st.ModTime()) > 24*time.Hour {
			os.Remove(file)
			continue
		}
		if !wait {
			return nil, errLocked
		}
		time.Sleep(time.Second)
	}
}
