// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gitignore

import "testing"

func TestIgnored(t *testing.T) {
	root := Parse("", []byte(`
# comment
*.o
/build
out/
docs/**/*.html
**/gen
!keep.o
lib/**
\#notcomment
trailing
a?c
x[0-9]y
`))
	sub := Parse("pkg", []byte("local.txt\n/anchored\n!*.o\n"))
	s := Stack{}.Push(root).Push(sub)

	tests := []struct {
		path string
		dir  bool
		want bool
	}{
		{"main.o", false, true},
		{"src/main.o", false, true},
		{"keep.o", false, false},
		{"build", true, true},
		{"src/build", true, false},
		{"out", true, true},
		{"src/out", true, true},
		{"out", false, false}, // dir-only pattern doesn't match a file
		{"docs/a/b/x.html", false, true},
		{"docs/x.html", false, true},
		{"docs/x.htm", false, false},
		{"a/gen", true, true},
		{"gen", true, true},
		{"lib/x/y.c", false, true},
		{"lib", true, false},
		{"#notcomment", false, true},
		{"trailing", false, true},
		{"abc", false, true},
		{"abbc", false, false},
		{"x5y", false, true},
		{"xay", false, false},
		{"pkg/local.txt", false, true},
		{"local.txt", false, false},
		{"pkg/anchored", false, true},
		{"pkg/deep/anchored", false, false},
		{"pkg/foo.o", false, false}, // negated in deeper file
		{"src/foo.go", false, false},
	}
	for _, tt := range tests {
		if got := s.Ignored(tt.path, tt.dir); got != tt.want {
			t.Errorf("Ignored(%q, dir=%v) = %v, want %v", tt.path, tt.dir, got, tt.want)
		}
	}
}
