package hub

import (
	"defhop/internal/deps"
	"defhop/internal/java"
	"defhop/internal/model"
	"defhop/internal/store"
	"path/filepath"
	"strings"
	"sync"
)

// fqnLocateCache avoids repeated m2 walks for the same external type.
var fqnLocateCache sync.Map // fqn -> jarPath\x00bin|"miss"

// ClearExternalLocateCache drops cached jar/JDK lookups (call after JDK home changes).
func (h *Hub) ClearExternalLocateCache() {
	fqnLocateCache.Range(func(k, _ any) bool {
		fqnLocateCache.Delete(k)
		return true
	})
}

// HintConfigureJDK is returned via navMenu when a java.* DEF needs a JDK path.
const HintConfigureJDK = "configure-jdk"

func isJDKFQN(fqn string) bool {
	return strings.HasPrefix(fqn, "java.") || strings.HasPrefix(fqn, "javax.") ||
		strings.HasPrefix(fqn, "jdk.") || strings.HasPrefix(fqn, "com.sun.")
}

// jdkFQNOf returns the JDK type FQN for a symbol (type itself or its owner).
func jdkFQNOf(sym *model.Symbol) string {
	if sym == nil {
		return ""
	}
	if strings.HasPrefix(sym.Key, "T|") {
		fqn := strings.TrimPrefix(sym.Key, "T|")
		if isJDKFQN(fqn) {
			return fqn
		}
	}
	if sym.OwnerKey != "" {
		fqn := strings.TrimPrefix(sym.OwnerKey, "T|")
		if isJDKFQN(fqn) {
			return fqn
		}
	}
	if sym.Kind == "type" && isJDKFQN(sym.Name) {
		return sym.Name
	}
	return ""
}

// jdkConfigHint returns HintConfigureJDK when a JDK type cannot be resolved
// because no src.zip / jdkHome is available.
func jdkConfigHint(fqn string) string {
	if !isJDKFQN(fqn) {
		return ""
	}
	if deps.FindJDKSrcZip() != "" {
		return "" // JDK present but this type missing — don't nag about settings
	}
	return HintConfigureJDK
}

// ensureExternalType materializes a library type from ~/.m2 (sources or binary stub)
// when it is missing from the workspace index — e.g. RequestConfig via import.
func (h *Hub) ensureExternalType(fqn string) *model.Symbol {
	fqn = strings.TrimSpace(fqn)
	fqn = strings.TrimPrefix(fqn, "T|")
	if fqn == "" || !strings.Contains(fqn, ".") {
		return nil
	}
	key := "T|" + fqn
	// JDK types require an explicit jdkHome — ignore stale SymTab / cache hits.
	if isJDKFQN(fqn) && deps.FindJDKSrcZip() == "" {
		return nil
	}
	if snap := h.Index.Snapshot(); snap != nil {
		if s := snap.SymTab[key]; s != nil {
			return s
		}
	}
	cacheDir := h.CacheDir
	if cacheDir == "" {
		return nil
	}
	outRoot := filepath.Join(cacheDir, "deps")

	// JDK types: pull from configured src.zip on demand.
	if isJDKFQN(fqn) {
		if z, ok := deps.LocateJDKClass(fqn); ok {
			sf, err := deps.MaterializeFQN(z, false, fqn, outRoot)
			if err == nil && sf != nil && len(sf.Data) > 0 {
				return h.publishExternalFile(sf, key)
			}
		}
		return nil
	}
	if strings.HasPrefix(fqn, "jakarta.") {
		// fall through to m2
	}

	m2 := depsMavenRepo()
	jar, binary := locateCached(m2, fqn)
	if jar == "" {
		return nil
	}
	sf, err := deps.MaterializeFQN(jar, binary, fqn, outRoot)
	if err != nil || sf == nil || len(sf.Data) == 0 {
		return nil
	}
	return h.publishExternalFile(sf, key)
}

func (h *Hub) publishExternalFile(sf *deps.SourceFile, typeKey string) *model.Symbol {
	fd := parseSafe(sf.Path, string(sf.Data), 0)
	if fd == nil {
		return nil
	}
	fd.ContentHash = store.ContentHash(sf.Data)
	h.Index.PutFile(fd)
	if snap := h.Index.Snapshot(); snap != nil {
		return snap.SymTab[typeKey]
	}
	return nil
}

func depsMavenRepo() string {
	// Same resolution as deps.mavenLocalRepo without exporting it.
	return deps.MavenLocalRepo()
}

func locateCached(m2, fqn string) (jar string, binary bool) {
	if v, ok := fqnLocateCache.Load(fqn); ok {
		s := v.(string)
		if s == "miss" {
			return "", false
		}
		parts := strings.SplitN(s, "\x00", 2)
		if len(parts) == 2 {
			return parts[0], parts[1] == "1"
		}
		return parts[0], false
	}
	jar, binary = deps.LocateClassJar(m2, fqn)
	if jar == "" {
		fqnLocateCache.Store(fqn, "miss")
		return "", false
	}
	flag := "0"
	if binary {
		flag = "1"
	}
	fqnLocateCache.Store(fqn, jar+"\x00"+flag)
	return jar, binary
}

// externalFQNFromContext resolves the library FQN under the cursor when SymTab misses.
func externalFQNFromContext(snap *store.Snapshot, fd *model.FileSlice, offset int, token, fallback string, sym *model.Symbol) string {
	if fd == nil {
		return ""
	}
	if sym != nil {
		if sym.Kind == "type" && strings.Contains(sym.Name, ".") {
			return sym.Name
		}
		if sym.Key != "" && strings.HasPrefix(sym.Key, "T|") && strings.Contains(sym.Key, ".") {
			if snap == nil || snap.SymTab[sym.Key] == nil || sym.Declaration == nil {
				return strings.TrimPrefix(sym.Key, "T|")
			}
		}
	}
	occ := java.OccAt(fd, offset, token)
	if occ != nil && strings.HasPrefix(occ.TargetKey, "T|") && strings.Contains(occ.TargetKey, ".") {
		tk := occ.TargetKey
		if snap == nil || snap.SymTab[tk] == nil {
			return strings.TrimPrefix(tk, "T|")
		}
	}
	name := token
	if name == "" {
		name = fallback
	}
	if name == "" || name[0] < 'A' || name[0] > 'Z' {
		return ""
	}
	for _, imp := range fd.Imports {
		imp = strings.TrimSuffix(imp, ".*")
		if strings.HasSuffix(imp, "."+name) {
			return imp
		}
	}
	return ""
}
