package java

import (
	"strings"
	"testing"
)

// List<Foo> field: F12 on Foo must target Foo, not List (regression from lastTypeID).
func TestGenericFieldTypeArgDEF(t *testing.T) {
	src := `package samples.lang;
public class Holder {
  private java.util.List<ItemDto> items;
}
class ItemDto {}
`
	fd := Parse("Holder.java", src, 1)
	if fd == nil {
		t.Fatal("parse failed")
	}
	off := strings.Index(src, "ItemDto>")
	if off < 0 {
		t.Fatal("ItemDto not found")
	}
	// Build a minimal snap via Parse-only Resolve needs SymTab — use store through hub-less path:
	// Resolve against a fake snap is awkward; assert Occ TargetKey instead.
	var hit *struct {
		Name, Target string
	}
	for _, occ := range fd.Occs {
		if occ == nil || occ.Start > off || occ.End <= off {
			continue
		}
		if occ.Name == "ItemDto" {
			hit = &struct{ Name, Target string }{occ.Name, occ.TargetKey}
			break
		}
	}
	if hit == nil {
		t.Fatalf("no Occ for ItemDto; occs=%d", len(fd.Occs))
	}
	if !strings.Contains(hit.Target, "ItemDto") {
		t.Fatalf("ItemDto Occ TargetKey=%q want ...ItemDto", hit.Target)
	}
	if strings.Contains(hit.Target, "List") {
		t.Fatalf("ItemDto Occ wrongly bound to List: %q", hit.Target)
	}
}

func TestGenericFieldOuterTypeOcc(t *testing.T) {
	src := `package samples.lang;
public class Holder {
  private List<ItemDto> items;
}
class ItemDto {}
`
	fd := Parse("Holder.java", src, 1)
	off := strings.Index(src, "List<")
	var target string
	for _, occ := range fd.Occs {
		if occ != nil && occ.Start <= off && occ.End > off && occ.Name == "List" {
			target = occ.TargetKey
			break
		}
	}
	if target == "" {
		t.Fatal("no Occ for List")
	}
	if !strings.Contains(target, "List") {
		t.Fatalf("List TargetKey=%q", target)
	}
}
