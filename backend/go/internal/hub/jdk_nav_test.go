package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"defhop/internal/deps"
	"defhop/internal/model"
	"defhop/internal/store"
)

func TestDEFSimpleDateFormat(t *testing.T) {
	z := deps.FindJDKSrcZip()
	if z == "" {
		// Tests don't auto-discover; probe JAVA_HOME / common roots via env set.
		for _, env := range []string{"JAVA_HOME", "JDK_HOME"} {
			if v := os.Getenv(env); v != "" {
				for _, p := range []string{
					filepath.Join(v, "lib", "src.zip"),
					filepath.Join(v, "src.zip"),
				} {
					if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Size() > 100*1024 {
						z = p
						break
					}
				}
			}
			if z != "" {
				break
			}
		}
	}
	if z == "" {
		t.Skip("no JDK src.zip")
	}
	deps.SetJDKHome(z)
	defer deps.SetJDKHome("")
	tmp := t.TempDir()
	h := New()
	h.CacheDir = filepath.Join(tmp, "cache")
	_ = os.MkdirAll(h.CacheDir, 0o755)
	src := `package demo;
import java.text.SimpleDateFormat;
import java.text.ParseException;
import java.util.Date;
public class DateUtil {
  public static Date parseDate(String date, String pattern) throws ParseException {
    SimpleDateFormat format = new SimpleDateFormat(pattern);
    return format.parse(date);
  }
}
`
	path := filepath.Join(tmp, "DateUtil.java")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	fd := parseSafe(path, src, 1)
	fd.ContentHash = store.ContentHashString(src)
	h.Index.PutFile(fd)
	line, col := -1, -1
	for i, l := range strings.Split(src, "\n") {
		if strings.Contains(l, "new SimpleDateFormat") {
			line, col = i, strings.Index(l, "SimpleDateFormat")
			break
		}
	}
	locs, _ := h.Navigate(model.NavRequest{
		Kind: model.NavDef, Path: path, Text: src, Line: line, Character: col,
	})
	if len(locs) == 0 {
		t.Fatal("expected DEF to SimpleDateFormat")
	}
	if !strings.Contains(locs[0].Path, "SimpleDateFormat.java") {
		t.Fatalf("got %s", locs[0].Path)
	}
}

func TestDEFPromptsConfigureJDKWhenUnset(t *testing.T) {
	deps.SetJDKHome("")
	deps.SetJDKAutoDiscover(false)
	defer deps.SetJDKHome("")
	defer deps.SetJDKAutoDiscover(true)
	tmp := t.TempDir()
	h := New()
	h.CacheDir = filepath.Join(tmp, "cache")
	_ = os.MkdirAll(h.CacheDir, 0o755)
	// Stale SymTab hit that previously opened index-cache without a prompt.
	stalePath := filepath.Join(h.CacheDir, "deps", "jdk-fake", "java", "text", "SimpleDateFormat.java")
	_ = os.MkdirAll(filepath.Dir(stalePath), 0o755)
	_ = os.WriteFile(stalePath, []byte("package java.text;\npublic class SimpleDateFormat {}\n"), 0o644)
	fdStale := parseSafe(stalePath, "package java.text;\npublic class SimpleDateFormat {}\n", 1)
	fdStale.ContentHash = store.ContentHashString("package java.text;\npublic class SimpleDateFormat {}\n")
	h.Index.PutFile(fdStale)

	src := `package demo;
import java.text.SimpleDateFormat;
public class DateUtil {
  void f() { new SimpleDateFormat("x"); }
}
`
	path := filepath.Join(tmp, "DateUtil.java")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	fd := parseSafe(path, src, 1)
	fd.ContentHash = store.ContentHashString(src)
	h.Index.PutFile(fd)
	line, col := -1, -1
	for i, l := range strings.Split(src, "\n") {
		if strings.Contains(l, "new SimpleDateFormat") {
			line, col = i, strings.Index(l, "SimpleDateFormat")
			break
		}
	}
	locs, menu := h.Navigate(model.NavRequest{
		Kind: model.NavDef, Path: path, Text: src, Line: line, Character: col,
	})
	if len(locs) != 0 {
		t.Fatalf("expected no locations without jdkHome, got %#v", locs)
	}
	if menu != HintConfigureJDK {
		t.Fatalf("expected navMenu %q, got %q", HintConfigureJDK, menu)
	}
}
