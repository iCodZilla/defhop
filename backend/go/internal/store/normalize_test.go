package store

import (
	"defhop/internal/model"
	"testing"
)

func TestNormalizeTypeKeyRequiresPackageOrImport(t *testing.T) {
	snap := &Snapshot{
		SymTab: map[string]*model.Symbol{
			"T|com.other.Token": {Key: "T|com.other.Token", Name: "Token", Kind: "type"},
		},
		Simple: map[string][]string{"Token": {"T|com.other.Token"}},
	}
	// Unique workspace hit must NOT win without package/import evidence.
	got := normalizeTypeKey(snap, "T|Token", nil, "com.here")
	if got != "T|Token" {
		t.Fatalf("got %s, want unresolved T|Token", got)
	}
	// Same-package wins.
	snap.SymTab["T|com.here.Token"] = &model.Symbol{Key: "T|com.here.Token", Name: "Token", Kind: "type"}
	snap.Simple["Token"] = []string{"T|com.other.Token", "T|com.here.Token"}
	got = normalizeTypeKey(snap, "T|Token", nil, "com.here")
	if got != "T|com.here.Token" {
		t.Fatalf("got %s, want same-package", got)
	}
	// Explicit import wins.
	got = normalizeTypeKey(snap, "T|Token", []string{"com.other.Token"}, "com.here")
	if got != "T|com.other.Token" {
		t.Fatalf("got %s, want import", got)
	}
}
