package java

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"defhop/internal/store"
)

func indexJavaFiles(t *testing.T, dir string, files map[string]string) *store.Snapshot {
	t.Helper()
	idx := store.New()
	for name, src := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		fd := Parse(p, src, 1)
		if fd == nil {
			t.Fatalf("parse %s failed", name)
		}
		idx.PutFile(fd)
	}
	return idx.Snapshot()
}

func resolveAt(t *testing.T, snap *store.Snapshot, path, src, needle string) (*store.Snapshot, string, string) {
	t.Helper()
	fd := snap.File(store.PathKey(path))
	if fd == nil {
		t.Fatal("missing file slice")
	}
	off := strings.Index(src, needle)
	if off < 0 {
		t.Fatalf("needle %q not found", needle)
	}
	sym, key := Resolve(snap, fd, off, TokenAt(src, off), src)
	if sym == nil {
		return snap, key, ""
	}
	decl := ""
	if sym.Declaration != nil {
		decl = sym.Declaration.Path
	}
	return snap, key, decl
}

func TestChainCallReturnType(t *testing.T) {
	tmp := t.TempDir()
	aSrc := `package demo;
public class A {
  B b;
  public B getB() { return b; }
}
`
	bSrc := `package demo;
public class B {
  String name;
  public String getName() { return name; }
  public void foo() {}
}
`
	useSrc := `package demo;
public class Use {
  void f(A a) {
    String s = a.getB().getName();
  }
}
`
	snap := indexJavaFiles(t, tmp, map[string]string{
		"A.java": aSrc, "B.java": bSrc, "Use.java": useSrc,
	})
	usePath := filepath.Join(tmp, "Use.java")
	_, key, decl := resolveAt(t, snap, usePath, useSrc, "getName")
	if !strings.Contains(key, "B") && !strings.Contains(decl, "B.java") {
		t.Fatalf("expected B.getName, got key=%s decl=%s", key, decl)
	}
}

func TestChainFieldThenMember(t *testing.T) {
	tmp := t.TempDir()
	aSrc := `package demo;
public class A {
  public B b;
}
`
	bSrc := `package demo;
public class B {
  public String name;
  public void foo() {}
}
`
	useSrc := `package demo;
public class Use {
  void f(A a) {
    String s = a.b.name;
    a.b.foo();
  }
}
`
	snap := indexJavaFiles(t, tmp, map[string]string{
		"A.java": aSrc, "B.java": bSrc, "Use.java": useSrc,
	})
	usePath := filepath.Join(tmp, "Use.java")
	_, key, decl := resolveAt(t, snap, usePath, useSrc, "name")
	if !strings.Contains(key, "B") && !strings.Contains(decl, "B.java") {
		t.Fatalf("expected B.name, got key=%s decl=%s", key, decl)
	}
	_, key, decl = resolveAt(t, snap, usePath, useSrc, "foo")
	if !strings.Contains(key, "B") && !strings.Contains(decl, "B.java") {
		t.Fatalf("expected B.foo, got key=%s decl=%s", key, decl)
	}
}

func TestChainListGetElement(t *testing.T) {
	tmp := t.TempDir()
	bSrc := `package demo;
public class B {
  public void foo() {}
}
`
	useSrc := `package demo;
import java.util.List;
public class Use {
  void f(List<B> list) {
    list.get(0).foo();
  }
}
`
	snap := indexJavaFiles(t, tmp, map[string]string{
		"B.java": bSrc, "Use.java": useSrc,
	})
	usePath := filepath.Join(tmp, "Use.java")
	_, key, decl := resolveAt(t, snap, usePath, useSrc, "foo")
	if !strings.Contains(key, "B") && !strings.Contains(decl, "B.java") {
		t.Fatalf("expected B.foo via List.get, got key=%s decl=%s", key, decl)
	}
}

func TestChainOptionalOfGet(t *testing.T) {
	tmp := t.TempDir()
	bSrc := `package demo;
public class B {
  public void foo() {}
}
`
	useSrc := `package demo;
import java.util.Optional;
public class Use {
  void f(B b) {
    Optional.of(b).get().foo();
  }
}
`
	snap := indexJavaFiles(t, tmp, map[string]string{
		"B.java": bSrc, "Use.java": useSrc,
	})
	usePath := filepath.Join(tmp, "Use.java")
	_, key, decl := resolveAt(t, snap, usePath, useSrc, "foo")
	if !strings.Contains(key, "B") && !strings.Contains(decl, "B.java") {
		t.Fatalf("expected B.foo via Optional.of.get, got key=%s decl=%s", key, decl)
	}
}

func TestChainNewThenMember(t *testing.T) {
	tmp := t.TempDir()
	bSrc := `package demo;
public class B {
  public void foo() {}
}
`
	useSrc := `package demo;
public class Use {
  void f() {
    new B().foo();
  }
}
`
	snap := indexJavaFiles(t, tmp, map[string]string{
		"B.java": bSrc, "Use.java": useSrc,
	})
	usePath := filepath.Join(tmp, "Use.java")
	_, key, decl := resolveAt(t, snap, usePath, useSrc, "foo")
	if !strings.Contains(key, "B") && !strings.Contains(decl, "B.java") {
		t.Fatalf("expected B.foo, got key=%s decl=%s", key, decl)
	}
}

func TestChainTripleCall(t *testing.T) {
	tmp := t.TempDir()
	aSrc := `package demo;
public class A {
  public B getB() { return null; }
}
`
	bSrc := `package demo;
public class B {
  public C getC() { return null; }
}
`
	cSrc := `package demo;
public class C {
  public void run() {}
}
`
	useSrc := `package demo;
public class Use {
  void f(A a) {
    a.getB().getC().run();
  }
}
`
	snap := indexJavaFiles(t, tmp, map[string]string{
		"A.java": aSrc, "B.java": bSrc, "C.java": cSrc, "Use.java": useSrc,
	})
	usePath := filepath.Join(tmp, "Use.java")
	_, key, decl := resolveAt(t, snap, usePath, useSrc, "run")
	if !strings.Contains(key, "C") && !strings.Contains(decl, "C.java") {
		t.Fatalf("expected C.run, got key=%s decl=%s", key, decl)
	}
}
