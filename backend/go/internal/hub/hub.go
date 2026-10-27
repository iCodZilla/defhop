package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"defhop/internal/java"
	"defhop/internal/model"
	"defhop/internal/overlay"
	"defhop/internal/plugin"
	"defhop/internal/plugins/feign"
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
	Root     string
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
}

func New(plugins ...plugin.Plugin) *Hub {
	return &Hub{
		Overlay:  overlay.New(),
		Index:    store.New(),
		Plugins:  plugin.NewRegistry(plugins...),
		progress: newProgressTracker(),
		exclude: map[string]bool{
			".git": true, ".gradle": true, ".idea": true, ".settings": true,
			".vscode": true, "bin": true, "build": true, "classes": true,
			"dist": true, "logs": true, "node_modules": true, "out": true, "target": true,
		},
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

// Init cold-starts the workspace: scan -> Java core parallel parse -> plugin detect+parallel index.
func (h *Hub) Init(ctx context.Context, root, cacheDir string) (files, symbols, refs int, elapsedMs int64, active []string, err error) {
	start := time.Now()
	h.Root = root
	h.CacheDir = cacheDir
	h.progress = newProgressTracker()
	h.progress.setScan("scanning...")

	facts, javaPaths, err := h.scan(root)
	if err != nil {
		return 0, 0, 0, 0, nil, err
	}
	h.progress.setScanDone(len(javaPaths),
		fmt.Sprintf("scan done: %d java, %d xml", len(facts.JavaFiles), len(facts.XMLFiles)))

	store.EnsureCacheVersion(cacheDir)
	prevMeta := store.LoadManifest(cacheDir)
	fps := store.FingerprintFiles(javaPaths, prevMeta, func(done, total int) {
		h.progress.setHashing(done, total)
	})

	pathHashes := make(map[string]string, len(javaPaths)*3)
	pathBytes := make(map[string][]byte)
	metaOut := make(map[string]store.FileMeta, len(javaPaths))
	for _, fp := range fps {
		if fp.Hash == "" {
			continue
		}
		p := fp.Path
		slash := filepath.ToSlash(p)
		key := store.PathKey(p)
		pathHashes[p] = fp.Hash
		pathHashes[slash] = fp.Hash
		pathHashes[key] = fp.Hash
		if len(fp.Data) > 0 {
			pathBytes[p] = fp.Data
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
			pathHashes[p] = sum
			pathHashes[filepath.ToSlash(p)] = sum
			pathHashes[store.PathKey(p)] = sum
			if info, err := os.Stat(p); err == nil {
				metaOut[store.PathKey(p)] = store.FileMeta{
					Hash: sum, Size: info.Size(), Mtime: info.ModTime().UnixNano(),
				}
			}
		}
	}
	parsed := h.parseJavaParallel(ctx, toParse, pathBytes, cachedN, len(javaPaths))
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
	h.progress.setJavaReady(len(fileMap)) // unlock nav before disk / plugins
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

	if len(toSave) > 0 || writePacked {
		h.persistCacheAsync(cacheDir, fpSig, fileMap, toSave, pathHashes, writePacked)
	}

	return h.statusCounts(time.Since(start).Milliseconds())
}

// persistCacheAsync writes per-file gobs + packed workspace.gob off the Init critical path.
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

// FlushCachePersist waits for background cache writes (call before process exit).
func (h *Hub) FlushCachePersist() {
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

	// Sample Java files for Detect hints ? prefer mapstruct / mapper-ish paths.
	samplePaths := pickDetectSamples(javaPaths, 60)
	for _, p := range samplePaths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		sampleDetect(string(data), facts)
	}
	return facts, javaPaths, err
}

func pickDetectSamples(paths []string, limit int) []string {
	if len(paths) <= limit {
		return paths
	}
	var prefer, rest []string
	for _, p := range paths {
		lower := strings.ToLower(filepath.ToSlash(p))
		if strings.Contains(lower, "mapstruct") || strings.Contains(lower, "/mapper") ||
			strings.Contains(lower, "/provider/") || strings.Contains(lower, "feign") ||
			strings.Contains(lower, "generated-sources") || strings.Contains(lower, "/generated/") {
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
		"Data", "Getter", "Setter", "Builder", "Slf4j", "Mapping", "Mappings",
		"Component", "Service", "Repository", "Controller", "RestController",
		"Autowired", "Resource", "Bean", "Configuration", "SpringBootApplication",
		"Value", "ConfigurationProperties", "FeignClient", "EnableFeignClients"} {
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
			fd = nil
		}
	}()
	return java.Parse(path, text, gen)
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
// the file in the background. Navigation never waits on this parse.
func (h *Hub) UpdateBuffer(path, text string) uint64 {
	key := store.PathKey(path)
	gen := h.Overlay.Put(key, text)
	go h.indexBufferAsync(path, key, text, gen)
	return gen
}

func (h *Hub) indexBufferAsync(path, key, text string, gen uint64) {
	// Drop superseded buffers ? only the latest gen is published.
	if h.Overlay.Gen(key) != gen {
		return
	}
	lower := strings.ToLower(path)
	ev := plugin.ChangeEvent{PathKey: key, Path: path, Text: text}
	if strings.HasSuffix(lower, ".java") {
		fd := parseSafe(path, text, gen)
		if fd == nil {
			return
		}
		if h.Overlay.Gen(key) != gen {
			return
		}
		h.mu.Lock()
		if h.Overlay.Gen(key) != gen {
			h.mu.Unlock()
			return
		}
		old := h.Index.PutFile(fd)
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
		for _, p := range h.Plugins.Active() {
			p.OnFileChanged(ev)
		}
		h.mu.Unlock()
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
		fresh := parseSafe(req.Path, text, gen)
		if fresh != nil {
			h.mu.Lock()
			// Another request may have published the same/newer gen already.
			cur := h.Index.Snapshot().File(key)
			if cur == nil || cur.Gen < gen {
				old := h.Index.PutFile(fresh)
				for _, p := range h.Plugins.Active() {
					p.OnFileChanged(plugin.ChangeEvent{
						PathKey: key, Path: req.Path, Text: text, Old: old, New: fresh,
					})
				}
			}
			h.mu.Unlock()
			snap = h.Index.Snapshot()
			fd = snap.File(key)
		}
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
					if ts := snap.Symbol(tkey); ts != nil && ts.Declaration != nil {
						return []*model.Location{ts.Declaration}, ""
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
			return []*model.Location{sym.Declaration}, ""
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
