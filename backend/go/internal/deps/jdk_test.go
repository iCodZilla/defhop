package deps

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// realJDKSrcZip finds a machine JDK for tests that need real sources.
// Prefers the largest src.zip so tiny demo zips (e.g. jpda) are ignored.
func realJDKSrcZip(t *testing.T) string {
	t.Helper()
	var best string
	var bestSize int64
	consider := func(p string) {
		p = filepath.Clean(p)
		st, err := os.Stat(p)
		if err != nil || st.IsDir() || st.Size() < 5*1024*1024 {
			return
		}
		if st.Size() > bestSize {
			bestSize = st.Size()
			best = p
		}
	}
	for _, env := range []string{"JAVA_HOME", "JDK_HOME"} {
		if v := os.Getenv(env); v != "" {
			consider(filepath.Join(v, "lib", "src.zip"))
			consider(filepath.Join(v, "src.zip"))
		}
	}
	if javaBin, err := exec.LookPath("java"); err == nil {
		dir := filepath.Dir(javaBin)
		consider(filepath.Join(dir, "..", "lib", "src.zip"))
		consider(filepath.Join(dir, "..", "src.zip"))
	}
	for _, root := range []string{`D:\dev\sdk\java`, `C:\Program Files\Java`} {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d == nil {
				return nil
			}
			if !d.IsDir() && strings.EqualFold(d.Name(), "src.zip") {
				consider(path)
			}
			if d.IsDir() {
				rel, _ := filepath.Rel(root, path)
				if strings.Count(rel, string(os.PathSeparator)) > 3 {
					return filepath.SkipDir
				}
			}
			return nil
		})
	}
	return best
}

func TestLocateSimpleDateFormat(t *testing.T) {
	z := realJDKSrcZip(t)
	if z == "" {
		t.Skip("no JDK src.zip on this machine")
	}
	SetJDKHome(z)
	defer SetJDKHome("")
	path, ok := LocateJDKClass("java.text.SimpleDateFormat")
	if !ok || path == "" {
		t.Fatalf("LocateJDKClass failed; src.zip=%s", z)
	}
	tmp := t.TempDir()
	sf, err := MaterializeFQN(path, false, "java.text.SimpleDateFormat", tmp)
	if err != nil || sf == nil {
		t.Fatalf("materialize: %v", err)
	}
	if !strings.Contains(string(sf.Data), "class SimpleDateFormat") &&
		!strings.Contains(string(sf.Data), "SimpleDateFormat") {
		t.Fatalf("unexpected content: %s", truncate(string(sf.Data), 200))
	}
	t.Log("src.zip=", path, "out=", sf.Path)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
