// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package outline

import (
	"fmt"
	"strings"
	"testing"
)

func outline(lang, src string) string {
	var out []string
	for _, s := range Extract(lang, src) {
		out = append(out, fmt.Sprintf("%s%s %s:%d", strings.Repeat(".", s.Depth), s.Kind, s.Name, s.Line))
	}
	return strings.Join(out, " | ")
}

func TestExtract(t *testing.T) {
	tests := []struct {
		lang, src, want string
	}{
		{"Go", "package x\n\ntype Reader struct {\n\tbuf []byte\n}\n\nfunc NewReader() *Reader {\n\treturn nil\n}\n\nfunc (b *Reader) Read(p []byte) (int, error) {\n\tx := f(1)\n}\ntype Op int\n",
			"struct Reader:3 | func NewReader:7 | method Reader.Read:11 | type Op:14"},
		{"Java", "package a;\n\n@Deprecated\npublic class Activity extends Base {\n    private int mX = 1;\n    public Activity() {\n    }\n    @Override\n    protected void onCreate(Bundle b) {\n        if (x) {\n        }\n        foo(1);\n        return;\n    }\n    interface Callback {\n        void call(int x);\n    }\n}\n",
			"class Activity:4 | .method Activity:6 | .method onCreate:9 | .interface Callback:15 | ..method call:16"},
		{"C++", "#include <x>\n// int commented(int);\nnamespace android {\nclass Foo;\nclass Rect : public Base {\n  bool intersect(const Rect& r) const;\n};\nbool Rect::intersect(const Rect& r) const {\n  return x(r);\n}\nRect::Rect(int x) : x_(x) {}\nRect::Rect()\n    : x_(0),\n      y_(f(1)) {}\nstatic int helper(void)\n{\n  int y = compute(1);\n}\n}\n",
			"namespace android:3 | class Rect:5 | method Rect.intersect:8 | method Rect.Rect:11 | method Rect.Rect:12 | func helper:15"},
		{"C/C++ Header", "struct Rect {\n    bool intersect(const Rect& r) const;\n    virtual void draw() = 0;\n};\n",
			"struct Rect:1 | .method intersect:2 | .method draw:3"},
		{"Python", "import os\n\nclass Foo(Base):\n    def __init__(self):\n        pass\n\n    async def run(self):\n        x = 1\n\ndef main():\n    # def fake():\n    pass\n",
			"class Foo:3 | .method __init__:4 | .method run:7 | func main:10"},
		{"TypeScript", "export interface Props {\n  id: string;\n}\nexport type Id = string;\nexport class Store {\n  async load(id: string): Promise<void> {\n    if (x) {\n    }\n  }\n}\nexport const fetchData = async (url: string) => {};\nexport function render() {}\n",
			"interface Props:1 | type Id:4 | class Store:5 | .method load:6 | func fetchData:11 | func render:12"},
		{"Rust", "pub struct Foo {\n}\nimpl Foo {\n    pub fn new() -> Self {\n    }\n}\npub(crate) fn helper() {}\n",
			"struct Foo:1 | impl Foo:3 | .method new:4 | func helper:7"},
		{"Kotlin", "data class Point(val x: Int)\nfun String.shout(): String = uppercase()\nobject Registry {\n    suspend fun load() {}\n}\n",
			"class Point:1 | method String.shout:2 | object Registry:3 | .method load:4"},
		{"Markdown", "# Title\n\nText\n\n## Install\n### Linux\n## Usage\n",
			"h1 Title:1 | .h2 Install:5 | ..h3 Linux:6 | .h2 Usage:7"},
		{"Blueprint", "cc_library {\n    name: \"libui\",\n    srcs: [\"a.cpp\"],\n}\n",
			"module cc_library:1 | .name libui:2"},
		{"Protocol Buffer", "message Req {\n  int32 id = 1;\n}\nservice Api {\n  rpc Get(Req) returns (Resp);\n}\n",
			"message Req:1 | service Api:4 | .method Get:5"},
		{"Bash", "#!/bin/bash\nbuild() {\n  make\n}\nfunction deploy {\n}\n", "func build:2"},
		{"Text", "anything", ""},
	}
	for _, tt := range tests {
		if got := outline(tt.lang, tt.src); got != tt.want {
			t.Errorf("%s:\n got %s\nwant %s", tt.lang, got, tt.want)
		}
	}
}
