package store

import (
	"defhop/internal/model"
	"testing"
)

func TestInternFileSliceSharesStrings(t *testing.T) {
	fd := &model.FileSlice{
		Path:    "/ws/a/Foo.java",
		Package: "com.example",
		Imports: []string{"java.util.List", "java.util.List"},
		Symbols: []*model.Symbol{
			{
				Key: "T|com.example.Foo", Kind: "type", Name: "Foo",
				Declaration: &model.Location{Path: "/ws/a/Foo.java", Kind: "type"},
			},
			{
				Key: "M|T|com.example.Foo|bar|", Kind: "method", Name: "bar",
				OwnerKey: "T|com.example.Foo", ReturnType: "T|java.util.List",
			},
		},
		Occs: []*model.Occ{
			{Role: "reference", Name: "Foo", TargetKey: "T|com.example.Foo",
				Location: &model.Location{Path: "/ws/a/Foo.java"}},
		},
	}
	InternFileSlice(fd)
	if got, want := Intern(fd.Symbols[0].Key), fd.Occs[0].TargetKey; got != want {
		t.Fatalf("key %q vs occ %q", got, want)
	}
	if fd.Path != fd.Symbols[0].Declaration.Path {
		t.Fatalf("path not interned equally")
	}
	if a, b := Intern(fd.Imports[0]), Intern(fd.Imports[1]); a != b {
		t.Fatalf("duplicate imports not shared")
	}
}
