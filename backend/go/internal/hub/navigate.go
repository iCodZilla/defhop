package hub

import (
	"defhop/internal/java"
	"defhop/internal/model"
	"defhop/internal/plugins/feign"
	"defhop/internal/plugins/spring"
	"defhop/internal/store"
	"defhop/internal/textenc"
	"fmt"
	"os"
	"strings"
)

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
	return string(textenc.Decode(data)), 0
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
			if !pluginHandles(p.ID(), req.Path) {
				continue
			}
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

	// Plugin contributions first (override-friendly); path-routed for xml/config.
	for _, p := range h.Plugins.Active() {
		if !pluginHandles(p.ID(), req.Path) {
			continue
		}
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
		if !pluginHandles(p.ID(), req.Path) {
			continue
		}
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
