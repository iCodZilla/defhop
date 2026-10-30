package deps

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	jdkHomeMu         sync.RWMutex
	jdkHomePref       string // explicit preference (project setting via extension)
	jdkAutoDiscoverOn = true
)

// SetJDKHome sets the preferred JDK root (or direct path to src.zip).
// Empty clears the preference so env / auto-discovery may apply.
func SetJDKHome(home string) {
	jdkHomeMu.Lock()
	jdkHomePref = strings.TrimSpace(home)
	jdkHomeMu.Unlock()
}

// JDKHome returns the configured preference (may be empty).
func JDKHome() string {
	jdkHomeMu.RLock()
	defer jdkHomeMu.RUnlock()
	return jdkHomePref
}

// SetJDKAutoDiscover toggles JAVA_HOME / disk fallbacks (tests may disable).
func SetJDKAutoDiscover(on bool) {
	jdkHomeMu.Lock()
	jdkAutoDiscoverOn = on
	jdkHomeMu.Unlock()
}

func jdkAutoDiscoverEnabled() bool {
	jdkHomeMu.RLock()
	defer jdkHomeMu.RUnlock()
	return jdkAutoDiscoverOn
}

func configuredSrcZipCandidates() []string {
	jdkHomeMu.RLock()
	pref := jdkHomePref
	jdkHomeMu.RUnlock()
	if pref == "" {
		if v := os.Getenv("DEFHOP_JDK_HOME"); v != "" {
			pref = strings.TrimSpace(v)
		}
	}
	if pref == "" {
		return nil
	}
	return srcZipCandidatesForHome(pref)
}

func srcZipCandidatesForHome(pref string) []string {
	pref = filepath.Clean(strings.TrimSpace(pref))
	if pref == "" {
		return nil
	}
	var out []string
	if strings.EqualFold(filepath.Base(pref), "src.zip") {
		out = append(out, pref)
		return out
	}
	out = append(out,
		filepath.Join(pref, "lib", "src.zip"),
		filepath.Join(pref, "src.zip"),
		pref,
	)
	return out
}

// validSrcZip returns p if it looks like a usable sources archive.
func validSrcZip(p string) string {
	p = filepath.Clean(p)
	st, err := os.Stat(p)
	if err != nil || st.IsDir() || st.Size() < 1024 {
		return ""
	}
	return p
}

// JDKHomeFromSrcZip maps .../lib/src.zip or .../src.zip → JDK root (or the zip itself).
func JDKHomeFromSrcZip(zipPath string) string {
	zipPath = filepath.Clean(zipPath)
	if zipPath == "" {
		return ""
	}
	if strings.EqualFold(filepath.Base(zipPath), "src.zip") {
		dir := filepath.Dir(zipPath)
		if strings.EqualFold(filepath.Base(dir), "lib") {
			return filepath.Dir(dir)
		}
		return dir
	}
	return zipPath
}
