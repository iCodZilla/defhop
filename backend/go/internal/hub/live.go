package hub

import (
	"defhop/internal/overlay"
	"defhop/internal/plugin"
	"defhop/internal/plugins/spring"
	"defhop/internal/store"
	"defhop/internal/textenc"
	"os"
	"sort"
	"strings"
)

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
	if info, err := os.Stat(path); err == nil && info.Size() > maxIndexedFileBytes {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	h.IndexBufferSync(path, string(textenc.Decode(data)))
	return true
}

// RemovePath drops a file from overlay + symbol index and notifies plugins.
func (h *Hub) RemovePath(path string) {
	key := store.PathKey(path)
	h.Overlay.Remove(key)
	h.mu.Lock()
	defer h.mu.Unlock()
	old := h.Index.RemoveFile(path)
	h.invalidateWorkspaceFP()
	ev := plugin.ChangeEvent{PathKey: key, Path: path, Old: old, New: nil}
	for _, p := range h.Plugins.Active() {
		if pluginHandles(p.ID(), path) {
			p.OnFileChanged(ev)
		}
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
			if cur.Gen < gen {
				h.Index.BumpGen(path, gen)
			}
			h.Overlay.MarkPublished(key, gen)
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
			h.Overlay.MarkPublished(key, gen)
			return
		}
		if cur != nil && cur.ContentHash == sum {
			h.mu.Unlock()
			h.Index.BumpGen(path, gen)
			h.Overlay.MarkPublished(key, gen)
			return
		}
		old := h.Index.PutFile(fd)
		h.invalidateWorkspaceFP()
		ev.Old, ev.New = old, fd
		for _, p := range h.Plugins.Active() {
			if pluginHandles(p.ID(), path) {
				p.OnFileChanged(ev)
			}
		}
		h.mu.Unlock()
		h.Overlay.MarkPublished(key, gen)
		return
	}
	if strings.HasSuffix(lower, ".xml") ||
		strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml") ||
		strings.HasSuffix(lower, ".properties") {
		h.mu.Lock()
		h.invalidateWorkspaceFP()
		for _, p := range h.Plugins.Active() {
			if pluginHandles(p.ID(), path) {
				p.OnFileChanged(ev)
			}
		}
		h.mu.Unlock()
		h.Overlay.MarkPublished(key, gen)
	}
}

// publishStaleOverlays mirrors IntelliJ "lazy index update on first access" /
// PsiDocumentManager.commit*: before DEF/REFS, publish dirty overlays into
// the symbol index so navigation never waits on the async UPDATE worker.
// Only keys marked dirty by Overlay.Put are considered (not every open buffer).
// Prefer the navigation target first, then newest gens. No hard cap — a truncated
// flush is what made multi-file AI edits feel "broken" until a later reindex.
func (h *Hub) publishStaleOverlays(preferPath string) {
	entries := h.Overlay.DirtyMeta()
	if len(entries) == 0 {
		return
	}
	preferKey := store.PathKey(preferPath)
	flushOne := func(e overlay.Meta) bool {
		if e.Path == "" {
			h.Overlay.MarkPublished(e.Key, e.Gen)
			return false
		}
		lower := strings.ToLower(e.Path)
		isJava := strings.HasSuffix(lower, ".java")
		isPluginFile := strings.HasSuffix(lower, ".xml") ||
			strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml") ||
			strings.HasSuffix(lower, ".properties")
		if !isJava && !isPluginFile {
			h.Overlay.MarkPublished(e.Key, e.Gen)
			return false
		}
		if isJava {
			snap := h.Index.Snapshot()
			fd := snap.File(e.Key)
			if fd != nil && fd.Gen >= e.Gen {
				h.Overlay.MarkPublished(e.Key, e.Gen)
				return false
			}
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

// pluginHandles skips plugins that cannot contribute for this path family.
func pluginHandles(id, path string) bool {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".xml"):
		return id == "mybatis"
	case spring.IsConfigPath(path):
		return id == "spring"
	default:
		return true
	}
}

// fileText returns overlay text if present, else disk, else req text.
