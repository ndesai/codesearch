// Copyright 2011 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package index

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

func mmapFileErr(f *os.File) (mmapData, error) {
	st, err := f.Stat()
	if err != nil {
		return mmapData{}, err
	}
	size := st.Size()
	if int64(int(size+4095)) != size+4095 {
		return mmapData{}, fmt.Errorf("%s: too large for mmap", f.Name())
	}
	if size == 0 {
		return mmapData{f, nil}, nil
	}
	h, err := syscall.CreateFileMapping(syscall.Handle(f.Fd()), nil, syscall.PAGE_READONLY, uint32(size>>32), uint32(size), nil)
	if err != nil {
		return mmapData{}, fmt.Errorf("CreateFileMapping %s: %v", f.Name(), err)
	}
	defer syscall.CloseHandle(h)

	addr, err := syscall.MapViewOfFile(h, syscall.FILE_MAP_READ, 0, 0, 0)
	if err != nil {
		return mmapData{}, fmt.Errorf("MapViewOfFile %s: %v", f.Name(), err)
	}
	data := unsafe.Slice((*byte)(unsafe.Pointer(addr)), size)
	return mmapData{f, data}, nil
}

func munmap(m mmapData) error {
	var err error
	if m.d != nil {
		err = syscall.UnmapViewOfFile(uintptr(unsafe.Pointer(&m.d[0])))
	}
	if m.f != nil {
		m.f.Close()
	}
	return err
}
