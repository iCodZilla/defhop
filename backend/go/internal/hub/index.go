package hub

import (
	"context"
	"crypto/sha256"
	"defhop/internal/deps"
	"defhop/internal/java"
	"defhop/internal/model"
	"defhop/internal/plugin"
	"defhop/internal/plugins/lombok"
	"defhop/internal/store"
	"defhop/internal/textenc"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func (h *Hub) collectGeneratedJava(ctx context.Context, buildRoot string, javaPaths *[]string, facts *model.WorkspaceFacts) {
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
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if info.IsDir() {
				name := strings.ToLower(info.Name())
				if name == "classes" || name == "test-classes" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(strings.ToLower(info.Name()), ".java") && info.Size() <= maxIndexedFileBytes {
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
	if cacheDir == "" && len(roots) > 0 {
		cacheDir = filepath.Join(roots[0], ".defhop")
	}
	release, err := store.AcquireIndexLock(cacheDir, 90*time.Second)
	if err != nil {
		return 0, 0, 0, 0, nil, err
	}
	defer release()
	h.resetProgress()
	h.progress.setScan("stopping background index...")
	h.stopBackgroundDeps()
	h.progress.setScan("clearing cache...")
	h.setWorkspaceState("", false)
	store.WipeCache(cacheDir)
	return h.initRootsLocked(ctx, roots, cacheDir)
}

// InitRoots indexes one or more workspace folders into a single symbol store
// (VS Code multi-root). Absolute path keys keep modules distinct.
func (h *Hub) InitRoots(ctx context.Context, roots []string, cacheDir string) (files, symbols, refs int, elapsedMs int64, active []string, err error) {
	cleaned, cacheDir, err := normalizeRoots(roots, cacheDir)
	if err != nil {
		return 0, 0, 0, 0, nil, err
	}
	release, err := store.AcquireIndexLock(cacheDir, 90*time.Second)
	if err != nil {
		return 0, 0, 0, 0, nil, err
	}
	defer release()
	return h.initRootsLocked(ctx, cleaned, cacheDir)
}

func normalizeRoots(roots []string, cacheDir string) (cleaned []string, outCache string, err error) {
	cleaned = make([]string, 0, len(roots))
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
		return nil, "", fmt.Errorf("workspace root required")
	}
	outCache = cacheDir
	if outCache == "" {
		// Never let an empty cacheDir resolve to the process working
		// directory (tests used to write "version"/"files" into the package).
		outCache = filepath.Join(cleaned[0], ".defhop")
	}
	return cleaned, outCache, nil
}

// initRootsLocked assumes the caller holds the disk index.lock for cacheDir.
func (h *Hub) initRootsLocked(ctx context.Context, roots []string, cacheDir string) (files, symbols, refs int, elapsedMs int64, active []string, err error) {
	h.indexMu.Lock()
	defer h.indexMu.Unlock()
	start := time.Now()
	cleaned, cacheDir, err := normalizeRoots(roots, cacheDir)
	if err != nil {
		return 0, 0, 0, 0, nil, err
	}
	h.setConfig(cleaned[0], cleaned, cacheDir)
	h.resetProgress()
	h.progress.setScan("scanning...")

	// Commands keep mutating the index while this background scan runs.
	// Snapshot the pre-scan state so finishIndex can detect (and keep) files
	// created/changed by UPDATE/INDEX in the meantime.
	before := h.Index.Snapshot()
	facts, javaPaths, err := h.scanAll(ctx, cleaned)
	if err != nil {
		return 0, 0, 0, time.Since(start).Milliseconds(), nil, err
	}
	h.progress.setScanDone(len(javaPaths),
		fmt.Sprintf("scan done: %d java, %d xml (%d roots)", len(facts.JavaFiles), len(facts.XMLFiles), len(cleaned)))

	store.EnsureCacheVersion(cacheDir)
	prevMeta := store.LoadManifest(cacheDir)
	fps := store.FingerprintFiles(ctx, javaPaths, prevMeta, func(done, total int) {
		h.progress.setHashing(done, total)
	})
	if err := ctx.Err(); err != nil {
		return 0, 0, 0, time.Since(start).Milliseconds(), nil, err
	}

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
	lastFP, pluginsReady := h.workspaceState()
	if fpSig != "" && fpSig == lastFP && h.Index.FileCount() > 0 && pluginsReady {
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
				return h.finishIndex(ctx, before, facts, fileMap, javaPaths, pathHashes, metaOut, cacheDir, fpSig, start, nil, false)
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
		cached := store.LoadFileCacheKeys(ctx, cacheDir, needGob)
		if err := ctx.Err(); err != nil {
			return 0, 0, 0, time.Since(start).Milliseconds(), nil, err
		}
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
			data = textenc.Decode(data)
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
	if err := ctx.Err(); err != nil {
		// Cancelled: keep the previous published snapshot instead of
		// swapping in a partially parsed one.
		return 0, 0, 0, time.Since(start).Milliseconds(), nil, err
	}
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
	return h.finishIndex(ctx, before, facts, fileMap, javaPaths, pathHashes, metaOut, cacheDir, fpSig, start, toSave, true)
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
	before *store.Snapshot,
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
	// Commands keep answering UPDATE/INDEX while the scan runs. Merge what
	// they touched before ReplaceAll so a background refresh cannot roll the
	// index back to the scan's older view.
	now := h.Index.Snapshot()
	merged := false
	removed := false
	if before != nil && now != nil {
		for key, cur := range now.Files {
			if cur == nil || cur.Gen > 0 || cur.Path == "" {
				continue // overlay text is re-published after ReplaceAll
			}
			prev, existed := before.Files[key]
			if !existed {
				if _, scanned := fileMap[key]; !scanned {
					fileMap[key] = cur // created + INDEXed during the scan
					javaPaths = append(javaPaths, cur.Path)
					if cur.ContentHash != "" {
						pathHashes[key] = cur.ContentHash
					}
					if info, err := os.Stat(cur.Path); err == nil {
						metaOut[key] = store.FileMeta{
							Hash: cur.ContentHash, Size: info.Size(), Mtime: info.ModTime().UnixNano(),
						}
					}
					merged = true
				}
				continue
			}
			if prev == nil || prev.ContentHash == "" || cur.ContentHash == "" ||
				prev.ContentHash == cur.ContentHash {
				continue
			}
			// Changed on disk during the scan: re-read the current bytes.
			info, statErr := os.Stat(cur.Path)
			if statErr != nil || info.Size() > maxIndexedFileBytes {
				continue
			}
			data, readErr := os.ReadFile(cur.Path)
			if readErr != nil {
				continue
			}
			data = textenc.Decode(data)
			hash := store.ContentHash(data)
			fd := parseSafe(cur.Path, string(data), 0)
			if fd == nil {
				continue
			}
			fd.ContentHash = hash
			fileMap[key] = fd
			pathHashes[key] = hash
			metaOut[key] = store.FileMeta{
				Hash: hash, Size: info.Size(), Mtime: info.ModTime().UnixNano(),
			}
			merged = true
		}
		// A REMOVE processed during the scan must not leave a ghost from the
		// file the scan saw before it was deleted.
		for key := range fileMap {
			if _, ok := now.Files[key]; ok {
				continue
			}
			if _, existed := before.Files[key]; !existed {
				continue
			}
			delete(fileMap, key)
			delete(pathHashes, key)
			delete(metaOut, key)
			removed = true
		}
	}
	if removed {
		kept := javaPaths[:0]
		for _, p := range javaPaths {
			if _, ok := fileMap[store.PathKey(p)]; ok {
				kept = append(kept, p)
			}
		}
		javaPaths = kept
	}
	if merged || removed {
		fpSig = workspaceFingerprint(javaPaths, pathHashes)
	}
	h.Index.ReplaceAll(fileMap)
	h.publishStaleOverlays("")
	h.progress.setJavaReady(len(fileMap)) // unlock nav before detect / plugins / deps
	_ = store.SaveManifest(cacheDir, metaOut)

	if fpSig == "" {
		fpSig = workspaceFingerprint(javaPaths, pathHashes)
	}
	// Java snapshot is stable; pluginsReady flips true after background plugin Index.
	h.setWorkspaceState(fpSig, false)

	rootsCopy := h.roots()
	cacheCopy := cacheDir
	fpCopy := fpSig
	var factsCopy *model.WorkspaceFacts
	if facts != nil {
		fc := *facts
		factsCopy = &fc
	}
	savePacked := writePacked
	postCtx, cancel := context.WithCancel(context.Background())
	h.depsMu.Lock()
	if h.depsCancel != nil {
		h.depsCancel()
	}
	h.depsCancel = cancel
	h.depsMu.Unlock()

	// Detect sampling + plugins + deps after INIT returns — F12 uses the Java
	// core index immediately; framework jumps unlock as plugin Index finishes.
	h.postJavaWG.Add(1)
	go func() {
		defer h.postJavaWG.Done()
		h.enrichDetectFacts(factsCopy)
		if postCtx.Err() != nil {
			return
		}
		activePlugins := h.Plugins.DetectAndActivate(factsCopy)
		activeSet := map[string]bool{}
		for _, p := range activePlugins {
			activeSet[p.ID()] = true
		}
		h.progress.markPlugins(activeSet)
		h.indexPluginsParallel(postCtx, activePlugins, factsCopy)
		if postCtx.Err() != nil {
			return
		}
		h.setWorkspaceState(fpCopy, true)
		h.progress.setDone()
		h.indexDependencySources(postCtx, rootsCopy, cacheCopy, factsCopy)
		if len(toSave) > 0 || savePacked {
			h.persistCacheAsync(cacheCopy, fpCopy, fileMap, toSave, pathHashes, savePacked)
		}
	}()

	_ = ctx
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

// FlushCachePersist waits for background cache writes and post-java work
// (call before process exit).
func (h *Hub) FlushCachePersist() {
	h.postJavaWG.Wait()
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

func (h *Hub) scanAll(ctx context.Context, roots []string) (*model.WorkspaceFacts, []string, error) {
	if len(roots) == 0 {
		return nil, nil, fmt.Errorf("no roots")
	}
	if len(roots) == 1 {
		return h.scan(ctx, roots[0])
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
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		facts, paths, err := h.scan(ctx, root)
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

func (h *Hub) scan(ctx context.Context, root string) (*model.WorkspaceFacts, []string, error) {
	facts := &model.WorkspaceFacts{
		Root:           root,
		AnnotationHits: map[string]int{},
		ImportHits:     map[string]int{},
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return facts, nil, err
	}

	var topDirs []string
	var javaPaths []string
	var mu sync.Mutex
	// Root-level files (rare) stay sequential; subtrees walk in parallel.
	for _, e := range entries {
		if ctx.Err() != nil {
			return facts, javaPaths, ctx.Err()
		}
		name := e.Name()
		full := filepath.Join(root, name)
		if e.IsDir() {
			lower := strings.ToLower(name)
			if h.exclude[lower] {
				if lower == "target" || lower == "build" || lower == "out" {
					h.collectGeneratedJava(ctx, full, &javaPaths, facts)
				}
				continue
			}
			topDirs = append(topDirs, full)
			continue
		}
		h.scanFile(full, name, facts, &javaPaths)
	}

	workers := runtime.NumCPU()
	if workers > len(topDirs) {
		workers = len(topDirs)
	}
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan string, len(topDirs))
	var wg sync.WaitGroup
	var walkErr atomic.Value // error
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			localFacts := &model.WorkspaceFacts{
				AnnotationHits: map[string]int{},
				ImportHits:     map[string]int{},
			}
			var localJava []string
			for dir := range jobs {
				if ctx.Err() != nil {
					return
				}
				err := h.walkTree(ctx, dir, localFacts, &localJava)
				if err != nil && walkErr.Load() == nil {
					walkErr.Store(err)
				}
			}
			mu.Lock()
			mergeScanFacts(facts, localFacts)
			javaPaths = append(javaPaths, localJava...)
			mu.Unlock()
		}()
	}
	for _, d := range topDirs {
		jobs <- d
	}
	close(jobs)
	wg.Wait()
	if v := walkErr.Load(); v != nil {
		err = v.(error)
	}

	// Path-level Detect hints (no I/O): MapStruct packages / generated Impls.
	// Content sampling is deferred to enrichDetectFacts (after Java nav unlock).
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
	facts.JavaFiles = append([]string(nil), javaPaths...)
	return facts, javaPaths, err
}

func mergeScanFacts(dst, src *model.WorkspaceFacts) {
	if src.HasPom {
		dst.HasPom = true
	}
	dst.XMLFiles = append(dst.XMLFiles, src.XMLFiles...)
	dst.ConfigFiles = append(dst.ConfigFiles, src.ConfigFiles...)
	dst.ResourceRoots = append(dst.ResourceRoots, src.ResourceRoots...)
	for k, v := range src.AnnotationHits {
		dst.AnnotationHits[k] += v
	}
	for k, v := range src.ImportHits {
		dst.ImportHits[k] += v
	}
}

func (h *Hub) walkTree(ctx context.Context, treeRoot string, facts *model.WorkspaceFacts, javaPaths *[]string) error {
	return filepath.WalkDir(treeRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() && path != treeRoot {
			lower := strings.ToLower(d.Name())
			if h.exclude[lower] {
				if lower == "target" || lower == "build" || lower == "out" {
					h.collectGeneratedJava(ctx, path, javaPaths, facts)
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
		h.scanFile(path, d.Name(), facts, javaPaths)
		return nil
	})
}

func (h *Hub) scanFile(path, name string, facts *model.WorkspaceFacts, javaPaths *[]string) {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".java"):
		if info, ierr := os.Stat(path); ierr == nil && info.Size() > maxIndexedFileBytes {
			return
		}
		*javaPaths = append(*javaPaths, path)
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
}

// enrichDetectFacts samples Java sources for plugin Detect (parallel reads).
// Called after the core index is published so F12 is not blocked on this I/O.
func (h *Hub) enrichDetectFacts(facts *model.WorkspaceFacts) {
	if facts == nil || len(facts.JavaFiles) == 0 {
		return
	}
	samplePaths := pickDetectSamples(facts.JavaFiles, 60)
	workers := runtime.NumCPU()
	if workers > len(samplePaths) {
		workers = len(samplePaths)
	}
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan string, len(samplePaths))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := &model.WorkspaceFacts{
				AnnotationHits: map[string]int{},
				ImportHits:     map[string]int{},
			}
			for p := range jobs {
				data, err := os.ReadFile(p)
				if err != nil {
					continue
				}
				sampleDetect(string(textenc.Decode(data)), local)
			}
			mu.Lock()
			for k, v := range local.AnnotationHits {
				facts.AnnotationHits[k] += v
			}
			for k, v := range local.ImportHits {
				facts.ImportHits[k] += v
			}
			mu.Unlock()
		}()
	}
	for _, p := range samplePaths {
		jobs <- p
	}
	close(jobs)
	wg.Wait()
	ensureFrameworkDetectHints(facts.JavaFiles, samplePaths, facts)
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
	const maxExtra = 200 // avoid worst-case full-tree reads on huge workspaces
	extra := 0
	for _, p := range all {
		if !needsLombokHint(facts) && !needsMapstructHint(facts) {
			return
		}
		if seen[store.PathKey(p)] {
			continue
		}
		if extra >= maxExtra {
			return
		}
		extra++
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := string(textenc.Decode(data))
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
	// Parse is often IO + CPU bound; use more workers than cores when many files.
	workers := runtime.NumCPU() * 2
	if workers < 4 {
		workers = 4
	}
	if workers > 32 {
		workers = 32
	}
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
					fd := parseSafe(p, string(textenc.Decode(data)), 0)
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
