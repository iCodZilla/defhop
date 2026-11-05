package java

import (
	"testing"
)

func TestParseUnicodeIdentifiers(t *testing.T) {
	fd := Parse("/tmp/T.java", "package demo;\npublic class 测试 {\n  int 数量;\n  void 计算() {}\n}\n", 0)
	if fd == nil {
		t.Fatal("nil file slice")
	}
	names := map[string]bool{}
	for _, sym := range fd.Symbols {
		if sym != nil {
			names[sym.Name] = true
		}
	}
	for _, want := range []string{"demo.测试", "数量", "计算"} {
		if !names[want] {
			t.Fatalf("symbol %q missing; got %v", want, names)
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add("class A { int x; }")
	f.Add("package p;\nclass A extends B { void m() { x.y(); } }")
	f.Add("/* unterminated")
	f.Add("class A { String s = \"\"\"\n{\n\"\"\"; }")
	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 1<<20 {
			t.Skip()
		}
		_ = Parse("/f/F.java", src, 0)
	})
}
