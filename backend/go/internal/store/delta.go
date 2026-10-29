package store

import (
	"defhop/internal/model"
	"strings"
)

// publishDeltaLocked patches the previous snapshot for one path change.
// Maps are copy-on-write: unchanged indexes stay shared with the previous snap.
func (s *Store) publishDeltaLocked(pathKey string, old, neu *model.FileSlice) {
	prev := s.snap.Load()
	if prev == nil || prev.Files == nil {
		s.publishLocked()
		return
	}
	c := newCOW(prev)
	if neu != nil {
		c.mutFiles()[pathKey] = neu
	} else {
		delete(c.mutFiles(), pathKey)
	}
	removeFileContribution(c, pathKey, old)
	if neu != nil {
		addFileContribution(c, pathKey, neu)
		normalizeFileSymbols(c.snap, neu)
		// Subtype edges use normalized SuperKeys.
		for _, sym := range neu.Symbols {
			if sym != nil && sym.Kind == "type" {
				addSubtypeEdges(c, sym)
			}
		}
	}
	s.snap.Store(c.snap)
}

// cow tracks which maps have been forked from prev for this publish.
type cow struct {
	prev *Snapshot
	snap *Snapshot
	files, symTab, subtype, flat, simple, method, field, refs bool
}

func newCOW(prev *Snapshot) *cow {
	return &cow{
		prev: prev,
		snap: &Snapshot{
			Files:      prev.Files,
			SymTab:     prev.SymTab,
			SubtypeIdx: prev.SubtypeIdx,
			Flat:       prev.Flat,
			Simple:     prev.Simple,
			MethodIdx:  prev.MethodIdx,
			FieldIdx:   prev.FieldIdx,
			Refs:       prev.Refs,
		},
	}
}

func (c *cow) mutFiles() map[string]*model.FileSlice {
	if !c.files {
		c.snap.Files = clonePtrMap(c.prev.Files)
		c.files = true
	}
	return c.snap.Files
}

func (c *cow) mutSymTab() map[string]*model.Symbol {
	if !c.symTab {
		c.snap.SymTab = clonePtrMap(c.prev.SymTab)
		c.symTab = true
	}
	return c.snap.SymTab
}

func (c *cow) mutSubtype() map[string][]string {
	if !c.subtype {
		c.snap.SubtypeIdx = cloneStringSliceMap(c.prev.SubtypeIdx)
		c.subtype = true
	}
	if c.snap.SubtypeIdx == nil {
		c.snap.SubtypeIdx = make(map[string][]string)
	}
	return c.snap.SubtypeIdx
}

func (c *cow) mutFlat() map[string]string {
	if !c.flat {
		c.snap.Flat = cloneStringMap(c.prev.Flat)
		c.flat = true
	}
	return c.snap.Flat
}

func (c *cow) mutSimple() map[string][]string {
	if !c.simple {
		c.snap.Simple = cloneStringSliceMap(c.prev.Simple)
		c.simple = true
	}
	return c.snap.Simple
}

func (c *cow) mutMethod() map[string][]string {
	if !c.method {
		c.snap.MethodIdx = cloneStringSliceMap(c.prev.MethodIdx)
		c.method = true
	}
	return c.snap.MethodIdx
}

func (c *cow) mutField() map[string][]string {
	if !c.field {
		c.snap.FieldIdx = cloneStringSliceMap(c.prev.FieldIdx)
		c.field = true
	}
	return c.snap.FieldIdx
}

func (c *cow) mutRefs() map[string][]Ref {
	if !c.refs {
		c.snap.Refs = cloneRefMap(c.prev.Refs)
		c.refs = true
	}
	return c.snap.Refs
}

func clonePtrMap[V any](in map[string]*V) map[string]*V {
	out := make(map[string]*V, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneStringSliceMap(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneRefMap(in map[string][]Ref) map[string][]Ref {
	out := make(map[string][]Ref, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func removeFileContribution(c *cow, pathKey string, old *model.FileSlice) {
	if old == nil {
		return
	}
	refKeys := map[string]struct{}{}
	for _, o := range old.Occs {
		if o.TargetKey == "" || o.Location == nil || strings.HasPrefix(o.TargetKey, "L|") {
			continue
		}
		refKeys[o.TargetKey] = struct{}{}
	}
	if len(refKeys) > 0 {
		refs := c.mutRefs()
		for k := range refKeys {
			list := refs[k]
			kept := make([]Ref, 0, len(list))
			for _, r := range list {
				if r.PathKey != pathKey {
					kept = append(kept, r)
				}
			}
			if len(kept) == 0 {
				delete(refs, k)
			} else {
				refs[k] = kept
			}
		}
	}

	removedKeys := map[string]struct{}{}
	for _, sym := range old.Symbols {
		if sym.Key == "" || strings.HasPrefix(sym.Key, "L|") {
			continue
		}
		if cur := c.snap.SymTab[sym.Key]; cur == sym {
			delete(c.mutSymTab(), sym.Key)
			removedKeys[sym.Key] = struct{}{}
		}
		unindexSymbol(c, sym)
	}
	if len(removedKeys) == 0 {
		return
	}
	// Rare: same key defined in another file — restore that definition.
	for pk, fd := range c.snap.Files {
		if pk == pathKey || fd == nil {
			continue
		}
		for _, sym := range fd.Symbols {
			if _, ok := removedKeys[sym.Key]; !ok || sym.Key == "" {
				continue
			}
			if _, exists := c.snap.SymTab[sym.Key]; exists {
				continue
			}
			c.mutSymTab()[sym.Key] = sym
			indexSymbol(c, sym)
			if sym.Kind == "type" {
				addSubtypeEdges(c, sym)
			}
			delete(removedKeys, sym.Key)
			if len(removedKeys) == 0 {
				return
			}
		}
	}
}

func addFileContribution(c *cow, pathKey string, fd *model.FileSlice) {
	if fd == nil {
		return
	}
	pathKey = Intern(pathKey)
	for _, sym := range fd.Symbols {
		if sym.Key == "" || strings.HasPrefix(sym.Key, "L|") {
			continue
		}
		sym.Key = Intern(sym.Key)
		if sym.OwnerKey != "" {
			sym.OwnerKey = Intern(sym.OwnerKey)
		}
		c.mutSymTab()[sym.Key] = sym
		indexSymbol(c, sym)
		// type subtype edges added after normalize in publishDeltaLocked
	}
	if len(fd.Occs) == 0 {
		return
	}
	refs := c.mutRefs()
	for _, o := range fd.Occs {
		if o.TargetKey == "" || o.Location == nil || strings.HasPrefix(o.TargetKey, "L|") {
			continue
		}
		tk := Intern(o.TargetKey)
		refs[tk] = append(refs[tk], Ref{
			PathKey: pathKey, Loc: o.Location, Role: o.Role,
		})
	}
}

func indexSymbol(c *cow, sym *model.Symbol) {
	switch sym.Kind {
	case "type":
		flat := c.mutFlat()
		flat[sym.Name] = sym.Key
		flat[strings.ReplaceAll(sym.Name, "$", ".")] = sym.Key
		simple := simpleName(sym.Name)
		c.mutSimple()[simple] = appendUnique(c.snap.Simple[simple], sym.Key)
	case "method", "constructor":
		mk := sym.OwnerKey + "#" + sym.Name
		c.mutMethod()[mk] = appendUnique(c.snap.MethodIdx[mk], sym.Key)
	case "field":
		if sym.OwnerKey != "" {
			c.mutField()[sym.OwnerKey] = appendUnique(c.snap.FieldIdx[sym.OwnerKey], sym.Key)
		}
	}
}

func unindexSymbol(c *cow, sym *model.Symbol) {
	switch sym.Kind {
	case "type":
		flat := c.mutFlat()
		if flat[sym.Name] == sym.Key {
			delete(flat, sym.Name)
		}
		dotted := strings.ReplaceAll(sym.Name, "$", ".")
		if flat[dotted] == sym.Key {
			delete(flat, dotted)
		}
		simple := simpleName(sym.Name)
		sm := c.mutSimple()
		sm[simple] = removeString(sm[simple], sym.Key)
		if len(sm[simple]) == 0 {
			delete(sm, simple)
		}
		removeSubtypeEdges(c, sym)
	case "method", "constructor":
		mk := sym.OwnerKey + "#" + sym.Name
		mi := c.mutMethod()
		mi[mk] = removeString(mi[mk], sym.Key)
		if len(mi[mk]) == 0 {
			delete(mi, mk)
		}
	case "field":
		if sym.OwnerKey != "" {
			fi := c.mutField()
			fi[sym.OwnerKey] = removeString(fi[sym.OwnerKey], sym.Key)
			if len(fi[sym.OwnerKey]) == 0 {
				delete(fi, sym.OwnerKey)
			}
		}
	}
}

func addSubtypeEdges(c *cow, sym *model.Symbol) {
	if sym == nil || sym.Kind != "type" || len(sym.SuperKeys) == 0 {
		return
	}
	idx := c.mutSubtype()
	for _, sk := range sym.SuperKeys {
		if sk == "" {
			continue
		}
		idx[sk] = appendUnique(idx[sk], sym.Key)
	}
}

func removeSubtypeEdges(c *cow, sym *model.Symbol) {
	if sym == nil || sym.Kind != "type" || len(sym.SuperKeys) == 0 {
		return
	}
	idx := c.mutSubtype()
	for _, sk := range sym.SuperKeys {
		idx[sk] = removeString(idx[sk], sym.Key)
		if len(idx[sk]) == 0 {
			delete(idx, sk)
		}
	}
}

func normalizeFileSymbols(snap *Snapshot, fd *model.FileSlice) {
	if fd == nil {
		return
	}
	for _, sym := range fd.Symbols {
		if sym.Kind == "field" && sym.TypeKey != "" {
			sym.TypeKey = normalizeTypeKey(snap, sym.TypeKey, fd.Imports)
		}
		if sym.Kind == "type" {
			for i, sk := range sym.SuperKeys {
				sym.SuperKeys[i] = normalizeTypeKey(snap, sk, fd.Imports)
			}
		}
		if sym.Kind == "method" && sym.ReturnType != "" {
			sym.ReturnType = normalizeTypeKey(snap, sym.ReturnType, fd.Imports)
		}
	}
}

func appendUnique(list []string, key string) []string {
	for _, x := range list {
		if x == key {
			return list
		}
	}
	out := make([]string, len(list)+1)
	copy(out, list)
	out[len(list)] = key
	return out
}

func removeString(list []string, key string) []string {
	if len(list) == 0 {
		return list
	}
	out := make([]string, 0, len(list))
	for _, x := range list {
		if x != key {
			out = append(out, x)
		}
	}
	return out
}
