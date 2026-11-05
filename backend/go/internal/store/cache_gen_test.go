package store

import (
	"context"
	"testing"

	"defhop/internal/model"
)

// Gen is a per-process overlay counter. Persisted slices must reload at gen 0,
// otherwise the next session's first overlay edit (gen 1) is treated as stale.
func TestCacheDoesNotPersistOverlayGen(t *testing.T) {
	dir := t.TempDir()
	path := "/proj/A.java"
	key := PathKey(path)
	files := map[string]*model.FileSlice{
		key: {
			Path:        path,
			Gen:         7,
			ContentHash: "h",
			Symbols:     []*model.Symbol{{Key: "T|demo.A", Name: "demo.A", Kind: "type"}},
		},
	}

	if err := SavePacked(dir, "fp", files); err != nil {
		t.Fatal(err)
	}
	packed := LoadPacked(dir, "fp")
	if packed[key] == nil {
		t.Fatal("packed slice missing")
	}
	if packed[key].Gen != 0 {
		t.Fatalf("packed gen = %d, want 0", packed[key].Gen)
	}

	if err := SaveFileCache(dir, files, map[string]string{key: "h"}); err != nil {
		t.Fatal(err)
	}
	cached := LoadFileCache(context.Background(), dir, map[string]string{key: "h"})
	if cached[key] == nil {
		t.Fatal("file cache slice missing")
	}
	if cached[key].Gen != 0 {
		t.Fatalf("file cache gen = %d, want 0", cached[key].Gen)
	}
}
