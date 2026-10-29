package java_test

import (
	"strings"
	"testing"

	"defhop/internal/java"
	"defhop/internal/model"
	"defhop/internal/store"
)

func TestOverloadCallJumpsToMatchingArity(t *testing.T) {
	src := `package demo;
public class DateUtil {
  public static Date parseDate(String date) throws Exception {
    return parseDate(date, "yyyy-MM-dd");
  }
  public static Date parseDate(String date, String pattern) throws Exception {
    return null;
  }
}
`
	fd := java.Parse("DateUtil.java", src, 0)
	st := store.New()
	st.PutFile(fd)
	snap := st.Snapshot()

	line, col := findNeedle(src, `return parseDate(date, "yyyy-MM-dd")`, "parseDate")
	off := java.OffsetAt(src, line, col)
	sym, _ := java.Resolve(snap, fd, off, java.TokenAt(src, off), src)
	if sym == nil {
		t.Fatal("resolve returned nil")
	}
	if !strings.Contains(sym.Key, "|parseDate|") || paramArity(sym) != 2 {
		t.Fatalf("expected 2-arg overload, got key=%s", sym.Key)
	}
	if sym.Declaration == nil || sym.Declaration.Line == line {
		t.Fatalf("should jump to other overload declaration, got line=%d key=%s",
			sym.Declaration.Line, sym.Key)
	}
}

func TestOverloadPrefersMatchingArgType(t *testing.T) {
	src := `package demo;
public class Util {
  public static void put(int n) {}
  public static void put(String s) {}
  void f() {
    put("hi");
  }
}
`
	fd := java.Parse("Util.java", src, 0)
	st := store.New()
	st.PutFile(fd)
	snap := st.Snapshot()
	line, col := findNeedle(src, `put("hi")`, "put")
	off := java.OffsetAt(src, line, col)
	sym, _ := java.Resolve(snap, fd, off, java.TokenAt(src, off), src)
	if sym == nil {
		t.Fatal("resolve returned nil")
	}
	if !strings.Contains(sym.Key, "String") {
		t.Fatalf("expected String overload, got key=%s", sym.Key)
	}
}

func TestOneArgCallStaysOnOneArg(t *testing.T) {
	src := `package demo;
public class DateUtil {
  public static Date parseDate(String date) throws Exception {
    return null;
  }
  public static Date parseDate(String date, String pattern) throws Exception {
    return parseDate(date);
  }
}
`
	fd := java.Parse("DateUtil.java", src, 0)
	st := store.New()
	st.PutFile(fd)
	snap := st.Snapshot()
	line, col := findNeedle(src, "return parseDate(date);", "parseDate")
	off := java.OffsetAt(src, line, col)
	sym, _ := java.Resolve(snap, fd, off, java.TokenAt(src, off), src)
	if sym == nil || paramArity(sym) != 1 {
		t.Fatalf("expected 1-arg, got %#v", sym)
	}
}

func findNeedle(src, lineSub, token string) (line, col int) {
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if strings.Contains(l, lineSub) {
			return i, strings.Index(l, token)
		}
	}
	return -1, -1
}

func paramArity(sym *model.Symbol) int {
	prefix := "M|" + sym.OwnerKey + "|" + sym.Name + "|"
	if !strings.HasPrefix(sym.Key, prefix) {
		return -1
	}
	params := strings.TrimPrefix(sym.Key, prefix)
	if params == "" {
		return 0
	}
	return strings.Count(params, ",") + 1
}
