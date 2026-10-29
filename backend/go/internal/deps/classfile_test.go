package deps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBinaryHttpClientRequestConfig(t *testing.T) {
	home, _ := os.UserHomeDir()
	jar := filepath.Join(home, ".m2", "repository", "org", "apache", "httpcomponents",
		"httpclient", "4.5.13", "httpclient-4.5.13.jar")
	if _, err := os.Stat(jar); err != nil {
		t.Skip("httpclient jar not in local m2")
	}
	tmp := t.TempDir()
	budget := 500
	files, err := MaterializeJar(JarRef{Path: jar, Label: "httpclient", Binary: true}, tmp, &budget)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("expected stub sources from binary jar")
	}
	found := false
	for _, f := range files {
		if strings.Contains(f.Path, "RequestConfig.java") || strings.Contains(string(f.Data), "class RequestConfig") {
			found = true
			if !strings.Contains(string(f.Data), "package org.apache.http.client.config") {
				t.Fatalf("bad stub package: %s", f.Data[:min(120, len(f.Data))])
			}
			break
		}
	}
	if !found {
		t.Fatalf("RequestConfig stub missing among %d files", len(files))
	}
}

func TestParseAndStubRoundTrip(t *testing.T) {
	// Minimal synthetic: reuse a tiny jar from writeSourcesJar style but with .class — skip if none.
	// Unit-test StubJava shape only.
	ci := &ClassInfo{
		BinaryName: "com.example.LibDemo",
		IsPublic:   true,
		Methods: []MemberInfo{
			{Name: "<init>", Descriptor: "()V"},
			{Name: "hello", Descriptor: "()V", IsStatic: false},
			{Name: "custom", Descriptor: "()Lcom/example/LibDemo;", IsStatic: true},
		},
		Fields: []MemberInfo{
			{Name: "DEFAULT", Descriptor: "Lcom/example/LibDemo;", IsStatic: true},
		},
	}
	stub := StubJava(ci)
	if !strings.Contains(stub, "package com.example;") || !strings.Contains(stub, "class LibDemo") {
		t.Fatal(stub)
	}
	if !strings.Contains(stub, "void hello(") || !strings.Contains(stub, "static LibDemo custom(") {
		t.Fatal(stub)
	}
}

func TestDiscoverFallsBackToBinary(t *testing.T) {
	home, _ := os.UserHomeDir()
	m2 := filepath.Join(home, ".m2", "repository")
	jar := filepath.Join(m2, "org", "apache", "httpcomponents", "httpclient", "4.5.13", "httpclient-4.5.13.jar")
	if _, err := os.Stat(jar); err != nil {
		t.Skip("no httpclient in m2")
	}
	tmp := t.TempDir()
	pom := `<?xml version="1.0"?><project>
  <dependencies>
    <dependency>
      <groupId>org.apache.httpcomponents</groupId>
      <artifactId>httpclient</artifactId>
      <version>4.5.13</version>
    </dependency>
  </dependencies>
</project>`
	if err := os.WriteFile(filepath.Join(tmp, "pom.xml"), []byte(pom), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEFHOP_M2", m2)
	t.Setenv("JAVA_HOME", filepath.Join(tmp, "nojdk"))
	jars := DiscoverSourcesJars([]string{tmp}, map[string]int{"org.apache.http": 1})
	if len(jars) == 0 {
		t.Fatal("expected httpclient jar")
	}
	hit := false
	for _, j := range jars {
		if strings.Contains(j.Path, "httpclient") && j.Binary {
			hit = true
		}
	}
	if !hit {
		// sources.jar may exist on some machines — still OK if non-binary found
		for _, j := range jars {
			if strings.Contains(j.Path, "httpclient") {
				hit = true
			}
		}
	}
	if !hit {
		t.Fatalf("httpclient not discovered: %#v", jars)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
