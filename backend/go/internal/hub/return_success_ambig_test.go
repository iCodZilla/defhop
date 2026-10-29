package hub

import (
	"defhop/internal/model"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"defhop/internal/java"
)

func TestAmbiguousResultSuccessJumps(t *testing.T) {
	dir := t.TempDir()
	r1 := filepath.Join(dir, "R1.java")
	r2 := filepath.Join(dir, "R2.java")
	callerPath := filepath.Join(dir, "Caller.java")
	s1 := "package a;\npublic class Result {\n  public static Result success(){ return null; }\n}\n"
	s2 := "package b;\npublic class Result {\n}\n"
	callerSrc := "package c;\nclass Caller {\n  Object m(){ return Result.success(); }\n}\n"
	os.WriteFile(r1, []byte(s1), 0644)
	os.WriteFile(r2, []byte(s2), 0644)
	os.WriteFile(callerPath, []byte(callerSrc), 0644)
	h := New()
	h.Index.PutFile(java.Parse(r1, s1, 0))
	h.Index.PutFile(java.Parse(r2, s2, 0))
	h.Index.PutFile(java.Parse(callerPath, callerSrc, 0))
	offset := strings.Index(callerSrc, "success")
	line, col := offsetToLineCol(callerSrc, offset)
	locs, _ := h.Navigate(model.NavRequest{Kind: model.NavDef, Path: callerPath, Line: line, Character: col})
	if len(locs) == 0 {
		t.Fatal("expected Result.success to resolve when only one Result owns success")
	}
}
