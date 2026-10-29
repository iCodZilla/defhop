package hub

import (
	"defhop/internal/java"
	"defhop/internal/model"
	"defhop/internal/store"
	"strings"
)

// SymbolItem is a lightweight outline / hierarchy node for the VS Code host.
type SymbolItem struct {
	Name         string       `json:"name"`
	Kind         string       `json:"kind"`
	Detail       string       `json:"detail,omitempty"`
	Path         string       `json:"path"`
	Line         int          `json:"line"`
	Character    int          `json:"character"`
	EndLine      int          `json:"endLine"`
	EndCharacter int          `json:"endCharacter"`
	Children     []SymbolItem `json:"children,omitempty"`
	Key          string       `json:"key,omitempty"`
}

// Outline returns nested document symbols for a Java file.
func (h *Hub) Outline(path string) []SymbolItem {
	h.publishStaleOverlays(path)
	snap := h.Index.Snapshot()
	if snap == nil {
		return nil
	}
	fd := snap.File(store.PathKey(path))
	if fd == nil {
		return nil
	}
	byOwner := map[string][]*model.Symbol{}
	var types []*model.Symbol
	for _, sym := range fd.Symbols {
		if sym == nil || sym.Declaration == nil || strings.HasPrefix(sym.Key, "L|") {
			continue
		}
		switch sym.Kind {
		case "type":
			types = append(types, sym)
		case "method", "constructor", "field":
			byOwner[sym.OwnerKey] = append(byOwner[sym.OwnerKey], sym)
		}
	}
	out := make([]SymbolItem, 0, len(types))
	for _, t := range types {
		item := symbolItemFrom(t)
		for _, child := range byOwner[t.Key] {
			item.Children = append(item.Children, symbolItemFrom(child))
		}
		out = append(out, item)
	}
	// Orphan members (no owning type in this file).
	seenOwners := map[string]bool{}
	for _, t := range types {
		seenOwners[t.Key] = true
	}
	for owner, members := range byOwner {
		if seenOwners[owner] {
			continue
		}
		for _, m := range members {
			out = append(out, symbolItemFrom(m))
		}
	}
	return out
}

// TypeHierarchy resolves the type at position and returns supers + subtypes.
func (h *Hub) TypeHierarchy(path string, line, character int) (item *SymbolItem, supers, subtypes []SymbolItem) {
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
		return nil, nil, nil
	}
	offset := java.OffsetAt(text, line, character)
	token := java.TokenAt(text, offset)
	sym, _ := java.Resolve(snap, fd, offset, token, text)
	if sym == nil {
		return nil, nil, nil
	}
	// Prefer the owning type for members.
	typeSym := sym
	if sym.Kind != "type" && sym.OwnerKey != "" {
		if owner := snap.Symbol(sym.OwnerKey); owner != nil {
			typeSym = owner
		}
	}
	if typeSym.Kind != "type" || typeSym.Declaration == nil {
		return nil, nil, nil
	}
	it := symbolItemFrom(typeSym)
	item = &it
	for _, sk := range typeSym.SuperKeys {
		sk = snap.ResolveTypeKey(sk, fd)
		if s := snap.Symbol(sk); s != nil && s.Declaration != nil {
			supers = append(supers, symbolItemFrom(s))
		}
	}
	for _, subKey := range snap.Subtypes(typeSym.Key) {
		if s := snap.Symbol(subKey); s != nil && s.Declaration != nil {
			subtypes = append(subtypes, symbolItemFrom(s))
		}
	}
	return item, supers, subtypes
}

// WorkspaceSymbols finds types/methods/fields by simple-name query (Ctrl+T).
func (h *Hub) WorkspaceSymbols(query string, limit int) []SymbolItem {
	if limit <= 0 {
		limit = 100
	}
	snap := h.Index.Snapshot()
	if snap == nil {
		return nil
	}
	q := strings.TrimSpace(query)
	ql := strings.ToLower(q)
	var out []SymbolItem
	seen := map[string]bool{}
	add := func(sym *model.Symbol) {
		if sym == nil || sym.Declaration == nil || seen[sym.Key] {
			return
		}
		if strings.HasPrefix(sym.Key, "L|") {
			return
		}
		seen[sym.Key] = true
		out = append(out, symbolItemFrom(sym))
	}
	if q == "" {
		// Empty query: sample some types for a quick list.
		for _, keys := range snap.Simple {
			for _, k := range keys {
				add(snap.SymTab[k])
				if len(out) >= limit {
					return out
				}
			}
		}
		return out
	}
	// Exact / prefix hits on simple names first.
	for simple, keys := range snap.Simple {
		sl := strings.ToLower(simple)
		if sl == ql || strings.HasPrefix(sl, ql) || strings.Contains(sl, ql) {
			for _, k := range keys {
				add(snap.SymTab[k])
				if len(out) >= limit {
					return out
				}
			}
		}
	}
	// Methods / fields by name substring.
	for _, sym := range snap.SymTab {
		if sym == nil || (sym.Kind != "method" && sym.Kind != "field" && sym.Kind != "constructor") {
			continue
		}
		if strings.Contains(strings.ToLower(sym.Name), ql) {
			add(sym)
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

func symbolItemFrom(sym *model.Symbol) SymbolItem {
	d := sym.Declaration
	item := SymbolItem{
		Name: sym.Name,
		Kind: sym.Kind,
		Key:  sym.Key,
	}
	if d != nil {
		item.Path = d.Path
		item.Line = d.Line
		item.Character = d.Character
		item.EndLine = d.EndLine
		item.EndCharacter = d.EndCharacter
		if item.EndLine == 0 && item.EndCharacter == 0 {
			item.EndLine = d.Line
			item.EndCharacter = d.Character + len(sym.Name)
		}
	}
	if sym.Kind == "type" {
		if i := strings.LastIndexByte(sym.Name, '.'); i >= 0 {
			item.Name = sym.Name[i+1:]
			item.Detail = sym.Name
		}
	}
	return item
}
