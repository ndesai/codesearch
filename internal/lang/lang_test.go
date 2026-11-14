// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lang

import "testing"

func TestDetect(t *testing.T) {
	for name, want := range map[string]string{
		"a/b/Foo.java":          "Java",
		"x.kt":                  "Kotlin",
		"frameworks/Android.bp": "Blueprint",
		"IFoo.aidl":             "AIDL",
		"types.hal":             "HIDL",
		"rect.h":                "C/C++ Header",
		"rect.cpp":              "C++",
		"main.go":               "Go",
		"BUILD.bazel":           "Starlark",
		"Makefile":              "Makefile",
		"init.rc":               "Android Init",
		"lib.rs":                "Rust",
		"app.tsx":               "TypeScript",
		"NOTICE":                "Text",
		"weird.zzz":             "Text",
	} {
		if got := Detect(name).Name; got != want {
			t.Errorf("Detect(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestFilter(t *testing.T) {
	cpp := NewFilter("c++")
	if !cpp.Match("a.cc") || !cpp.Match("a.h") || cpp.Match("a.java") {
		t.Error("lang:c++")
	}
	for _, q := range []string{"java", "go", "py", "python", "kotlin", "rust", "ts", "js", "bp", "aidl", "proto"} {
		if NewFilter(q) == nil {
			t.Errorf("NewFilter(%q) = nil", q)
		}
	}
	if !NewFilter("py").Match("x.py") || !NewFilter("bp").Match("Android.bp") || !NewFilter("ts").Match("a.ts") {
		t.Error("filter match")
	}
	if NewFilter("nosuchlanguage") != nil {
		t.Error("unknown language accepted")
	}
}
