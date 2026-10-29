// Package deps discovers and indexes Java dependency sources (Maven/Gradle
// *-sources.jar and JDK src.zip) without a JVM.
package deps

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Limits keep dependency indexing bounded on large classpaths.
const (
	MaxJars          = 80
	MaxFilesPerJar   = 400
	MaxTotalFiles    = 4000
	MaxJDKFiles      = 1200
)

// JarRef is one sources or binary archive to index.
type JarRef struct {
	Path   string // filesystem path to .jar / .zip
	Label  string // short id for cache folder (artifact or "jdk")
	IsJDK  bool
	Binary bool // true = .class jar (no sources); synthesize stubs
	Prefer bool // score boost (matches workspace imports)
}

var (
	reMavenCoord  = regexp.MustCompile(`(?s)<groupId>\s*([^<]+?)\s*</groupId>\s*<artifactId>\s*([^<]+?)\s*</artifactId>(?:\s*<version>\s*([^<]+?)\s*</version>)?`)
	reGradleCoord = regexp.MustCompile(`(?:"|')([A-Za-z0-9_.-]+):([A-Za-z0-9_.-]+):([A-Za-z0-9._-]+)(?:"|')`)
	rePomProp     = regexp.MustCompile(`(?s)<properties>(.*?)</properties>`)
	rePropEntry   = regexp.MustCompile(`<(?:[A-Za-z0-9_.-]+:)?([A-Za-z0-9_.-]+)>\s*([^<]+?)\s*</`)
	reDepMgmt     = regexp.MustCompile(`(?s)<dependencyManagement>(.*?)</dependencyManagement>`)
	reDepsBlock   = regexp.MustCompile(`(?s)<dependencies>(.*?)</dependencies>`)
)

// DiscoverSourcesJars finds dependency source archives for workspace roots.
// Prefer jars whose coordinates match ImportHits prefixes when provided.
func DiscoverSourcesJars(roots []string, importHits map[string]int) []JarRef {
	seen := map[string]bool{}
	var out []JarRef

	add := func(r JarRef) {
		if r.Path == "" {
			return
		}
		key := strings.ToLower(filepath.Clean(r.Path))
		if seen[key] {
			return
		}
		if _, err := os.Stat(r.Path); err != nil {
			return
		}
		seen[key] = true
		out = append(out, r)
	}

	// 1) JDK sources first (highest navigation value for java.* types).
	if jdk := findJDKSrcZip(); jdk != "" {
		add(JarRef{Path: jdk, Label: "jdk", IsJDK: true, Prefer: true})
	}

	// 2) Explicit workspace lib / .defhop-deps folders (shallow — avoid walking the whole tree).
	for _, root := range roots {
		for _, dir := range []string{
			filepath.Join(root, "lib"),
			filepath.Join(root, ".defhop-deps"),
		} {
			_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return filepath.SkipDir
				}
				if d.IsDir() {
					// Only one level under lib / .defhop-deps.
					if path != dir {
						return filepath.SkipDir
					}
					return nil
				}
				lower := strings.ToLower(d.Name())
				if strings.HasSuffix(lower, "-sources.jar") || strings.HasSuffix(lower, "-sources.zip") {
					add(JarRef{Path: path, Label: jarLabel(d.Name()), Prefer: true})
				}
				return nil
			})
		}
	}

	m2 := mavenLocalRepo()
	gradleCaches := gradleModulesCache()

	coords := collectCoords(roots)
	for _, c := range coords {
		prefer := coordPreferred(c, importHits)
		if p, bin := resolveMavenJar(m2, c); p != "" {
			add(JarRef{Path: p, Label: c.Artifact, Binary: bin, Prefer: prefer})
		} else if p, bin := resolveGradleJar(gradleCaches, c); p != "" {
			add(JarRef{Path: p, Label: c.Artifact, Binary: bin, Prefer: prefer})
		}
		if len(out) >= MaxJars+8 { // room before sort/cap
			break
		}
	}

	return rankAndCap(out, MaxJars)
}

type coord struct {
	Group, Artifact, Version string
}

func collectCoords(roots []string) []coord {
	seen := map[string]bool{}
	var out []coord
	add := func(c coord) {
		c.Group = strings.TrimSpace(c.Group)
		c.Artifact = strings.TrimSpace(c.Artifact)
		c.Version = strings.TrimSpace(c.Version)
		if c.Group == "" || c.Artifact == "" || c.Version == "" {
			return
		}
		if strings.Contains(c.Version, "${") {
			return
		}
		key := c.Group + ":" + c.Artifact + ":" + c.Version
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, c)
	}

	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && path != root {
				n := strings.ToLower(d.Name())
				if n == ".git" || n == "node_modules" || n == "target" || n == "build" ||
					n == ".idea" || n == ".gradle" || n == "out" {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			name := strings.ToLower(d.Name())
			switch {
			case name == "pom.xml":
				for _, c := range parsePomCoords(path) {
					add(c)
				}
			case name == "build.gradle" || name == "build.gradle.kts":
				for _, c := range parseGradleCoords(path) {
					add(c)
				}
			}
			return nil
		})
	}
	return out
}

func parsePomCoords(path string) []coord {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	text := string(data)
	props := pomProperties(text)
	// Merge parent POM props/DM when relativePath is present (common multi-module).
	if parent := parentPomPath(path, text); parent != "" {
		if pdata, err := os.ReadFile(parent); err == nil {
			pt := string(pdata)
			for k, v := range pomProperties(pt) {
				if _, ok := props[k]; !ok {
					props[k] = v
				}
			}
			text = pt + "\n" + text // parent DM first, child overrides below
		}
	}
	dm := map[string]string{}
	for _, m := range reDepMgmt.FindAllStringSubmatch(text, -1) {
		if len(m) < 2 {
			continue
		}
		for _, c := range coordsInBlock(m[1], props) {
			if c.Version != "" {
				dm[c.Group+"|"+c.Artifact] = c.Version
			}
		}
	}
	stripped := reDepMgmt.ReplaceAllString(text, "")
	var out []coord
	seen := map[string]bool{}
	for _, m := range reDepsBlock.FindAllStringSubmatch(stripped, -1) {
		if len(m) < 2 {
			continue
		}
		for _, c := range coordsInBlock(m[1], props) {
			if c.Version == "" {
				c.Version = dm[c.Group+"|"+c.Artifact]
			}
			c.Version = resolveProp(c.Version, props)
			if c.Version == "" || strings.Contains(c.Version, "${") {
				continue
			}
			key := c.Group + ":" + c.Artifact + ":" + c.Version
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, c)
		}
	}
	return out
}

func pomProperties(text string) map[string]string {
	props := map[string]string{}
	if m := rePomProp.FindStringSubmatch(text); len(m) == 2 {
		for _, pm := range rePropEntry.FindAllStringSubmatch(m[1], -1) {
			if len(pm) == 3 {
				props[pm[1]] = strings.TrimSpace(pm[2])
			}
		}
	}
	return props
}

func coordsInBlock(block string, props map[string]string) []coord {
	var out []coord
	for _, m := range reMavenCoord.FindAllStringSubmatch(block, -1) {
		if len(m) < 3 {
			continue
		}
		ver := ""
		if len(m) >= 4 {
			ver = strings.TrimSpace(m[3])
		}
		out = append(out, coord{
			Group:    strings.TrimSpace(m[1]),
			Artifact: strings.TrimSpace(m[2]),
			Version:  resolveProp(ver, props),
		})
	}
	return out
}

var reParentRel = regexp.MustCompile(`(?s)<parent>.*?<relativePath>\s*([^<]+?)\s*</relativePath>`)

func parentPomPath(pomPath, text string) string {
	m := reParentRel.FindStringSubmatch(text)
	if len(m) < 2 {
		// Default Maven parent lookup.
		cand := filepath.Join(filepath.Dir(pomPath), "..", "pom.xml")
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			// Only treat as parent when <parent> exists.
			if strings.Contains(text, "<parent>") {
				return filepath.Clean(cand)
			}
		}
		return ""
	}
	rel := strings.TrimSpace(m[1])
	if rel == "" {
		return ""
	}
	cand := filepath.Clean(filepath.Join(filepath.Dir(pomPath), rel))
	if st, err := os.Stat(cand); err == nil && !st.IsDir() {
		return cand
	}
	// relativePath often points at a directory.
	cand2 := filepath.Join(cand, "pom.xml")
	if st, err := os.Stat(cand2); err == nil && !st.IsDir() {
		return cand2
	}
	return ""
}

func parseGradleCoords(path string) []coord {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []coord
	for _, m := range reGradleCoord.FindAllStringSubmatch(string(data), -1) {
		if len(m) == 4 {
			out = append(out, coord{Group: m[1], Artifact: m[2], Version: m[3]})
		}
	}
	return out
}

func resolveProp(v string, props map[string]string) string {
	if !strings.Contains(v, "${") {
		return v
	}
	out := v
	for k, val := range props {
		out = strings.ReplaceAll(out, "${"+k+"}", val)
	}
	return out
}

// MavenLocalRepo returns the local Maven repository root (~/.m2/repository).
func MavenLocalRepo() string {
	return mavenLocalRepo()
}

func mavenLocalRepo() string {
	if v := os.Getenv("DEFHOP_M2"); v != "" {
		return v
	}
	if v := os.Getenv("M2_REPO"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".m2", "repository")
}

func gradleModulesCache() string {
	if v := os.Getenv("DEFHOP_GRADLE_CACHES"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".gradle", "caches", "modules-2", "files-2.1")
}

// resolveMavenJar prefers *-sources.jar; falls back to the binary jar.
func resolveMavenJar(m2 string, c coord) (path string, binary bool) {
	if m2 == "" {
		return "", false
	}
	dir := filepath.Join(append([]string{m2}, strings.Split(c.Group, ".")...)...)
	dir = filepath.Join(dir, c.Artifact, c.Version)
	src := filepath.Join(dir, c.Artifact+"-"+c.Version+"-sources.jar")
	if st, err := os.Stat(src); err == nil && !st.IsDir() {
		return src, false
	}
	bin := filepath.Join(dir, c.Artifact+"-"+c.Version+".jar")
	if st, err := os.Stat(bin); err == nil && !st.IsDir() {
		return bin, true
	}
	return "", false
}

// resolveGradleJar prefers *-sources.jar under the module cache; else any non-javadoc jar.
func resolveGradleJar(cache string, c coord) (path string, binary bool) {
	if cache == "" {
		return "", false
	}
	base := filepath.Join(cache, c.Group, c.Artifact, c.Version)
	var src, bin string
	_ = filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		n := strings.ToLower(d.Name())
		if !strings.HasSuffix(n, ".jar") {
			return nil
		}
		if strings.Contains(n, "-javadoc") {
			return nil
		}
		if strings.HasSuffix(n, "-sources.jar") {
			src = p
			return filepath.SkipAll
		}
		if bin == "" && !strings.Contains(n, "-sources") {
			bin = p
		}
		return nil
	})
	if src != "" {
		return src, false
	}
	if bin != "" {
		return bin, true
	}
	return "", false
}

// FindJDKSrcZip locates JDK sources (lib/src.zip or src.zip).
// Priority: SetJDKHome / DEFHOP_JDK_HOME → JAVA_HOME / JDK_HOME → PATH java → disk walk.
func FindJDKSrcZip() string {
	return findJDKSrcZip()
}

func findJDKSrcZip() string {
	// 1) Explicit preference (project / user setting via extension, or DEFHOP_JDK_HOME).
	for _, p := range configuredSrcZipCandidates() {
		if z := validSrcZip(p); z != "" {
			return z
		}
	}
	if !jdkAutoDiscoverEnabled() {
		return ""
	}

	var candidates []string
	add := func(p string) {
		if p != "" {
			candidates = append(candidates, p)
		}
	}
	// 2) Environment variables.
	for _, env := range []string{"JAVA_HOME", "JDK_HOME"} {
		if v := os.Getenv(env); v != "" {
			add(filepath.Join(v, "lib", "src.zip"))
			add(filepath.Join(v, "src.zip"))
		}
	}
	// 3) Derive from java on PATH.
	if javaBin, err := exec.LookPath("java"); err == nil && javaBin != "" {
		dir := filepath.Dir(javaBin)
		add(filepath.Join(dir, "..", "lib", "src.zip"))
		add(filepath.Join(dir, "..", "src.zip"))
		add(filepath.Join(dir, "..", "..", "lib", "src.zip"))
		add(filepath.Join(dir, "..", "..", "src.zip"))
	}
	// 4) Common install roots (best-effort, capped).
	home, _ := os.UserHomeDir()
	for _, root := range []string{
		`C:\Program Files\Java`,
		`C:\Program Files\Eclipse Adoptium`,
		`C:\Program Files\Microsoft`,
		`C:\Program Files\Amazon Corretto`,
		`D:\dev\sdk\java`,
		filepath.Join(home, "sdk", "java"),
		"/usr/lib/jvm",
		"/Library/Java/JavaVirtualMachines",
	} {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d == nil {
				return nil
			}
			if !d.IsDir() && strings.EqualFold(d.Name(), "src.zip") {
				candidates = append(candidates, path)
				return nil
			}
			if d.IsDir() {
				rel, _ := filepath.Rel(root, path)
				if strings.Count(rel, string(os.PathSeparator)) > 3 {
					return filepath.SkipDir
				}
			}
			return nil
		})
	}
	// Prefer the largest valid src.zip (real JDK sources beat tiny demos).
	var best string
	var bestSize int64
	for _, p := range candidates {
		p = filepath.Clean(p)
		st, err := os.Stat(p)
		if err != nil || st.IsDir() || st.Size() < 100*1024 {
			continue
		}
		if st.Size() > bestSize {
			bestSize = st.Size()
			best = p
		}
	}
	return best
}

func jarLabel(name string) string {
	n := strings.TrimSuffix(name, filepath.Ext(name))
	n = strings.TrimSuffix(n, "-sources")
	if len(n) > 48 {
		n = n[:48]
	}
	return sanitizeLabel(n)
}

func sanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "dep"
	}
	return b.String()
}

func coordPreferred(c coord, importHits map[string]int) bool {
	if len(importHits) == 0 {
		return false
	}
	g := c.Group
	for prefix, n := range importHits {
		if n <= 0 {
			continue
		}
		if strings.HasPrefix(prefix, g) || strings.HasPrefix(g, prefix) ||
			strings.Contains(prefix, c.Artifact) {
			return true
		}
		// org.springframework ↔ spring-*
		if strings.Contains(prefix, "springframework") && strings.Contains(g, "springframework") {
			return true
		}
		if strings.Contains(prefix, "mybatis") && (strings.Contains(g, "mybatis") || strings.Contains(g, "ibatis")) {
			return true
		}
		if strings.Contains(prefix, "lombok") && strings.Contains(g, "lombok") {
			return true
		}
		if strings.Contains(prefix, "apache.http") && strings.Contains(g, "httpcomponents") {
			return true
		}
	}
	return false
}

func rankAndCap(in []JarRef, capN int) []JarRef {
	if len(in) <= capN {
		return in
	}
	// Prefer: JDK, Prefer=true, then keep order.
	var prefer, rest []JarRef
	for _, r := range in {
		if r.IsJDK || r.Prefer {
			prefer = append(prefer, r)
		} else {
			rest = append(rest, r)
		}
	}
	out := append(prefer, rest...)
	if len(out) > capN {
		out = out[:capN]
	}
	return out
}
