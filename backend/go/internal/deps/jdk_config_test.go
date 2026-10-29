package deps

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfiguredJDKHomePreferred(t *testing.T) {
	tmp := t.TempDir()
	zipPath := filepath.Join(tmp, "jdk", "src.zip")
	if err := os.MkdirAll(filepath.Dir(zipPath), 0o755); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 2048)
	for i := range data {
		data[i] = 'x'
	}
	if err := os.WriteFile(zipPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	SetJDKHome(filepath.Join(tmp, "jdk"))
	defer SetJDKHome("")
	got := FindJDKSrcZip()
	if got != zipPath {
		t.Fatalf("expected %s, got %s", zipPath, got)
	}
}

func TestConfiguredSrcZipDirect(t *testing.T) {
	tmp := t.TempDir()
	zipPath := filepath.Join(tmp, "src.zip")
	data := make([]byte, 2048)
	if err := os.WriteFile(zipPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	SetJDKHome(zipPath)
	defer SetJDKHome("")
	got := FindJDKSrcZip()
	if got != zipPath {
		t.Fatalf("expected %s, got %s", zipPath, got)
	}
}

func TestFindJDKSrcZipEmptyWhenAutoOff(t *testing.T) {
	SetJDKHome("")
	t.Setenv("DEFHOP_JDK_HOME", "")
	t.Setenv("JAVA_HOME", `C:\Program Files\Java\should-not-matter`)
	SetJDKAutoDiscover(false)
	defer SetJDKAutoDiscover(true)
	if got := FindJDKSrcZip(); got != "" {
		t.Fatalf("expected empty with auto off, got %s", got)
	}
}

func TestJDKHomeFromSrcZip(t *testing.T) {
	got := JDKHomeFromSrcZip(`D:\sdk\jdk-17\lib\src.zip`)
	want := filepath.Clean(`D:\sdk\jdk-17`)
	if got != want {
		t.Fatalf("lib layout: got %s want %s", got, want)
	}
	got = JDKHomeFromSrcZip(`D:\sdk\jdk-17\src.zip`)
	want = filepath.Clean(`D:\sdk\jdk-17`)
	if got != want {
		t.Fatalf("root layout: got %s want %s", got, want)
	}
}
