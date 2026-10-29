package hub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"defhop/internal/deps"
	"defhop/internal/model"
)

func TestEnsureExternalRequestConfig(t *testing.T) {
	m2 := deps.MavenLocalRepo()
	jar, _ := deps.LocateClassJar(m2, "org.apache.http.client.config.RequestConfig")
	if jar == "" {
		t.Skip("httpclient not in m2")
	}
	tmp := t.TempDir()
	h := New()
	h.CacheDir = tmp
	src := `package demo;
import org.apache.http.client.config.RequestConfig;
public class HttpClientUtils {
  private static RequestConfig requestConfig = null;
}
`
	path := filepath.Join(tmp, "HttpClientUtils.java")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, _, err := h.InitRoots(context.Background(), []string{tmp}, filepath.Join(tmp, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	h.CacheDir = filepath.Join(tmp, "cache")

	line := 3
	col := strings.Index(strings.Split(src, "\n")[line], "RequestConfig")
	if col < 0 {
		t.Fatal("needle missing")
	}
	locs, _ := h.Navigate(model.NavRequest{
		Kind: model.NavDef, Path: path, Text: src, Line: line, Character: col,
	})
	if len(locs) == 0 {
		t.Fatal("expected DEF to RequestConfig stub")
	}
	if !strings.Contains(locs[0].Path, "RequestConfig.java") {
		t.Fatalf("got %s", locs[0].Path)
	}
}
