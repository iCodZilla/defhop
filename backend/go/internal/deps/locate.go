package deps

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// packageGroupHints maps import package prefixes to Maven group path under .m2.
var packageGroupHints = []struct {
	prefix string
	group  string // slash form under m2, e.g. org/apache/httpcomponents
}{
	{"org.apache.http", "org/apache/httpcomponents"},
	{"org.apache.hc", "org/apache/httpcomponents"},
	{"com.baomidou", "com/baomidou"},
	{"org.mybatis", "org/mybatis"},
	{"org.apache.ibatis", "org/apache/ibatis"},
	{"org.springframework", "org/springframework"},
	{"org.slf4j", "org/slf4j"},
	{"org.apache.commons", "org/apache/commons"},
	{"com.fasterxml.jackson", "com/fasterxml/jackson"},
	{"com.google.common", "com/google/guava"},
	{"com.google.guava", "com/google/guava"},
	{"lombok", "org/projectlombok"},
	{"org.mapstruct", "org/mapstruct"},
	{"io.netty", "io/netty"},
	{"redis.clients", "redis/clients"},
	{"com.alibaba", "com/alibaba"},
}

// LocateClassJar finds a local Maven jar that contains fqn (sources preferred).
func LocateClassJar(m2, fqn string) (jarPath string, binary bool) {
	if m2 == "" || fqn == "" || !strings.Contains(fqn, ".") {
		return "", false
	}
	classEntry := strings.ReplaceAll(fqn, ".", "/") + ".class"
	javaEntry := strings.ReplaceAll(fqn, ".", "/") + ".java"

	var dirs []string
	for _, h := range packageGroupHints {
		if strings.HasPrefix(fqn, h.prefix+".") || fqn == h.prefix {
			dirs = append(dirs, filepath.Join(m2, filepath.FromSlash(h.group)))
		}
	}
	// Literal group path from package (org.apache.foo.Bar → org/apache/foo).
	parts := strings.Split(fqn, ".")
	if len(parts) >= 3 {
		dirs = append(dirs, filepath.Join(m2, filepath.Join(parts[:2]...)))
		dirs = append(dirs, filepath.Join(m2, filepath.Join(parts[:3]...)))
	}

	seen := map[string]bool{}
	var srcHit, binHit string
	for _, dir := range dirs {
		key := strings.ToLower(filepath.Clean(dir))
		if seen[key] {
			continue
		}
		seen[key] = true
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			n := strings.ToLower(d.Name())
			if !strings.HasSuffix(n, ".jar") || strings.Contains(n, "-javadoc") {
				return nil
			}
			if strings.HasSuffix(n, "-sources.jar") {
				if jarHasEntry(path, javaEntry) || jarHasEntry(path, classEntry) {
					srcHit = path
					return filepath.SkipAll
				}
				return nil
			}
			if binHit == "" && jarHasEntry(path, classEntry) {
				binHit = path
			}
			return nil
		})
		if srcHit != "" {
			return srcHit, false
		}
	}
	if binHit != "" {
		return binHit, true
	}
	return "", false
}

func jarHasEntry(jarPath, entry string) bool {
	zr, err := zip.OpenReader(jarPath)
	if err != nil {
		return false
	}
	defer zr.Close()
	entry = strings.TrimPrefix(entry, "/")
	for _, f := range zr.File {
		if filepath.ToSlash(f.Name) == entry {
			return true
		}
	}
	return false
}

// MaterializeFQN extracts one type (sources .java or binary .class→stub) into outRoot.
// Prefer a single-entry extract so F12 stays snappy.
func MaterializeFQN(jarPath string, binary bool, fqn, outRoot string) (*SourceFile, error) {
	if jarPath == "" || fqn == "" {
		return nil, nil
	}
	budget := 1
	if sf, err := materializeOne(jarPath, binary, fqn, outRoot, &budget); err == nil && sf != nil {
		return sf, nil
	}
	return nil, nil
}

func materializeOne(jarPath string, binary bool, fqn, outRoot string, budget *int) (*SourceFile, error) {
	zr, err := zip.OpenReader(jarPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	isJDK := strings.EqualFold(filepath.Base(jarPath), "src.zip")
	destRoot := depCacheRoot(outRoot, jarPath, isJDK)
	if binary {
		entry := strings.ReplaceAll(fqn, ".", "/") + ".class"
		for _, f := range zr.File {
			if filepath.ToSlash(f.Name) != entry {
				continue
			}
			data, err := readZipFile(f)
			if err != nil {
				return nil, err
			}
			ci, err := ParseClassFile(data)
			if err != nil || ci == nil {
				return nil, err
			}
			stub := StubJava(ci)
			dest := filepath.Join(destRoot, filepath.FromSlash(strings.ReplaceAll(fqn, ".", "/")+".java"))
			bytes := []byte(stub)
			if err := writeJavaFile(dest, bytes); err != nil {
				return nil, err
			}
			if budget != nil {
				*budget--
			}
			return &SourceFile{Path: dest, Data: bytes}, nil
		}
		return nil, nil
	}
	// JDK src.zip may use module prefix: java.base/java/text/SimpleDateFormat.java
	rel := strings.ReplaceAll(fqn, ".", "/") + ".java"
	want := []string{rel, "java.base/" + rel, "java.desktop/" + rel, "java.xml/" + rel}
	for _, f := range zr.File {
		name := filepath.ToSlash(f.Name)
		ok := false
		for _, w := range want {
			if name == w || strings.HasSuffix(name, "/"+rel) {
				ok = true
				break
			}
		}
		if !ok {
			continue
		}
		data, err := readZipFile(f)
		if err != nil {
			return nil, err
		}
		dest := filepath.Join(destRoot, filepath.FromSlash(rel))
		if err := writeJavaFile(dest, data); err != nil {
			return nil, err
		}
		if budget != nil {
			*budget--
		}
		return &SourceFile{Path: dest, Data: data}, nil
	}
	return nil, nil
}

// LocateJDKClass returns the JDK src.zip (or empty) for java.*/javax.* types.
func LocateJDKClass(fqn string) (zipPath string, ok bool) {
	if !strings.HasPrefix(fqn, "java.") && !strings.HasPrefix(fqn, "javax.") &&
		!strings.HasPrefix(fqn, "jdk.") && !strings.HasPrefix(fqn, "com.sun.") {
		return "", false
	}
	z := FindJDKSrcZip()
	if z == "" {
		return "", false
	}
	rel := strings.ReplaceAll(fqn, ".", "/") + ".java"
	zr, err := zip.OpenReader(z)
	if err != nil {
		return "", false
	}
	defer zr.Close()
	for _, f := range zr.File {
		name := filepath.ToSlash(f.Name)
		if name == rel || strings.HasSuffix(name, "/"+rel) {
			return z, true
		}
	}
	return "", false
}

func sha256Short(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}
