package java

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"defhop/internal/model"
	"defhop/internal/store"
)

func TestStaticImportPrefixType(t *testing.T) {
	root := `d:\dev\defhop\testdata\demo`
	sub := filepath.Join(root, `src`, `main`, `java`, `demo`, `sub`, `SubGen.java`)
	gen := filepath.Join(root, `src`, `main`, `java`, `demo`, `GenV2.java`)
	b1, err := os.ReadFile(sub)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := os.ReadFile(gen)
	if err != nil {
		t.Fatal(err)
	}
	fd1 := Parse(filepath.ToSlash(sub), string(b1), 0)
	fd2 := Parse(filepath.ToSlash(gen), string(b2), 0)
	t.Logf("SubGen symbols:")
	for _, s := range fd1.Symbols {
		t.Logf("  %s kind=%s name=%s owner=%s", s.Key, s.Kind, s.Name, s.OwnerKey)
	}
	t.Logf("GenV2 imports: %v", fd2.Imports)
	st := store.New()
	st.ReplaceAll(map[string]*model.FileSlice{
		store.PathKey(fd1.Path): fd1,
		store.PathKey(fd2.Path): fd2,
	})
	snap := st.Snapshot()
	text := string(b2)
	off := strings.Index(text, "PREFIX_TYPE_FIXED")
	// second occurrence in body
	off = strings.Index(text[off+1:], "PREFIX_TYPE_FIXED")
	off = strings.Index(text, "String a = PREFIX_TYPE_FIXED")
	off = off + len("String a = ")
	tok := TokenAt(text, off)
	t.Logf("token=%q offset=%d", tok, off)
	sym, key := Resolve(snap, fd2, off, tok, text)
	if sym == nil {
		t.Fatalf("resolve nil key=%s", key)
	}
	t.Logf("resolved %s kind=%s decl=%v", sym.Key, sym.Kind, sym.Declaration)
	if !strings.Contains(sym.Key, "PREFIX_TYPE_FIXED") {
		t.Fatalf("want field PREFIX_TYPE_FIXED got %s", sym.Key)
	}
}