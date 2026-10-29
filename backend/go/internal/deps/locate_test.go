package deps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocateRequestConfig(t *testing.T) {
	m2 := MavenLocalRepo()
	jar, binary := LocateClassJar(m2, "org.apache.http.client.config.RequestConfig")
	if jar == "" {
		t.Skip("httpclient not in m2")
	}
	if !strings.Contains(strings.ToLower(jar), "httpclient") {
		t.Fatalf("unexpected jar %s", jar)
	}
	tmp := t.TempDir()
	sf, err := MaterializeFQN(jar, binary, "org.apache.http.client.config.RequestConfig", tmp)
	if err != nil || sf == nil {
		t.Fatalf("materialize: %v %#v", err, sf)
	}
	if !strings.Contains(string(sf.Data), "RequestConfig") {
		t.Fatalf("bad stub: %s", sf.Data)
	}
	if _, err := os.Stat(sf.Path); err != nil {
		t.Fatal(err)
	}
	t.Log("jar=", jar, "binary=", binary, "path=", sf.Path)
}

func TestLocateUsesHintPath(t *testing.T) {
	m2 := MavenLocalRepo()
	hint := filepath.Join(m2, "org", "apache", "httpcomponents")
	if _, err := os.Stat(hint); err != nil {
		t.Skip("no httpcomponents in m2")
	}
	jar, _ := LocateClassJar(m2, "org.apache.http.client.config.RequestConfig")
	if jar == "" {
		t.Fatal("expected locate via packageGroupHints")
	}
}
