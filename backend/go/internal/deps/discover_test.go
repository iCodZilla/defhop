package deps

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePomAndMaterialize(t *testing.T) {
	tmp := t.TempDir()
	m2 := filepath.Join(tmp, "m2")
	groupDir := filepath.Join(m2, "com", "example", "libdemo", "1.0.0")
	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jarPath := filepath.Join(groupDir, "libdemo-1.0.0-sources.jar")
	if err := writeSourcesJar(jarPath, map[string]string{
		"com/example/LibDemo.java": "package com.example;\npublic class LibDemo {\n  public void hello() {}\n}\n",
	}); err != nil {
		t.Fatal(err)
	}

	ws := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	pom := `<?xml version="1.0"?>
<project>
  <modelVersion>4.0.0</modelVersion>
  <groupId>t</groupId><artifactId>t</artifactId><version>1</version>
  <dependencies>
    <dependency>
      <groupId>com.example</groupId>
      <artifactId>libdemo</artifactId>
      <version>1.0.0</version>
    </dependency>
  </dependencies>
</project>`
	if err := os.WriteFile(filepath.Join(ws, "pom.xml"), []byte(pom), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DEFHOP_M2", m2)
	t.Setenv("JAVA_HOME", filepath.Join(tmp, "no-jdk"))
	SetJDKHome(filepath.Join(tmp, "no-jdk"))
	defer SetJDKHome("")

	jars := DiscoverSourcesJars([]string{ws}, map[string]int{"com.example": 1})
	var lib *JarRef
	for i := range jars {
		if strings.Contains(jars[i].Path, "libdemo") {
			lib = &jars[i]
			break
		}
	}
	if lib == nil {
		t.Fatalf("expected libdemo jar, got %#v", jars)
	}
	budget := 100
	files, err := MaterializeJar(*lib, filepath.Join(tmp, "out"), &budget)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 source, got %d", len(files))
	}
	if !strings.Contains(string(files[0].Data), "class LibDemo") {
		t.Fatalf("bad content: %s", files[0].Data)
	}
	// Second materialize should reuse disk file.
	budget = 100
	files2, err := MaterializeJar(*lib, filepath.Join(tmp, "out"), &budget)
	if err != nil || len(files2) != 1 {
		t.Fatalf("reuse failed: %v n=%d", err, len(files2))
	}
}

func TestJDKFilter(t *testing.T) {
	if !jdkEntryAllowed("java.base/java/util/List.java") {
		t.Fatal("expected allow List")
	}
	if jdkEntryAllowed("java.desktop/java/awt/Frame.java") {
		t.Fatal("expected deny awt")
	}
}

func writeSourcesJar(path string, files map[string]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte(body)); err != nil {
			return err
		}
	}
	return zw.Close()
}
