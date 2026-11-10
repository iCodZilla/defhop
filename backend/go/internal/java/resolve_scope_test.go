package java

import (
	"defhop/internal/model"
	"defhop/internal/store"
	"testing"
)

// Resolve must not pick a unique workspace-wide same-name symbol when the
// current file has no Occ / import for it — that caused wrong hops in large
// indexes (unrelated Foo in another package).
func TestResolveNoGlobalSameNameGuess(t *testing.T) {
	snap := &store.Snapshot{
		SymTab: map[string]*model.Symbol{},
		Simple: map[string][]string{},
		Flat:   map[string]string{},
	}
	other := &model.Symbol{
		Key:  "T|com.other.Token",
		Kind: "type",
		Name: "Token",
		Declaration: &model.Location{
			Path: "/other/Token.java", Line: 0, Character: 0,
		},
	}
	snap.SymTab[other.Key] = other
	snap.Simple["Token"] = []string{other.Key}
	snap.Flat["com.other.Token"] = other.Key

	fd := &model.FileSlice{
		Path:    "/here/Caller.java",
		Package: "com.here",
		Symbols: nil,
		Occs:    nil,
	}
	src := "package com.here;\nclass Caller { void m(){ Token x; } }\n"
	offset := indexOf(src, "Token")
	sym, _ := Resolve(snap, fd, offset, "Token", src)
	if sym != nil {
		t.Fatalf("expected no global same-name guess, got %s", sym.Key)
	}
}

func TestResolveSameFileUniqueDecl(t *testing.T) {
	local := &model.Symbol{
		Key:  "T|com.here.Token",
		Kind: "type",
		Name: "Token",
		Declaration: &model.Location{
			Path: "/here/Token.java", Line: 0, Character: 13,
		},
	}
	snap := &store.Snapshot{
		SymTab: map[string]*model.Symbol{local.Key: local},
		Simple: map[string][]string{"Token": {local.Key}},
		Flat:   map[string]string{"com.here.Token": local.Key},
	}
	fd := &model.FileSlice{
		Path:    "/here/Token.java",
		Package: "com.here",
		Symbols: []*model.Symbol{local},
	}
	src := "package com.here;\nclass Token {}\n"
	// No Occ — fall back to same-file unique decl.
	sym, key := Resolve(snap, fd, 0, "Token", src)
	if sym == nil || key != local.Key {
		t.Fatalf("expected same-file Token, got %#v key=%q", sym, key)
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
