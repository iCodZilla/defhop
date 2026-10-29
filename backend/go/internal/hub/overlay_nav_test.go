package hub

import (
	"defhop/internal/java"
	"defhop/internal/model"
	"defhop/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Editing Result.java (new method) must be visible when DEF runs from Caller.java
// even if the async UPDATE worker has not published yet.
func TestNavigateSeesOtherFileOverlayEdit(t *testing.T) {
	dir := t.TempDir()
	resultPath := filepath.Join(dir, "Result.java")
	callerPath := filepath.Join(dir, "Caller.java")

	resultV1 := "package p;\npublic class Result {\n}\n"
	resultV2 := "package p;\npublic class Result {\n  public static Result success(){ return null; }\n}\n"
	callerSrc := "package p;\nclass Caller {\n  void m(){ Result.success(); }\n}\n"

	if err := os.WriteFile(resultPath, []byte(resultV1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(callerPath, []byte(callerSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	h := New()
	h.Index.PutFile(java.Parse(resultPath, resultV1, 0))
	h.Index.PutFile(java.Parse(callerPath, callerSrc, 0))

	// Simulate editor UPDATE that only stored overlay (async index not run).
	key := store.PathKey(resultPath)
	gen := h.Overlay.Put(key, resultPath, resultV2)
	if gen == 0 {
		t.Fatal("expected overlay gen > 0")
	}
	if fd := h.Index.Snapshot().File(key); fd != nil && fd.Gen >= gen {
		t.Fatalf("precondition failed: index already at overlay gen")
	}

	offset := strings.Index(callerSrc, "success")
	if offset < 0 {
		t.Fatal("success not found")
	}
	line, col := offsetToLineCol(callerSrc, offset)

	locs, _ := h.Navigate(model.NavRequest{
		Kind:      model.NavDef,
		Path:      callerPath,
		Line:      line,
		Character: col,
	})
	if len(locs) == 0 {
		t.Fatal("expected DEF to Result.success after overlay edit")
	}
	found := false
	for _, loc := range locs {
		if strings.EqualFold(filepath.Clean(loc.Path), filepath.Clean(resultPath)) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected jump into Result.java, got %#v", locs)
	}
}

func TestParseSafeBrokenSyntaxStillIndexes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Broken.java")
	// Unbalanced braces / truncated method — tokenizer must still return a slice.
	src := "package p;\npublic class Broken {\n  public void ok(){}\n  public void bad(\n"
	h := New()
	gen := h.UpdateBuffer(path, src)
	h.indexOneBuffer(path, store.PathKey(path), src, gen)
	fd := h.Index.Snapshot().File(store.PathKey(path))
	if fd == nil {
		t.Fatal("expected FileSlice even with broken syntax")
	}
	if fd.Gen != gen {
		t.Fatalf("gen=%d want %d", fd.Gen, gen)
	}
	hasOK := false
	for _, sy := range fd.Symbols {
		if sy.Name == "ok" && sy.Kind == "method" {
			hasOK = true
		}
	}
	if !hasOK {
		t.Fatalf("expected method ok to survive broken sibling, symbols=%v", names(fd))
	}
}

func names(fd *model.FileSlice) []string {
	var out []string
	for _, sy := range fd.Symbols {
		out = append(out, sy.Kind+":"+sy.Name)
	}
	return out
}

func offsetToLineCol(src string, offset int) (line, col int) {
	for i := 0; i < offset && i < len(src); i++ {
		if src[i] == '\n' {
			line++
			col = 0
		} else {
			col++
		}
	}
	return line, col
}
