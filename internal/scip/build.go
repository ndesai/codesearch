// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scip

import "encoding/binary"

// Encoding helpers that build serialized SCIP indexes, for tests. They
// cover the fields Load reads: Index{metadata=1, documents=2},
// Metadata{project_root=3}, Document{relative_path=1, occurrences=2} and
// Occurrence{range=1 (packed), symbol=2, symbol_roles=3}.

func tag(num, wire int) []byte { return binary.AppendUvarint(nil, uint64(num<<3|wire)) }

func bytesField(num int, b []byte) []byte {
	out := tag(num, 2)
	out = binary.AppendUvarint(out, uint64(len(b)))
	return append(out, b...)
}

// EncodeOccurrence encodes an occurrence; rng is [line, startCol, endCol]
// or [startLine, startCol, endLine, endCol], 0-based.
func EncodeOccurrence(rng []int, symbol string, roles int) []byte {
	var packed []byte
	for _, r := range rng {
		packed = binary.AppendUvarint(packed, uint64(r))
	}
	out := bytesField(1, packed)
	out = append(out, bytesField(2, []byte(symbol))...)
	if roles != 0 {
		out = append(out, binary.AppendUvarint(tag(3, 0), uint64(roles))...)
	}
	return out
}

// EncodeDocument encodes a document with the given occurrences.
func EncodeDocument(path string, occs ...[]byte) []byte {
	out := bytesField(1, []byte(path))
	for _, o := range occs {
		out = append(out, bytesField(2, o)...)
	}
	return out
}

// EncodeIndex encodes an index with a project root URI and documents.
func EncodeIndex(projectRoot string, docs ...[]byte) []byte {
	out := bytesField(1, bytesField(3, []byte(projectRoot)))
	for _, d := range docs {
		out = append(out, bytesField(2, d)...)
	}
	return out
}
