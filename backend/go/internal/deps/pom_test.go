package deps

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPomDependencyManagement(t *testing.T) {
	tmp := t.TempDir()
	parent := filepath.Join(tmp, "pom.xml")
	childDir := filepath.Join(tmp, "mod")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	parentPom := `<?xml version="1.0"?>
<project>
  <properties><lib.version>1.2.3</lib.version></properties>
  <dependencyManagement>
    <dependencies>
      <dependency>
        <groupId>com.example</groupId>
        <artifactId>libdemo</artifactId>
        <version>${lib.version}</version>
      </dependency>
    </dependencies>
  </dependencyManagement>
</project>`
	childPom := `<?xml version="1.0"?>
<project>
  <parent>
    <groupId>t</groupId><artifactId>t</artifactId><version>1</version>
    <relativePath>../pom.xml</relativePath>
  </parent>
  <dependencies>
    <dependency>
      <groupId>com.example</groupId>
      <artifactId>libdemo</artifactId>
    </dependency>
  </dependencies>
</project>`
	if err := os.WriteFile(parent, []byte(parentPom), 0o644); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(childDir, "pom.xml")
	if err := os.WriteFile(child, []byte(childPom), 0o644); err != nil {
		t.Fatal(err)
	}
	coords := parsePomCoords(child)
	if len(coords) != 1 {
		t.Fatalf("expected 1 coord, got %#v", coords)
	}
	if coords[0].Version != "1.2.3" {
		t.Fatalf("expected version from DM+props, got %q", coords[0].Version)
	}
}
