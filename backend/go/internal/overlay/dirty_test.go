package overlay

import "testing"

func TestDirtyMetaClearedOnMarkPublished(t *testing.T) {
	s := New()
	gen := s.Put("a.java", "/ws/A.java", "class A {}")
	if n := len(s.DirtyMeta()); n != 1 {
		t.Fatalf("dirty=%d want 1", n)
	}
	s.MarkPublished("a.java", gen)
	if n := len(s.DirtyMeta()); n != 0 {
		t.Fatalf("after publish dirty=%d want 0", n)
	}
}

func TestDirtyMetaStaysWhenGenAdvances(t *testing.T) {
	s := New()
	gen1 := s.Put("a.java", "/ws/A.java", "class A {}")
	_ = s.Put("a.java", "/ws/A.java", "class A { int x; }")
	s.MarkPublished("a.java", gen1) // stale gen must not clear
	if n := len(s.DirtyMeta()); n != 1 {
		t.Fatalf("dirty=%d want 1 after stale mark", n)
	}
}

func TestRemoveClearsDirty(t *testing.T) {
	s := New()
	s.Put("a.java", "/ws/A.java", "class A {}")
	s.Remove("a.java")
	if n := len(s.DirtyMeta()); n != 0 {
		t.Fatalf("dirty=%d want 0", n)
	}
}
