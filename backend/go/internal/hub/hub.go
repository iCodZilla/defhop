package hub

import (
	"context"
	"defhop/internal/overlay"
	"defhop/internal/plugin"
	"defhop/internal/store"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Hub struct {
	Root     string   // primary root (status / cache default)
	Roots    []string // all workspace roots indexed together
	CacheDir string
	Overlay  *overlay.Store
	Index    *store.Store
	Plugins  *plugin.Registry

	exclude  map[string]bool
	mu       sync.Mutex // serializes index mutations from UPDATE path
	indexMu  sync.Mutex // serializes full (re)index runs
	stateMu  sync.Mutex // guards Root/Roots/CacheDir + lastWorkspaceFP/pluginsReady
	progress *progressTracker

	// lastWorkspaceFP fingerprints the last successful full index (path?hash).
	// REINDEX with the same fingerprint skips gob reload + snapshot + plugins.
	lastWorkspaceFP string
	pluginsReady    bool

	// cachePersistWG tracks async disk writes (per-file gobs + packed workspace.gob).
	cachePersistWG sync.WaitGroup
	// postJavaWG tracks plugins + dependency indexing after Java nav is ready.
	postJavaWG sync.WaitGroup

	depsMu     sync.Mutex
	depsCancel context.CancelFunc // cancels in-flight post-java work (plugins/deps)
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

// maxIndexedFileBytes skips machine-generated monsters (single-file bundles,
// protobuf dumps). They are rarely navigated and dominate parse time/memory.
const maxIndexedFileBytes = 8 << 20

func (h *Hub) setConfig(root string, roots []string, cacheDir string) {
	h.stateMu.Lock()
	h.Root = root
	h.Roots = roots
	h.CacheDir = cacheDir
	h.stateMu.Unlock()
}

func (h *Hub) cacheDir() string {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	return h.CacheDir
}

func (h *Hub) roots() []string {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	return append([]string(nil), h.Roots...)
}

func (h *Hub) workspaceState() (fp string, pluginsReady bool) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	return h.lastWorkspaceFP, h.pluginsReady
}

func (h *Hub) setWorkspaceState(fp string, pluginsReady bool) {
	h.stateMu.Lock()
	h.lastWorkspaceFP = fp
	h.pluginsReady = pluginsReady
	h.stateMu.Unlock()
}

func (h *Hub) invalidateWorkspaceFP() {
	h.stateMu.Lock()
	h.lastWorkspaceFP = ""
	h.stateMu.Unlock()
}

func (h *Hub) resetProgress() {
	if h.progress == nil {
		h.progress = newProgressTracker()
		return
	}
	h.progress.Reset()
}

// StopBackgroundDeps cancels post-java work and waits for in-flight cache
// writes; tests call it before removing a temporary workspace.
func (h *Hub) StopBackgroundDeps() { h.stopBackgroundDeps() }

// stopBackgroundDeps cancels plugins/deps indexing and waits briefly so Rebuild
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
		h.postJavaWG.Wait()
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
