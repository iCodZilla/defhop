package hub

import (
	"fmt"
	"defhop/internal/java"
	"defhop/internal/model"
	"defhop/internal/store"
	"strings"
)

// HoverInfo is markdown + optional jump target for the VS Code hover provider.
type HoverInfo struct {
	Markdown string       `json:"markdown"`
	Item     *SymbolItem  `json:"item,omitempty"`
}

// Hover resolves the symbol under the cursor and returns a short signature card.
func (h *Hub) Hover(path string, line, character int) *HoverInfo {
	sym, snap, _ := h.resolveAt(path, line, character)
	if sym == nil {
		return nil
	}
	item := symbolItemFrom(sym)
	var b strings.Builder
	b.WriteString("```java\n")
	switch sym.Kind {
	case "type":
		if sym.InterfaceType {
			b.WriteString("interface ")
		} else {
			b.WriteString("class ")
		}
		b.WriteString(sym.Name)
	case "method", "constructor":
		if sym.ReturnType != "" && sym.Kind == "method" {
			b.WriteString(strings.TrimPrefix(sym.ReturnType, "T|"))
			b.WriteByte(' ')
		}
		if sym.OwnerKey != "" {
			if owner := snap.Symbol(sym.OwnerKey); owner != nil {
				b.WriteString(simpleTail(owner.Name))
				b.WriteByte('.')
			}
		}
		b.WriteString(sym.Name)
		b.WriteString("()")
	case "field":
		if sym.TypeKey != "" {
			b.WriteString(strings.TrimPrefix(sym.TypeKey, "T|"))
			b.WriteByte(' ')
		}
		b.WriteString(sym.Name)
	default:
		b.WriteString(sym.Name)
	}
	b.WriteString("\n```\n")
	if sym.Kind == "type" || sym.Kind == "method" || sym.Kind == "field" {
		b.WriteString("*")
		b.WriteString(sym.Kind)
		b.WriteString("* — Defhop")
	}
	return &HoverInfo{Markdown: b.String(), Item: &item}
}

// CallHierarchy returns the callable at position plus incoming / outgoing calls.
func (h *Hub) CallHierarchy(path string, line, character int) (item *SymbolItem, incoming, outgoing []SymbolItem) {
	sym, snap, fd := h.resolveAt(path, line, character)
	if sym == nil {
		return nil, nil, nil
	}
	if sym.Kind != "method" && sym.Kind != "constructor" {
		return nil, nil, nil
	}
	it := symbolItemFrom(sym)
	item = &it

	seenIn := map[string]bool{}
	for _, loc := range java.References(snap, sym, false) {
		if loc == nil {
			continue
		}
		caller := enclosingCallable(snap, loc.Path, loc.Line)
		if caller != nil && caller.Declaration != nil {
			if seenIn[caller.Key] {
				continue
			}
			seenIn[caller.Key] = true
			incoming = append(incoming, symbolItemFrom(caller))
		} else {
			key := fmt.Sprintf("%s:%d", loc.Path, loc.Line)
			if seenIn[key] {
				continue
			}
			seenIn[key] = true
			incoming = append(incoming, SymbolItem{
				Name:         fmt.Sprintf("%s:%d", baseName(loc.Path), loc.Line+1),
				Kind:         "reference",
				Detail:       loc.Label,
				Path:         loc.Path,
				Line:         loc.Line,
				Character:    loc.Character,
				EndLine:      loc.EndLine,
				EndCharacter: loc.EndCharacter,
			})
		}
		if len(incoming) >= 200 {
			break
		}
	}

	if fd != nil && sym.Declaration != nil {
		startLine := sym.Declaration.Line
		endLine := startLine + 500
		for _, peer := range fd.Symbols {
			if peer == nil || peer.Declaration == nil || peer.OwnerKey != sym.OwnerKey {
				continue
			}
			if peer.Kind != "method" && peer.Kind != "constructor" && peer.Kind != "field" {
				continue
			}
			if peer.Declaration.Line > startLine && peer.Declaration.Line < endLine {
				endLine = peer.Declaration.Line
			}
		}
		seen := map[string]bool{}
		for _, o := range fd.Occs {
			if o == nil || o.Location == nil || o.TargetKey == "" {
				continue
			}
			if o.Location.Line < startLine || o.Location.Line >= endLine {
				continue
			}
			if o.Role == "declaration" {
				continue
			}
			target := snap.Symbol(o.TargetKey)
			if target == nil || (target.Kind != "method" && target.Kind != "constructor") {
				continue
			}
			if target.Key == sym.Key || seen[target.Key] {
				continue
			}
			seen[target.Key] = true
			outgoing = append(outgoing, symbolItemFrom(target))
			if len(outgoing) >= 80 {
				break
			}
		}
	}
	return item, incoming, outgoing
}

func (h *Hub) resolveAt(path string, line, character int) (*model.Symbol, *store.Snapshot, *model.FileSlice) {
	h.publishStaleOverlays(path)
	key := store.PathKey(path)
	text, gen := h.fileText(path, "")
	if text == "" {
		return nil, nil, nil
	}
	snap := h.Index.Snapshot()
	fd := snap.File(key)
	if fd == nil || (gen > 0 && fd.Gen < gen) {
		h.indexOneBuffer(path, key, text, gen)
		snap = h.Index.Snapshot()
		fd = snap.File(key)
	}
	if fd == nil {
		return nil, snap, nil
	}
	offset := java.OffsetAt(text, line, character)
	token := java.TokenAt(text, offset)
	sym, _ := java.Resolve(snap, fd, offset, token, text)
	return sym, snap, fd
}

// enclosingCallable finds the nearest method/constructor declared at or above line.
func enclosingCallable(snap *store.Snapshot, path string, line int) *model.Symbol {
	if snap == nil || path == "" {
		return nil
	}
	fd := snap.File(store.PathKey(path))
	if fd == nil {
		return nil
	}
	var best *model.Symbol
	bestLine := -1
	for _, s := range fd.Symbols {
		if s == nil || s.Declaration == nil {
			continue
		}
		if s.Kind != "method" && s.Kind != "constructor" {
			continue
		}
		if s.Declaration.Line <= line && s.Declaration.Line >= bestLine {
			best = s
			bestLine = s.Declaration.Line
		}
	}
	return best
}

func simpleTail(name string) string {
	if i := strings.LastIndexAny(name, ".$"); i >= 0 {
		return name[i+1:]
	}
	return name
}

func baseName(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}
