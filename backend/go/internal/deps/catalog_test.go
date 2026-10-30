package deps

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseGradleVersionCatalog(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "libs.versions.toml")
	content := `
[versions]
springBoot = "3.2.0"
jackson = "2.16.0"

[libraries]
spring-boot = { module = "org.springframework.boot:spring-boot", version.ref = "springBoot" }
jackson-core = { group = "com.fasterxml.jackson.core", name = "jackson-core", version.ref = "jackson" }
guava = "com.google.guava:guava:32.0.0"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	coords := parseGradleVersionCatalog(path)
	want := map[string]bool{
		"org.springframework.boot:spring-boot:3.2.0":                false,
		"com.fasterxml.jackson.core:jackson-core:2.16.0":           false,
		"com.google.guava:guava:32.0.0":                            false,
	}
	for _, c := range coords {
		key := c.Group + ":" + c.Artifact + ":" + c.Version
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for k, ok := range want {
		if !ok {
			t.Fatalf("missing coord %s in %#v", k, coords)
		}
	}
}
