package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"defhop/internal/deps"
	"defhop/internal/java"
	"defhop/internal/model"
	"defhop/internal/overlay"
	"defhop/internal/plugin"
	"defhop/internal/plugins/feign"
	"defhop/internal/plugins/lombok"
	"defhop/internal/plugins/spring"
	"defhop/internal/store"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Hub owns overlay, symbol store, plugins, and navigation.
type Hub struct {
	Root     string   // primary root (status / cache default)
	Roots    []string // all workspace roots indexed together
	CacheDir string
	Overlay  *overlay.Store
	Index    *store.Store
	Plugins  *plugin.Registry

	exclude  map[string]bool
	mu       sync.Mutex // serializes index mutations from UPDATE path
	progress *progressTracker

	// lastWorkspaceFP fingerprints the last successful full index (path?hash).
	// REINDEX with the same fingerprint skips gob reload + snapshot + plugins.
	lastWorkspaceFP string
	pluginsReady    bool

	// cachePersistWG tracks async disk writes (per-file gobs + packed workspace.gob).
	cachePersistWG sync.WaitGroup
	// depsWG tracks background dependency-sources indexing (after setDone).
	depsWG sync.WaitGroup

	depsMu     sync.Mutex
	depsCancel context.CancelFunc // cancels in-flight indexDependencySources
}

func New(plugins ...plugin.Plugin) *Hub {
	return &Hub{
		Overlay:  overlay.New(),
		Index:    store.New(),
		Plugins:  plugin.NewRegistry(plugins...),
		progress: newProgressTracker(),
		exclude: map[string]bool{
			".git": true, ".gradle": true, ".idea": true, ".settings": true,
			".vscode": true, ".cache": true, ".defhop": true,
			"bin": true, "build": true, "classes": true,
			"dist": true, "logs": true, "node_modules": true, "out": true, "target": true,
		},
	}
}

// stopBackgroundDeps cancels dependency indexing and waits briefly so Rebuild
// can wipe the cache without racing PutFile into a half-deleted tree.
func (h *Hub) stopBackgroundDeps() {
	h.depsMu.Lock()
	cancel := h.depsCancel
	h.depsCancel = nil
	h.depsMu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		h.depsWG.Wait()
		h.cachePersistWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		fmt.Fprintf(os.Stderr, "[progress] rebuild: background tasks still running; continuing wipe\n")
	}
}

func (h *Hub) ConfigureExcludes(csv string) {
	for _, item := range strings.FieldsFunc(csv, func(r rune) bool { return r == ',' || r == ';' }) {
		n := strings.TrimSpace(strings.ToLower(filepath.Base(item)))
		if n != "" {
			h.exclude[n] = true
		}
	}
}

// Deprecated plain-text progress ? keep for non-Init paths; prefer h.progress.
func progress(msg string) {
	fmt.Fprintf(os.Stderr, "[progress] %s\n", msg)
}

// collectGeneratedJava indexes annotation-processor outputs (MapStruct Impl, etc.)
// without walking the rest of target/build (classes, jars, ?).
func (h *Hub) collectGeneratedJava(buildRoot string, javaPaths *[]string, facts *model.WorkspaceFacts) {
	candidates := []string{
		filepath.Join(buildRoot, "generated-sources"),
		filepath.Join(buildRoot, "generated-test-sources"),
		filepath.Join(buildRoot, "generated"),
	}
	for _, root := range candidates {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil {
				return nil
			}
			if info.IsDir() {
				name := strings.ToLower(info.Name())
				if name == "classes" || name == "test-classes" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(strings.ToLower(info.Name()), ".java") {
				*javaPaths = append(*javaPaths, path)
				facts.JavaFiles = append(facts.JavaFiles, path)
			}
			return nil
		})
	}
}

// Init cold-starts a single workspace root.
func (h *Hub) Init(ctx context.Context, root, cacheDir string) (files, symbols, refs int, elapsedMs int64, active []string, err error) {
	return h.InitRoots(ctx, []string{root}, cacheDir)
}

// RebuildRoots deletes on-disk cache, drops in-memory fingerprint skip state,
// then cold-builds from sources. Emits progress immediately so the UI does not
// sit on the extension's placeholder step while background deps are cancelled.
func (h *Hub) RebuildRoots(ctx context.Context, roots []string, cacheDir string) (files, symbols, refs int, elapsedMs int64, active []string, err error) {
	h.progress = newProgressTracker()
	h.progress.setScan("stopping background index...")
	h.stopBackgroundDeps()
	h.progress.setScan("clearing cache...")
	h.lastWorkspaceFP = ""
	h.pluginsReady = false
	store.WipeCache(cacheDir)
	return h.InitRoots(ctx, roots, cacheDir)
}

// InitRoots indexes one or more workspace folders into a single symbol store
// (VS Code multi-root). Absolute path keys keep modules distinct.
func (h *Hub) InitRoots(ctx context.Context, roots []string, cacheDir string) (files, symbols, refs int, elapsedMs int64, active []string, err error) {
	start := time.Now()
	cleaned := make([]string, 0, len(roots))
	seenRoot := map[string]bool{}
	for _, r := range roots {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		key := store.PathKey(r)
		if seenRoot[key] {
			continue
		}
		seenRoot[key] = true
		cleaned = append(cleaned, r)
	}
	if len(cleaned) == 0 {
		return 0, 0, 0, 0, nil, fmt.Errorf("workspace root required")
	}
	h.Root = cleaned[0]
	h.Roots = cleaned
	h.CacheDir = cacheDir
	h.progress = newProgressTracker()
	h.progress.setScan("scanning...")

	facts, javaPaths, err := h.scanAll(cleaned)
	if err != nil {
		return 0, 0, 0, 0, nil, err
	}
	h.progress.setScanDone(len(javaPaths),
		fmt.Sprintf("scan done: %d java, %d xml (%d roots)", len(facts.JavaFiles), len(facts.XMLFiles), len(cleaned)))

	store.EnsureCacheVersion(cacheDir)
	prevMeta := store.LoadManifest(cacheDir)
	fps := store.FingerprintFiles(javaPaths, prevMeta, func(done, total int) {
		h.progress.setHashing(done, total)
	})

	pathHashes := make(map[string]string, len(javaPaths))
	pathBytes := make(map[string][]byte)
	metaOut := make(map[string]store.FileMeta, len(javaPaths))
	for i := range fps {
		fp := &fps[i]
		if fp.Hash == "" {
			continue
		}
		p := fp.Path
		key := store.PathKey(p)
		pathHashes[key] = fp.Hash
		if len(fp.Data) > 0 {
			pathBytes[p] = fp.Data
			fp.Data = nil // release fingerprint buffer; pathBytes owns the slice
		}
		metaOut[key] = fp.Meta
	}

	fpSig := workspaceFingerprint(javaPaths, pathHashes)
	// Same content as last index in this process ? skip gob reload + rebuild + plugins.
	if fpSig != "" && fpSig == h.lastWorkspaceFP && h.Index.FileCount() > 0 && h.pluginsReady {
		h.progress.setJavaRunning(len(javaPaths), len(javaPaths))
		h.progress.setJavaReady(h.Index.FileCount())
		h.progress.markPlugins(activePluginSet(h.Plugins.IDs()))
		for _, id := range h.Plugins.IDs() {
			h.progress.setPlugin(id, "ready")
		}
		h.progress.setDone()
		_ = store.SaveManifest(cacheDir, metaOut)
		return h.statusCounts(time.Since(start).Milliseconds())
	}

	// Cold start fast path: one packed workspace.gob instead of thousands of per-file gobs.
	if fpSig != "" {
		if packed := store.LoadPacked(cacheDir, fpSig); len(packed) > 0 {
			if fileMap, ok := packedMatches(javaPaths, pathHashes, packed); ok {
				h.progress.setJavaRunning(len(javaPaths), len(javaPaths))
				return h.finishIndex(ctx, facts, fileMap, javaPaths, pathHashes, metaOut, cacheDir, fpSig, start, nil, false)
			}
		}
	}

	live := h.Index.Snapshot()
	fileMap := make(map[string]*model.FileSlice, len(javaPaths))
	needGob := make(map[string]string) // pathKey -> hash
	var toParse []string
	for _, p := range javaPaths {
		key := store.PathKey(p)
		hash := pathHashes[key]
		if live != nil && hash != "" {
			if fd := live.File(key); fd != nil && fd.ContentHash == hash {
				fileMap[key] = fd
				continue
			}
		}
		if hash != "" {
			needGob[key] = hash
		} else {
			toParse = append(toParse, p)
		}
	}
	if len(needGob) > 0 {
		cached := store.LoadFileCacheKeys(cacheDir, needGob)
		for key, fd := range cached {
			fileMap[key] = fd
			delete(needGob, key)
		}
		if len(needGob) > 0 {
			byKey := make(map[string]string, len(javaPaths))
			for _, p := range javaPaths {
				byKey[store.PathKey(p)] = p
			}
			for key := range needGob {
				if p := byKey[key]; p != "" {
					toParse = append(toParse, p)
				}
			}
		}
	}

	cachedN := len(javaPaths) - len(toParse)
	if len(javaPaths) > 0 {
		h.progress.setJavaRunning(cachedN, len(javaPaths))
	}
	for _, p := range toParse {
		if pathBytes[p] != nil {
			continue
		}
		if data, err := os.ReadFile(p); err == nil {
			pathBytes[p] = data
			sum := store.ContentHash(data)
			key := store.PathKey(p)
			pathHashes[key] = sum
			if info, err := os.Stat(p); err == nil {
				metaOut[key] = store.FileMeta{
					Hash: sum, Size: info.Size(), Mtime: info.ModTime().UnixNano(),
				}
			}
		}
	}
	parsed := h.parseJavaParallel(ctx, toParse, pathBytes, cachedN, len(javaPaths))
	// Drop source bytes as soon as parse finishes — FileSlice no longer needs them.
	for k := range pathBytes {
		pathBytes[k] = nil
	}
	pathBytes = nil
	for _, fd := range parsed {
		if fd == nil {
			continue
		}
		key := store.PathKey(fd.Path)
		if fd.ContentHash == "" {
			fd.ContentHash = pathHashes[key]
		}
		fileMap[key] = fd
	}

	var toSave map[string]*model.FileSlice
	if len(toParse) > 0 {
		toSave = make(map[string]*model.FileSlice, len(toParse))
		for _, p := range toParse {
			key := store.PathKey(p)
			if fd := fileMap[key]; fd != nil {
				toSave[key] = fd
			}
		}
	}
	return h.finishIndex(ctx, facts, fileMap, javaPaths, pathHashes, metaOut, cacheDir, fpSig, start, toSave, true)
}

func packedMatches(javaPaths []string, pathHashes map[string]string, packed map[string]*model.FileSlice) (map[string]*model.FileSlice, bool) {
	if len(packed) < len(javaPaths) {
		return nil, false
	}
	out := make(map[string]*model.FileSlice, len(javaPaths))
	for _, p := range javaPaths {
		key := store.PathKey(p)
		fd := packed[key]
		if fd == nil {
			return nil, false
		}
		want := pathHashes[key]
		if want != "" && fd.ContentHash != "" && fd.ContentHash != want {
			return nil, false
		}
		if fd.ContentHash == "" {
			fd.ContentHash = want
		}
		out[key] = fd
	}
	return out, true
}

func (h *Hub) finishIndex(
	ctx context.Context,
	facts *model.WorkspaceFacts,
	fileMap map[string]*model.FileSlice,
	javaPaths []string,
	pathHashes map[string]string,
	metaOut map[string]store.FileMeta,
	cacheDir, fpSig string,
	start time.Time,
	toSave map[string]*model.FileSlice,
	writePacked bool,
) (files, symbols, refs int, elapsedMs int64, active []string, err error) {
	h.Index.ReplaceAll(fileMap)
	h.progress.setJavaReady(len(fileMap)) // unlock nav before disk / plugins / deps
	_ = store.SaveManifest(cacheDir, metaOut)

	activePlugins := h.Plugins.DetectAndActivate(facts)
	activeSet := map[string]bool{}
	for _, p := range activePlugins {
		activeSet[p.ID()] = true
	}
	h.progress.markPlugins(activeSet)
	h.indexPluginsParallel(ctx, activePlugins, facts)
	h.pluginsReady = true
	if fpSig == "" {
		fpSig = workspaceFingerprint(javaPaths, pathHashes)
	}
	h.lastWorkspaceFP = fpSig
	h.progress.setDone()

	// Dependency sources in background — do not block INIT / status 100%.
	rootsCopy := append([]string(nil), h.Roots...)
	cacheCopy := cacheDir
	var factsCopy *model.WorkspaceFacts
	if facts != nil {
		fc := *facts
		factsCopy = &fc
	}
	depsCtx, cancel := context.WithCancel(context.Background())
	h.depsMu.Lock()
	if h.depsCancel != nil {
		h.depsCancel()
	}
	h.depsCancel = cancel
	h.depsMu.Unlock()
	h.depsWG.Add(1)
	go func() {
		defer h.depsWG.Done()
		h.indexDependencySources(depsCtx, rootsCopy, cacheCopy, factsCopy)
	}()

	if len(toSave) > 0 || writePacked {
		h.persistCacheAsync(cacheDir, fpSig, fileMap, toSave, pathHashes, writePacked)
	}

	return h.statusCounts(time.Since(start).Milliseconds())
}

// indexDependencySources materializes JDK + Maven/Gradle *-sources.jar entries
// under cacheDir/deps and merges them into the symbol store (capped).
func (h *Hub) indexDependencySources(ctx context.Context, roots []string, cacheDir string, facts *model.WorkspaceFacts) {
	if cacheDir == "" || len(roots) == 0 {
		return
	}
	select {
	case <-ctx.Done():
		return
	default:
	}
	var hits map[string]int
	if facts != nil {
		hits = facts.ImportHits
	}
	jars := deps.DiscoverSourcesJars(roots, hits)
	if len(jars) == 0 {
		return
	}
	outRoot := filepath.Join(cacheDir, "deps")
	budget := deps.MaxTotalFiles
	totalFiles, jarHits := 0, 0
	for _, ref := range jars {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if budget <= 0 {
			break
		}
		files, err := deps.MaterializeJar(ref, outRoot, &budget)
		if err != nil || len(files) == 0 {
			continue
		}
		jarHits++
		for _, sf := range files {
			select {
			case <-ctx.Done():
				return
			default:
			}
			sum := store.ContentHash(sf.Data)
			key := store.PathKey(sf.Path)
			if cur := h.Index.Snapshot().File(key); cur != nil && cur.ContentHash == sum {
				totalFiles++
				continue
			}
			fd := parseSafe(sf.Path, string(sf.Data), 0)
			if fd == nil {
				continue
			}
			fd.ContentHash = sum
			h.Index.PutFile(fd)
			totalFiles++
		}
	}
	if totalFiles > 0 {
		fmt.Fprintf(os.Stderr, "[progress] %s\n", deps.FormatSummary(jarHits, totalFiles))
	}
}

// persistCacheAsync writes dirty per-file gobs + packed workspace.gob off the Init
// critical path. Per-file covers warm partial rebuilds; packed covers cold start.
func (h *Hub) persistCacheAsync(
	cacheDir, fpSig string,
	fileMap, toSave map[string]*model.FileSlice,
	pathHashes map[string]string,
	writePacked bool,
) {
	saveCopy := make(map[string]*model.FileSlice, len(toSave))
	for k, v := range toSave {
		saveCopy[k] = v
	}
	hashCopy := make(map[string]string, len(pathHashes))
	for k, v := range pathHashes {
		hashCopy[k] = v
	}
	var packedCopy map[string]*model.FileSlice
	if writePacked && fpSig != "" {
		packedCopy = make(map[string]*model.FileSlice, len(fileMap))
		for k, v := range fileMap {
			packedCopy[k] = v
		}
	}
	dir, fp := cacheDir, fpSig
	h.cachePersistWG.Add(1)
	go func() {
		defer h.cachePersistWG.Done()
		if len(saveCopy) > 0 {
			_ = store.SaveFileCache(dir, saveCopy, hashCopy)
		}
		if packedCopy != nil {
			_ = store.SavePacked(dir, fp, packedCopy)
		}
	}()
}

// FlushCachePersist waits for background cache writes and deps indexing
// (call before process exit).
func (h *Hub) FlushCachePersist() {
	h.depsWG.Wait()
	h.cachePersistWG.Wait()
}

func (h *Hub) statusCounts(elapsedMs int64) (files, symbols, refs int, ms int64, active []string, err error) {
	snap := h.Index.Snapshot()
	symN, refN := 0, 0
	fileN := 0
	if snap != nil {
		fileN = len(snap.Files)
		symN = len(snap.SymTab)
		for _, rs := range snap.Refs {
			refN += len(rs)
		}
	}
	return fileN, symN, refN, elapsedMs, h.Plugins.IDs(), nil
}

func activePluginSet(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func workspaceFingerprint(javaPaths []string, pathHashes map[string]string) string {
	if len(javaPaths) == 0 {
		return ""
	}
	keys := make([]string, 0, len(javaPaths))
	for _, p := range javaPaths {
		keys = append(keys, store.PathKey(p))
	}
	sort.Strings(keys)
	var b strings.Builder
	b.Grow(len(keys) * 48)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(pathHashes[k])
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:16])
}

func (h *Hub) scanAll(roots []string) (*model.WorkspaceFacts, []string, error) {
	if len(roots) == 0 {
		return nil, nil, fmt.Errorf("no roots")
	}
	if len(roots) == 1 {
		return h.scan(roots[0])
	}
	merged := &model.WorkspaceFacts{
		Root:           roots[0],
		AnnotationHits: map[string]int{},
		ImportHits:     map[string]int{},
	}
	var javaPaths []string
	seenJava := map[string]bool{}
	seenXML := map[string]bool{}
	seenCfg := map[string]bool{}
	seenRes := map[string]bool{}
	for _, root := range roots {
		facts, paths, err := h.scan(root)
		if err != nil {
			return nil, nil, err
		}
		if facts.HasPom {
			merged.HasPom = true
		}
		for k, v := range facts.AnnotationHits {
			merged.AnnotationHits[k] += v
		}
		for k, v := range facts.ImportHits {
			merged.ImportHits[k] += v
		}
		for _, p := range paths {
			k := store.PathKey(p)
			if !seenJava[k] {
				seenJava[k] = true
				javaPaths = append(javaPaths, p)
				merged.JavaFiles = append(merged.JavaFiles, p)
			}
		}
		for _, p := range facts.XMLFiles {
			k := store.PathKey(p)
			if !seenXML[k] {
				seenXML[k] = true
				merged.XMLFiles = append(merged.XMLFiles, p)
			}
		}
		for _, p := range facts.ConfigFiles {
			k := store.PathKey(p)
			if !seenCfg[k] {
				seenCfg[k] = true
				merged.ConfigFiles = append(merged.ConfigFiles, p)
			}
		}
		for _, p := range facts.ResourceRoots {
			k := store.PathKey(p)
			if !seenRes[k] {
				seenRes[k] = true
				merged.ResourceRoots = append(merged.ResourceRoots, p)
			}
		}
	}
	return merged, javaPaths, nil
}

func (h *Hub) scan(root string) (*model.WorkspaceFacts, []string, error) {
	facts := &model.WorkspaceFacts{
		Root:           root,
		AnnotationHits: map[string]int{},
		ImportHits:     map[string]int{},
	}
	var javaPaths []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && path != root {
			lower := strings.ToLower(d.Name())
			if h.exclude[lower] {
				// MapStruct / annotation processors emit sources under
				// target|build|out/.../generated(-sources)/ ? index those only.
				if lower == "target" || lower == "build" || lower == "out" {
					h.collectGeneratedJava(path, &javaPaths, facts)
				}
				return filepath.SkipDir
			}
			if d.Name() == "resources" {
				parent := filepath.Base(filepath.Dir(path))
				if parent == "main" || parent == "test" {
					facts.ResourceRoots = append(facts.ResourceRoots, path)
				}
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		lower := strings.ToLower(name)
		switch {
		case strings.HasSuffix(lower, ".java"):
			javaPaths = append(javaPaths, path)
			facts.JavaFiles = append(facts.JavaFiles, path)
		case strings.HasSuffix(lower, ".xml"):
			facts.XMLFiles = append(facts.XMLFiles, path)
		case strings.EqualFold(name, "pom.xml"):
			facts.HasPom = true
		case strings.HasSuffix(lower, ".yml"), strings.HasSuffix(lower, ".yaml"),
			strings.HasSuffix(lower, ".properties"):
			slash := filepath.ToSlash(path)
			base := lower
			if strings.HasPrefix(base, "application") || strings.HasPrefix(base, "bootstrap") ||
				strings.Contains(strings.ToLower(slash), "/resources/") {
				facts.ConfigFiles = append(facts.ConfigFiles, path)
			}
		}
		return nil
	})
	// Path-level Detect hints (no I/O): MapStruct packages / generated Impls.
	for _, p := range javaPaths {
		lower := strings.ToLower(filepath.ToSlash(p))
		if strings.Contains(lower, "/mapstruct/") || strings.Contains(lower, "\\mapstruct\\") {
			facts.ImportHits["org.mapstruct"]++
		}
		if strings.Contains(lower, "/generated-sources/") || strings.Contains(lower, "/generated/") {
			if strings.HasSuffix(lower, "impl.java") {
				facts.ImportHits["org.mapstruct"]++
			}
		}
	}

	// Sample Java files for Detect hints — prefer mapstruct / mapper-ish paths.
	samplePaths := pickDetectSamples(javaPaths, 60)
	for _, p := range samplePaths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		sampleDetect(string(data), facts)
	}
	// If the sample window missed framework markers (large workspaces),
	// scan remaining files until lombok / mapstruct hints appear.
	ensureFrameworkDetectHints(javaPaths, samplePaths, facts)
	return facts, javaPaths, err
}

func pickDetectSamples(paths []string, limit int) []string {
	if len(paths) <= limit {
		return paths
	}
	var prefer, rest []string
	for _, p := range paths {
		lower := strings.ToLower(filepath.ToSlash(p))
		base := strings.ToLower(filepath.Base(p))
		// Prefer plugin-ish paths so Detect is not starved when the workspace is huge.
		if strings.Contains(lower, "mapstruct") || strings.Contains(lower, "lombok") ||
			strings.Contains(base, "mapper") || strings.Contains(lower, "/provider/") ||
			strings.Contains(lower, "feign") || strings.Contains(lower, "generated-sources") ||
			strings.Contains(lower, "/generated/") {
			prefer = append(prefer, p)
		} else {
			rest = append(rest, p)
		}
	}
	out := append([]string(nil), prefer...)
	for _, p := range rest {
		if len(out) >= limit {
			break
		}
		out = append(out, p)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func sampleDetect(src string, facts *model.WorkspaceFacts) {
	for _, a := range []string{"Mapper", "Select", "Insert", "Update", "Delete",
		"Data", "Getter", "Setter", "Builder", "SuperBuilder", "Slf4j",
		"NoArgsConstructor", "AllArgsConstructor", "RequiredArgsConstructor",
		"Mapping", "Mappings",
		"Component", "Service", "Repository", "Controller", "RestController",
		"Autowired", "Resource", "Bean", "Configuration", "SpringBootApplication",
		"Value", "ConfigurationProperties", "FeignClient", "EnableFeignClients",
		"Qualifier"} {
		if strings.Contains(src, "@"+a) {
			facts.AnnotationHits[a]++
		}
	}
	for _, p := range []string{"lombok.", "org.mapstruct", "org.apache.ibatis",
		"org.mybatis", "com.baomidou.mybatisplus", "org.springframework",
		"org.springframework.cloud.openfeign"} {
		if strings.Contains(src, p) {
			facts.ImportHits[p]++
		}
	}
	if strings.Contains(src, "ServiceImpl") {
		facts.ImportHits["com.baomidou.mybatisplus"]++
	}
}

func needsLombokHint(facts *model.WorkspaceFacts) bool {
	if facts == nil {
		return false
	}
	if facts.ImportHits["lombok."] > 0 {
		return false
	}
	for _, a := range []string{"Data", "Getter", "Setter", "Builder", "SuperBuilder", "Slf4j",
		"NoArgsConstructor", "AllArgsConstructor", "RequiredArgsConstructor"} {
		if facts.AnnotationHits[a] > 0 {
			return false
		}
	}
	return true
}

func needsMapstructHint(facts *model.WorkspaceFacts) bool {
	if facts == nil {
		return false
	}
	if facts.ImportHits["org.mapstruct"] > 0 {
		return false
	}
	return facts.AnnotationHits["Mapping"] == 0 && facts.AnnotationHits["Mappings"] == 0
}

func ensureFrameworkDetectHints(all, sampled []string, facts *model.WorkspaceFacts) {
	if facts == nil || (!needsLombokHint(facts) && !needsMapstructHint(facts)) {
		return
	}
	seen := map[string]bool{}
	for _, p := range sampled {
		seen[store.PathKey(p)] = true
	}
	for _, p := range all {
		if !needsLombokHint(facts) && !needsMapstructHint(facts) {
			return
		}
		if seen[store.PathKey(p)] {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := string(data)
		// Cheap gate before full sampleDetect.
		if (needsLombokHint(facts) && (strings.Contains(s, "lombok.") || strings.Contains(s, "@Data") ||
			strings.Contains(s, "@Builder") || strings.Contains(s, "@Getter"))) ||
			(needsMapstructHint(facts) && (strings.Contains(s, "org.mapstruct") || strings.Contains(s, "@Mapping"))) {
			sampleDetect(s, facts)
		}
	}
}

func (h *Hub) parseJavaParallel(ctx context.Context, paths []string, preloaded map[string][]byte, already, grandTotal int) []*model.FileSlice {
	total := len(paths)
	if total == 0 {
		if grandTotal > 0 {
			h.progress.setJavaRunning(grandTotal, grandTotal)
		}
		return nil
	}
	workers := runtime.NumCPU()
	if workers > total {
		workers = total
	}
	work := make(chan string, total)
	for _, p := range paths {
		work <- p
	}
	close(work)

	var wg sync.WaitGroup
	var done atomic.Int32
	outCh := make(chan *model.FileSlice, total)
	reportTotal := grandTotal
	if reportTotal <= 0 {
		reportTotal = total
	}

	h.progress.setJavaRunning(already, reportTotal)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case p, ok := <-work:
					if !ok {
						return
					}
					data := preloaded[p]
					if data == nil {
						var err error
						data, err = os.ReadFile(p)
						if err != nil {
							continue
						}
					}
					fd := parseSafe(p, string(data), 0)
					if fd != nil {
						outCh <- fd
					}
					n := int(done.Add(1))
					// Emit often enough for a smooth 15?82% bar (? every 1?2%).
					step := 20
					if reportTotal > 2000 {
						step = reportTotal / 50
					} else if reportTotal > 500 {
						step = reportTotal / 40
					}
					if step < 10 {
						step = 10
					}
					if n == total || n%step == 0 {
						h.progress.setJavaRunning(already+n, reportTotal)
					}
				}
			}
		}()
	}
	wg.Wait()
	close(outCh)
	h.progress.setJavaRunning(already+total, reportTotal)
	var out []*model.FileSlice
	for fd := range outCh {
		out = append(out, fd)
	}
	return out
}

func parseSafe(path, text string, gen uint64) (fd *model.FileSlice) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "[index] panic parsing %s: %v\n", path, r)
			// Keep indexing resilient: publish an empty slice at this gen so
			// overlay/index gens stay aligned and bad syntax cannot stall updates.
			fd = &model.FileSlice{Path: filepath.ToSlash(path), Gen: gen}
		}
	}()
	fd = java.Parse(path, text, gen)
	if fd == nil {
		fd = &model.FileSlice{Path: filepath.ToSlash(path), Gen: gen}
	}
	// Synthesize Lombok accessors/builder/log into the slice before publish.
	lombok.Enrich(fd)
	return fd
}

func (h *Hub) indexPluginsParallel(ctx context.Context, plugs []plugin.Plugin, facts *model.WorkspaceFacts) {
	if len(plugs) == 0 {
		return
	}
	var wg sync.WaitGroup
	core := h.Index.Snapshot()
	for _, p := range plugs {
		wg.Add(1)
		go func(pl plugin.Plugin) {
			defer wg.Done()
			h.progress.setPlugin(pl.ID(), "indexing")
			if err := pl.Index(ctx, core, facts); err != nil {
				fmt.Fprintf(os.Stderr, "[plugin] %s: %v\n", pl.ID(), err)
				h.progress.setPlugin(pl.ID(), "error")
			} else {
				h.progress.setPlugin(pl.ID(), "ready")
			}
		}(p)
	}
	wg.Wait()
}

// UpdateBuffer stores the overlay immediately (returns at once) and reindexes
// the file in the background. Navigate also sync-publishes stale overlays so
// cross-file jumps see edits without waiting on this async path.
// Used for editor keystrokes — must not block IPC.
func (h *Hub) UpdateBuffer(path, text string) uint64 {
	key := store.PathKey(path)
	gen := h.Overlay.Put(key, path, text)
	go h.indexBufferAsync(path, key, text, gen)
	return gen
}

// IndexBufferSync puts the overlay and publishes into the symbol index on this
// goroutine. Used for AI/tool disk writes so INDEX returns only when navigable.
func (h *Hub) IndexBufferSync(path, text string) uint64 {
	key := store.PathKey(path)
	gen := h.Overlay.Put(key, path, text)
	h.indexOneBuffer(path, key, text, gen)
	return gen
}

// IndexDiskFile reads one path from disk and sync-indexes it (create / incremental).
// Returns false when the file cannot be read.
func (h *Hub) IndexDiskFile(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	h.IndexBufferSync(path, string(data))
	return true
}

// RemovePath drops a file from overlay + symbol index and notifies plugins.
func (h *Hub) RemovePath(path string) {
	key := store.PathKey(path)
	h.Overlay.Remove(key)
	h.mu.Lock()
	defer h.mu.Unlock()
	old := h.Index.RemoveFile(path)
	h.lastWorkspaceFP = ""
	ev := plugin.ChangeEvent{PathKey: key, Path: path, Old: old, New: nil}
	for _, p := range h.Plugins.Active() {
		p.OnFileChanged(ev)
	}
}

// DropOverlay clears the editor overlay for a closed document (keeps disk index).
func (h *Hub) DropOverlay(path string) {
	h.Overlay.Remove(store.PathKey(path))
}

func (h *Hub) indexBufferAsync(path, key, text string, gen uint64) {
	h.indexOneBuffer(path, key, text, gen)
}

// indexOneBuffer publishes one overlay generation into the symbol index.
// Tolerant of broken Java: parseSafe always returns a FileSlice.
// Superseded gens and unchanged content (hash match) skip the expensive parse.
func (h *Hub) indexOneBuffer(path, key, text string, gen uint64) {
	if h.Overlay.Gen(key) != gen {
		return
	}
	lower := strings.ToLower(path)
	ev := plugin.ChangeEvent{PathKey: key, Path: path, Text: text}
	if strings.HasSuffix(lower, ".java") {
		sum := store.ContentHashString(text)
		if cur := h.Index.Snapshot().File(key); cur != nil && cur.ContentHash == sum {
			if cur.Gen >= gen {
				return
			}
			h.Index.BumpGen(path, gen)
			return
		}
		if h.Overlay.Gen(key) != gen {
			return
		}
		fd := parseSafe(path, text, gen)
		if fd != nil {
			fd.ContentHash = sum
		}
		if h.Overlay.Gen(key) != gen {
			return
		}
		h.mu.Lock()
		if h.Overlay.Gen(key) != gen {
			h.mu.Unlock()
			return
		}
		cur := h.Index.Snapshot().File(key)
		if cur != nil && cur.Gen >= gen {
			h.mu.Unlock()
			return
		}
		if cur != nil && cur.ContentHash == sum {
			h.mu.Unlock()
			h.Index.BumpGen(path, gen)
			return
		}
		old := h.Index.PutFile(fd)
		h.lastWorkspaceFP = ""
		ev.Old, ev.New = old, fd
		for _, p := range h.Plugins.Active() {
			p.OnFileChanged(ev)
		}
		h.mu.Unlock()
		return
	}
	if strings.HasSuffix(lower, ".xml") ||
		strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml") ||
		strings.HasSuffix(lower, ".properties") {
		h.mu.Lock()
		h.lastWorkspaceFP = ""
		for _, p := range h.Plugins.Active() {
			p.OnFileChanged(ev)
		}
		h.mu.Unlock()
	}
}

// publishStaleOverlays mirrors IntelliJ "lazy index update on first access" /
// PsiDocumentManager.commit*: before DEF/REFS, publish every dirty overlay into
// the symbol index so navigation never waits on the async UPDATE worker.
// Prefer the navigation target first, then newest gens. No hard cap — a truncated
// flush is what made multi-file AI edits feel "broken" until a later reindex.
func (h *Hub) publishStaleOverlays(preferPath string) {
	entries := h.Overlay.AllMeta()
	if len(entries) == 0 {
		return
	}
	preferKey := store.PathKey(preferPath)
	flushOne := func(e overlay.Meta) bool {
		if e.Path == "" || !strings.HasSuffix(strings.ToLower(e.Path), ".java") {
			return false
		}
		snap := h.Index.Snapshot()
		fd := snap.File(e.Key)
		if fd != nil && fd.Gen >= e.Gen {
			return false
		}
		ent, ok := h.Overlay.Get(e.Key)
		if !ok || ent.Gen != e.Gen {
			return false
		}
		h.indexOneBuffer(ent.Path, e.Key, ent.Text, e.Gen)
		return true
	}
	if preferKey != "" {
		for _, e := range entries {
			if e.Key != preferKey {
				continue
			}
			flushOne(e)
			break
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Key == preferKey {
			return true
		}
		if entries[j].Key == preferKey {
			return false
		}
		return entries[i].Gen > entries[j].Gen
	})
	for _, e := range entries {
		flushOne(e)
	}
}

// fileText returns overlay text if present, else disk, else req text.
func (h *Hub) fileText(path, fallback string) (text string, gen uint64) {
	key := store.PathKey(path)
	if e, ok := h.Overlay.Get(key); ok {
		return e.Text, e.Gen
	}
	if fallback != "" {
		return fallback, 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0
	}
	return string(data), 0
}

// Navigate answers DEF/REFS/IMPL/ROUTE.
// navMenu is reserved for optional UI hints (currently unused).
func (h *Hub) Navigate(req model.NavRequest) (locs []*model.Location, navMenu string) {
	// Publish edits from every open/dirty overlay first so cross-file symbols
	// (e.g. Result.success added moments ago) are visible immediately.
	h.publishStaleOverlays(req.Path)

	key := store.PathKey(req.Path)
	text, gen := h.fileText(req.Path, req.Text)
	if text == "" {
		return nil, ""
	}
	req.Text = text
	req.Gen = gen

	snap := h.Index.Snapshot()
	fd := snap.File(key)

	needFresh := strings.HasSuffix(strings.ToLower(req.Path), ".java") &&
		(fd == nil || (gen > 0 && fd.Gen < gen))
	if needFresh {
		h.indexOneBuffer(req.Path, key, text, gen)
		snap = h.Index.Snapshot()
		fd = snap.File(key)
	}

	lower := strings.ToLower(req.Path)
	if strings.HasSuffix(lower, ".xml") {
		return h.navigateXML(req, snap), ""
	}
	if spring.IsConfigPath(req.Path) {
		var out []*model.Location
		for _, p := range h.Plugins.Active() {
			if c := p.Contribute(snap, req, nil, ""); len(c) > 0 {
				out = append(out, c...)
			}
		}
		return dedupe(out), ""
	}
	if fd == nil {
		return nil, ""
	}

	offset := java.OffsetAt(text, req.Line, req.Character)
	token := java.TokenAt(text, offset)
	sym, fallback := java.Resolve(snap, fd, offset, token, text)

	// Plugin contributions first (override-friendly).
	for _, p := range h.Plugins.Active() {
		if c := p.Contribute(snap, req, sym, fallback); len(c) > 0 {
			locs = append(locs, c...)
		}
	}

	if len(locs) > 0 && (req.Kind == model.NavDef || req.Kind == model.NavRoute || req.Kind == model.NavImpl) {
		return dedupe(locs), ""
	}

	switch req.Kind {
	case model.NavDef:
		if sym != nil && sym.Declaration != nil {
			// Injected field on its declaration line ? jump to field type (e.g. Mapper),
			// not the field itself (same-line "jump" feels like a no-op).
			if sym.Kind == "field" && sym.TypeKey != "" && atSymbolDecl(req, sym) {
				if tkey := snap.ResolveTypeKey(sym.TypeKey, fd); tkey != "" {
					fqn := strings.TrimPrefix(tkey, "T|")
					if hint := jdkConfigHint(fqn); hint != "" {
						return nil, hint
					}
					if ts := snap.Symbol(tkey); ts != nil && ts.Declaration != nil {
						return []*model.Location{ts.Declaration}, ""
					}
					// Field type is an external library class not yet indexed.
					if ext := h.ensureExternalType(fqn); ext != nil && ext.Declaration != nil {
						return []*model.Location{ext.Declaration}, ""
					}
				}
			}
			// Interface / abstract method -> prefer implementations when unique-ish.
			// Skip FeignClient (mapped to Controllers by feign plugin; Fallback is not the target).
			if (sym.Kind == "method" || sym.Kind == "constructor") && sym.OwnerKey != "" {
				if owner := snap.Symbol(sym.OwnerKey); owner != nil && owner.InterfaceType && !feign.IsFeignMethod(snap, sym) {
					if impls := java.Implementations(snap, sym); len(impls) > 0 {
						return dedupe(impls), ""
					}
				}
			}
			// Stale JDK symbols from a previous index must not open cache when jdkHome is unset.
			if fqn := jdkFQNOf(sym); fqn != "" {
				if hint := jdkConfigHint(fqn); hint != "" {
					return nil, hint
				}
			}
			return []*model.Location{sym.Declaration}, ""
		}
		// Library type from import / unresolved type Occ (e.g. RequestConfig).
		if fqn := externalFQNFromContext(snap, fd, offset, token, fallback, sym); fqn != "" {
			if ext := h.ensureExternalType(fqn); ext != nil && ext.Declaration != nil {
				return []*model.Location{ext.Declaration}, ""
			}
			if hint := jdkConfigHint(fqn); hint != "" {
				return nil, hint
			}
		}
	case model.NavImpl:
		return dedupe(java.Implementations(snap, sym)), ""
	case model.NavRefs:
		if java.IsLocalKey(fallback) || (sym != nil && java.IsLocalKey(sym.Key)) {
			key := fallback
			if sym != nil && java.IsLocalKey(sym.Key) {
				key = sym.Key
			}
			return java.LocalRefs(fd, key, req.IncludeDecl), ""
		}
		coreRefs := java.References(snap, sym, req.IncludeDecl)
		if len(locs) > 0 {
			return dedupe(append(locs, coreRefs...)), ""
		}
		return dedupe(coreRefs), ""
	case model.NavRoute:
		// only plugins
	}
	return dedupe(locs), ""
}

func atSymbolDecl(req model.NavRequest, sym *model.Symbol) bool {
	if sym == nil || sym.Declaration == nil {
		return false
	}
	d := sym.Declaration
	if req.Line != d.Line {
		return false
	}
	end := d.EndCharacter
	if end <= d.Character {
		end = d.Character + len(sym.Name)
	}
	return req.Character >= d.Character && req.Character <= end
}

func (h *Hub) navigateXML(req model.NavRequest, snap *store.Snapshot) []*model.Location {
	var locs []*model.Location
	for _, p := range h.Plugins.Active() {
		if c := p.Contribute(snap, req, nil, ""); len(c) > 0 {
			locs = append(locs, c...)
		}
	}
	return dedupe(locs)
}

// DocumentHighlights returns same-file read/write highlights for the symbol under cursor.
func (h *Hub) DocumentHighlights(path string, line, character int) []*model.Location {
	locs, _ := h.Navigate(model.NavRequest{
		Kind:        model.NavRefs,
		Path:        path,
		Line:        line,
		Character:   character,
		IncludeDecl: true,
	})
	key := store.PathKey(path)
	var out []*model.Location
	for _, l := range locs {
		if l == nil {
			continue
		}
		if store.PathKey(l.Path) == key {
			out = append(out, l)
		}
	}
	return out
}

func dedupe(locs []*model.Location) []*model.Location {
	seen := map[string]bool{}
	var out []*model.Location
	for _, l := range locs {
		if l == nil {
			continue
		}
		k := fmt.Sprintf("%s:%d:%d", l.Path, l.Line, l.Character)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, l)
	}
	return out
}

// Status summary.
func (h *Hub) Status() (files, symbols, refs int, plugins []string, prog ProgressSnap) {
	if h.progress != nil {
		prog = h.progress.Snapshot()
	}
	snap := h.Index.Snapshot()
	if snap == nil {
		return 0, 0, 0, h.Plugins.IDs(), prog
	}
	refN := 0
	for _, rs := range snap.Refs {
		refN += len(rs)
	}
	return len(snap.Files), len(snap.SymTab), refN, h.Plugins.IDs(), prog
}
