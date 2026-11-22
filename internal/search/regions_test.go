// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package search

import (
	"strings"
	"testing"
)

// renderRegions renders each byte's region as a letter: c(ode), m (comment), s(tring).
func renderRegions(langName, src string) string {
	spans := scanRegions(langName, []byte(src))
	var b strings.Builder
	for i := range src {
		if src[i] == '\n' {
			b.WriteByte('\n')
			continue
		}
		b.WriteByte(map[region]byte{regionCode: 'c', regionComment: 'm', regionString: 's'}[regionAt(spans, i)])
	}
	return b.String()
}

func TestScanRegions(t *testing.T) {
	tests := []struct{ lang, src, want string }{
		{"Go", `x := "a\"b" // c`, `cccccsssssscmmmm`},
		{"Go", "s := `a\nb` /* c\nd */ y", "cccccss\nsscmmmm\nmmmmcc"},
		{"Java", `c = 'x'; // "not a string"`, `ccccsssccmmmmmmmmmmmmmmmmm`},
		{"Python", "x = '''a\n# b''' # c", "ccccssss\nsssssscmmm"},
		{"Bash", `echo "#x" # y`, `cccccsssscmmm`},
		{"Rust", `fn f<'a>(x: &'a str) // c`, `cccccccccccccccccccccmmmm`},
		{"SQL", `select 'x' -- c`, `cccccccssscmmmm`},
		{"C++", `int a; /* unterminated`, `cccccccmmmmmmmmmmmmmmm`},
		{"Text", `"quoted" # not a comment`, `cccccccccccccccccccccccc`},
	}
	for _, tt := range tests {
		if got := renderRegions(tt.lang, tt.src); got != tt.want {
			t.Errorf("%s %q:\n got %q\nwant %q", tt.lang, tt.src, got, tt.want)
		}
	}
}
