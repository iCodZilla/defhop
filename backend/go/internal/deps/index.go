package deps

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// jdkAllow prefixes keep only high-value JDK packages (path inside src.zip).
var jdkAllow = []string{
	"java.base/java/lang/",
	"java.base/java/util/",
	"java.base/java/io/",
	"java.base/java/nio/",
	"java.base/java/time/",
	"java.base/java/math/",
	"java.base/java/net/",
	"java.base/java/text/",
	"java.base/java/util/concurrent/",
	"java.base/java/util/function/",
	"java.base/java/util/stream/",
	"java.base/java/util/regex/",
	"java.base/java/lang/reflect/",
	"java.base/java/lang/annotation/",
	// Older src.zip layout (no module prefix):
	"java/lang/",
	"java/util/",
	"java/io/",
	"java/nio/",
	"java/time/",
	"java/math/",
	"java/net/",
	"java/text/",
}

// SourceFile is one .java entry materialized under the cache tree.
type SourceFile struct {
	Path string // absolute path on disk (under cacheDir/deps/...)
	Data []byte
}

// MaterializeJar extracts selected .java entries from a sources jar/zip into
// outRoot/<label>-<hash>/... and returns paths + bytes for parsing.
// When a stamp matches the jar mtime/size, reuses the on-disk tree (no zip read).
func MaterializeJar(ref JarRef, outRoot string, budget *int) ([]SourceFile, error) {
	if budget != nil && *budget <= 0 {
		return nil, nil
	}
	destRoot := depCacheRoot(outRoot, ref.Path, ref.IsJDK || ref.Label == "jdk")
	stampPath := destRoot + ".stamp"

	maxFiles := MaxFilesPerJar
	if ref.IsJDK {
		maxFiles = MaxJDKFiles
	}

	jarStat, err := os.Stat(ref.Path)
	if err != nil {
		return nil, err
	}
	kind := "src"
	if ref.Binary {
		kind = "bin"
	}
	stamp := fmt.Sprintf("%d:%d:%s", jarStat.Size(), jarStat.ModTime().UnixNano(), kind)
	if prev, err := os.ReadFile(stampPath); err == nil && string(prev) == stamp {
		if reused := loadExistingSources(destRoot, maxFiles, budget); len(reused) > 0 {
			return reused, nil
		}
	}

	zr, err := zip.OpenReader(ref.Path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	var out []SourceFile
	if ref.Binary {
		out, err = materializeBinary(zr, destRoot, maxFiles, budget)
	} else {
		out, err = materializeSources(zr, ref, destRoot, maxFiles, budget)
	}
	if err != nil {
		return out, err
	}
	if len(out) > 0 {
		_ = os.WriteFile(stampPath, []byte(stamp), 0o644)
	}
	return out, nil
}

func materializeSources(zr *zip.ReadCloser, ref JarRef, destRoot string, maxFiles int, budget *int) ([]SourceFile, error) {
	var out []SourceFile
	for _, f := range zr.File {
		if budget != nil && *budget <= 0 {
			break
		}
		if len(out) >= maxFiles {
			break
		}
		name := filepath.ToSlash(f.Name)
		if !strings.HasSuffix(strings.ToLower(name), ".java") {
			continue
		}
		if strings.Contains(name, "module-info.java") || strings.Contains(name, "package-info.java") {
			continue
		}
		if ref.IsJDK && !jdkEntryAllowed(name) {
			continue
		}

		rel := name
		if ref.IsJDK {
			rel = stripModulePrefix(name)
		}
		dest := filepath.Join(destRoot, filepath.FromSlash(rel))
		data, err := readZipFile(f)
		if err != nil || len(data) == 0 {
			continue
		}
		if err := writeJavaFile(dest, data); err != nil {
			continue
		}
		out = append(out, SourceFile{Path: dest, Data: data})
		if budget != nil {
			*budget--
		}
	}
	return out, nil
}

func materializeBinary(zr *zip.ReadCloser, destRoot string, maxFiles int, budget *int) ([]SourceFile, error) {
	var out []SourceFile
	for _, f := range zr.File {
		if budget != nil && *budget <= 0 {
			break
		}
		if len(out) >= maxFiles {
			break
		}
		name := filepath.ToSlash(f.Name)
		if !strings.HasSuffix(name, ".class") {
			continue
		}
		base := filepath.Base(name)
		// Skip anonymous / synthetic-looking inner classes (Foo$1.class).
		if strings.Contains(base, "$") {
			inner := base[strings.Index(base, "$")+1:]
			if len(inner) > 0 && inner[0] >= '0' && inner[0] <= '9' {
				continue
			}
		}
		data, err := readZipFile(f)
		if err != nil || len(data) == 0 {
			continue
		}
		ci, err := ParseClassFile(data)
		if err != nil || ci == nil || !ci.IsPublic || ci.BinaryName == "" {
			continue
		}
		stub := StubJava(ci)
		if stub == "" {
			continue
		}
		rel := strings.ReplaceAll(ci.BinaryName, ".", "/") + ".java"
		dest := filepath.Join(destRoot, filepath.FromSlash(rel))
		bytes := []byte(stub)
		if err := writeJavaFile(dest, bytes); err != nil {
			continue
		}
		out = append(out, SourceFile{Path: dest, Data: bytes})
		if budget != nil {
			*budget--
		}
	}
	return out, nil
}

func loadExistingSources(destRoot string, maxFiles int, budget *int) []SourceFile {
	var out []SourceFile
	_ = filepath.WalkDir(destRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if budget != nil && *budget <= 0 {
			return filepath.SkipAll
		}
		if len(out) >= maxFiles {
			return filepath.SkipAll
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".java") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			return nil
		}
		out = append(out, SourceFile{Path: path, Data: data})
		if budget != nil {
			*budget--
		}
		return nil
	})
	return out
}

func jdkEntryAllowed(name string) bool {
	n := strings.TrimPrefix(name, "/")
	for _, p := range jdkAllow {
		if strings.HasPrefix(n, p) {
			// Only top-level types under that prefix (avoid deep impl noise? keep all under prefix)
			return true
		}
	}
	return false
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	// Cap single file size (defensive).
	const max = 2 << 20 // 2 MiB
	return io.ReadAll(io.LimitReader(rc, max))
}

func needWrite(dest string, data []byte) bool {
	st, err := os.Stat(dest)
	if err != nil {
		return true
	}
	if st.IsDir() {
		return true
	}
	if st.Size() != int64(len(data)) {
		return true
	}
	existing, err := os.ReadFile(dest)
	if err != nil {
		return true
	}
	return string(existing) != string(data)
}

// depCacheRoot is shared by batch + on-demand extracts for the same archive.
func depCacheRoot(outRoot, jarPath string, isJDK bool) string {
	sum := sha256.Sum256([]byte(jarPath))
	id := hex.EncodeToString(sum[:8])
	label := sanitizeLabel(filepath.Base(jarPath))
	if isJDK || strings.EqualFold(filepath.Base(jarPath), "src.zip") {
		label = "jdk"
	}
	return filepath.Join(outRoot, label+"-"+id)
}

// stripModulePrefix maps java.base/java/text/Foo.java → java/text/Foo.java.
func stripModulePrefix(name string) string {
	name = strings.TrimPrefix(filepath.ToSlash(name), "/")
	if i := strings.Index(name, "/"); i > 0 {
		first := name[:i]
		if strings.HasPrefix(first, "java.") || strings.HasPrefix(first, "jdk.") {
			return name[i+1:]
		}
	}
	return name
}

// writeJavaFile writes data to dest as a regular file. If dest is a directory
// (corrupt cache from path clashes), it is removed first.
func writeJavaFile(dest string, data []byte) error {
	if !needWrite(dest, data) {
		return nil
	}
	if st, err := os.Stat(dest); err == nil && st.IsDir() {
		if err := os.RemoveAll(dest); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	// Parent path must not be a file left from a bad extract.
	dir := filepath.Dir(dest)
	if st, err := os.Stat(dir); err == nil && !st.IsDir() {
		if err := os.Remove(dir); err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(dest, data, 0o644)
}

// Progress message helper for stderr callers.
func FormatSummary(jars, files int) string {
	return fmt.Sprintf("deps: %d jars → %d sources", jars, files)
}
