package java_test

import (
	"defhop/internal/java"
	"strings"
	"testing"
)

func TestRecordComponentsAndBody(t *testing.T) {
	src := `package t;
public record Point(int x, String name) implements java.io.Serializable {
  public int sum() { return x; }
}
class Use {
  int f(Point p) { return p.x() + p.name().length(); }
}
`
	fd := java.Parse("Point.java", src, 0)
	want := map[string]bool{
		"T|t.Point":             false,
		"F|T|t.Point|x":         false,
		"F|T|t.Point|name":      false,
		"M|T|t.Point|x|":        false,
		"M|T|t.Point|name|":     false,
		"M|T|t.Point|sum|":      false,
		"T|t.Use":               false,
		"M|T|t.Use|f|t.Point":   false,
	}
	for _, s := range fd.Symbols {
		if s == nil {
			continue
		}
		if _, ok := want[s.Key]; ok {
			want[s.Key] = true
		}
	}
	for k, ok := range want {
		if !ok {
			t.Errorf("missing symbol %s", k)
		}
	}
	// Serializable should be a super
	for _, s := range fd.Symbols {
		if s != nil && s.Key == "T|t.Point" {
			found := false
			for _, sk := range s.SuperKeys {
				if strings.Contains(sk, "Serializable") {
					found = true
				}
			}
			if !found {
				t.Fatalf("Point should implement Serializable, supers=%v", s.SuperKeys)
			}
		}
	}
}

func TestSealedPermitsStillParsesBody(t *testing.T) {
	src := `package t;
public sealed class Shape permits Circle {
  public int tag() { return 1; }
}
final class Circle extends Shape {}
`
	fd := java.Parse("Shape.java", src, 0)
	hasTag := false
	for _, s := range fd.Symbols {
		if s != nil && s.Key == "M|T|t.Shape|tag|" {
			hasTag = true
		}
	}
	if !hasTag {
		t.Fatal("sealed class body method tag() missing")
	}
}
