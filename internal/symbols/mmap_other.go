// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !unix

package symbols

import (
	"io"
	"os"
)

// mapFile reads the file into memory where mmap is not available.
func mapFile(f *os.File, size int) ([]byte, func() error, error) {
	data := make([]byte, size)
	if _, err := io.ReadFull(f, data); err != nil {
		return nil, nil, err
	}
	return data, func() error { return nil }, nil
}
