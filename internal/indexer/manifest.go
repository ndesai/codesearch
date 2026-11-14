// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package indexer

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// A Manifest records the state of a repository's index: every file that
// was considered for indexing (with its size and modification time, so the
// next run can find what changed) and the shards that hold the index.
//
// File paths are slash-separated and relative to the repository root,
// sorted in index.Path order. Shard i holds the files whose paths sort
// at or after Shards[i].Start and before Shards[i+1].Start.
type Manifest struct {
	Root        string
	Fingerprint string
	Generation  int
	Updated     time.Time
	Shards      []Shard
	Files       []FileInfo
}

// A Shard is one index file covering a contiguous range of paths.
type Shard struct {
	Start string // first path in range ("" for the first shard)
	File  string // index file name, relative to the repo's index directory
	Files int    // number of files listed in the manifest for this range
	Bytes int64  // total size of those files
}

// FileInfo is the metadata used to detect changed files.
type FileInfo struct {
	Path  string // slash-separated, relative to the root
	Size  int64
	MTime int64 // modification time in nanoseconds
}

const (
	manifestMagic   = "codesearch manifest 1\n"
	manifestTrailer = "\nend manifest\n"
)

// ReadManifest reads the manifest from file.
// It returns (nil, nil) if the file does not exist.
func ReadManifest(file string) (*Manifest, error) {
	return readManifest(file, false)
}

// ReadManifestHeader is like ReadManifest but skips the file list,
// returning only the repository metadata and shard list.
func ReadManifestHeader(file string) (*Manifest, error) {
	return readManifest(file, true)
}

func readManifest(file string, headerOnly bool) (*Manifest, error) {
	f, err := os.Open(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	m, err := decodeManifest(bufio.NewReaderSize(f, 1<<16), headerOnly)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", file, err)
	}
	return m, nil
}

var errCorrupt = errors.New("corrupt manifest")

func decodeManifest(r *bufio.Reader, headerOnly bool) (m *Manifest, err error) {
	defer func() {
		if e := recover(); e != nil {
			m, err = nil, errCorrupt
		}
	}()
	readN := func() int {
		v, err := binary.ReadUvarint(r)
		if err != nil || v > 1<<40 {
			panic(errCorrupt)
		}
		return int(v)
	}
	readI := func() int64 {
		v, err := binary.ReadVarint(r)
		if err != nil {
			panic(errCorrupt)
		}
		return v
	}
	readS := func() string {
		n := readN()
		b := make([]byte, n)
		if _, err := io.ReadFull(r, b); err != nil {
			panic(errCorrupt)
		}
		return string(b)
	}
	magic := make([]byte, len(manifestMagic))
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != manifestMagic {
		return nil, errCorrupt
	}
	m = new(Manifest)
	m.Root = readS()
	m.Fingerprint = readS()
	m.Generation = readN()
	m.Updated = time.Unix(0, readI())
	m.Shards = make([]Shard, readN())
	for i := range m.Shards {
		s := &m.Shards[i]
		s.Start = readS()
		s.File = readS()
		s.Files = readN()
		s.Bytes = readI()
	}
	if headerOnly {
		return m, nil
	}
	m.Files = make([]FileInfo, readN())
	var last []byte
	for i := range m.Files {
		f := &m.Files[i]
		prefix := readN()
		suffix := readS()
		if prefix > len(last) {
			return nil, errCorrupt
		}
		last = append(last[:prefix], suffix...)
		f.Path = string(last)
		f.Size = readI()
		f.MTime = readI()
	}
	trailer := make([]byte, len(manifestTrailer))
	if _, err := io.ReadFull(r, trailer); err != nil || string(trailer) != manifestTrailer {
		return nil, errCorrupt
	}
	return m, nil
}

// Write writes the manifest to file atomically.
func (m *Manifest) Write(file string) error {
	tmp := file + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	var buf [binary.MaxVarintLen64]byte
	writeN := func(v int) { w.Write(buf[:binary.PutUvarint(buf[:], uint64(v))]) }
	writeI := func(v int64) { w.Write(buf[:binary.PutVarint(buf[:], v)]) }
	writeS := func(s string) { writeN(len(s)); w.WriteString(s) }

	w.WriteString(manifestMagic)
	writeS(m.Root)
	writeS(m.Fingerprint)
	writeN(m.Generation)
	writeI(m.Updated.UnixNano())
	writeN(len(m.Shards))
	for _, s := range m.Shards {
		writeS(s.Start)
		writeS(s.File)
		writeN(s.Files)
		writeI(s.Bytes)
	}
	writeN(len(m.Files))
	last := ""
	for _, f := range m.Files {
		p := 0
		for p < len(last) && p < len(f.Path) && last[p] == f.Path[p] {
			p++
		}
		writeN(p)
		writeS(f.Path[p:])
		writeI(f.Size)
		writeI(f.MTime)
		last = f.Path
	}
	w.WriteString(manifestTrailer)
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// ShardPaths returns the absolute paths of the manifest's shard files,
// given the repo's index directory.
func (m *Manifest) ShardPaths(dir string) []string {
	var out []string
	for _, s := range m.Shards {
		out = append(out, filepath.Join(dir, s.File))
	}
	return out
}

// NumFiles returns the number of files in the repository.
func (m *Manifest) NumFiles() int {
	n := 0
	for _, s := range m.Shards {
		n += s.Files
	}
	return n
}

// TotalBytes returns the total size of the files in the manifest.
func (m *Manifest) TotalBytes() int64 {
	var n int64
	for _, s := range m.Shards {
		n += s.Bytes
	}
	return n
}
