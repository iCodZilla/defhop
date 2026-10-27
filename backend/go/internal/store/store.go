package store

import (
	"defhop/internal/model"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Snapshot is an immutable published view for navigation reads.
type Snapshot struct {
	Files    map[string]*model.FileSlice
	SymTab  map[string]*model.Symbol
	SubtypeIdx map[string][]string
	Flat       map[string]string   // fqn -> T|key
	Simple     map[string][]string // simple name -> keys
	MethodIdx  map[string][]string // owner#name -> keys
	Refs       map[string][]Ref    // symbolKey -> refs
}

// Ref is a lightweight reference handle.
type Ref struct {
	PathKey string
	Loc     *model.Location
	Role    string
}

// Store holds mutable index state and publishes atomic snapshots.
type Store struct {
	mu    sync.Mutex
	files map[string]*model.FileSlice
	snap  atomic.Pointer[Snapshot]
}

func New() *Store {
	s := &Store{files: make(map[string]*model.FileSlice)}
	s.publishLocked()
	return s
}

func PathKey(p string) string {
	return strings.ToLower(filepath.ToSlash(p))
}

// Snapshot returns the latest published read view.
func (s *Store) Snapshot() *Snapshot {
	return s.snap.Load()
}

// PutFile replaces one file and republishes.
func (s *Store) PutFile(fd *model.FileSlice) *model.FileSlice {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := PathKey(fd.Path)
	old := s.files[key]
	s.files[key] = fd
	s.publishLocked()
	return old
}

// RemoveFile drops a file.
func (s *Store) RemoveFile(path string) *model.FileSlice {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := PathKey(path)
	old := s.files[key]
	delete(s.files, key)
	s.publishLocked()
	return old
}

// ReplaceAll swaps the entire file map (used by cold INIT).
func (s *Store) ReplaceAll(files map[string]*model.FileSlice) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files = files
	s.publishLocked()
}

// FileCount returns indexed file count under lock.
func (s *Store) FileCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.files)
}

func (s *Store) publishLocked() {
	snap := buildSnapshot(s.files)
	s.snap.Store(snap)
}

func buildSnapshot(files map[string]*model.FileSlice) *Snapshot {
	snap := &Snapshot{
		Files:    make(map[string]*model.FileSlice, len(files)),
		SymTab:  make(map[string]*model.Symbol),
		SubtypeIdx: make(map[string][]string),
		Flat:       make(map[string]string),
		Simple:     make(map[string][]string),
		MethodIdx:  make(map[string][]string),
		Refs:       make(map[string][]Ref),
	}
	paths := make([]string, 0, len(files))
	for k, v := range files {
		snap.Files[k] = v
		paths = append(paths, k)
	}
	sort.Strings(paths)

	for _, p := range paths {
		fd := files[p]
		for _, sym := range fd.Symbols {
			if sym.Key == "" || strings.HasPrefix(sym.Key, "L|") {
				continue
			}
			if _, ok := snap.SymTab[sym.Key]; !ok {
				snap.SymTab[sym.Key] = sym
			}
			switch sym.Kind {
			case "type":
				snap.Flat[sym.Name] = sym.Key
				snap.Flat[strings.ReplaceAll(sym.Name, "$", ".")] = sym.Key
				simple := simpleName(sym.Name)
				snap.Simple[simple] = append(snap.Simple[simple], sym.Key)
			case "method", "constructor":
				mk := sym.OwnerKey + "#" + sym.Name
				snap.MethodIdx[mk] = append(snap.MethodIdx[mk], sym.Key)
			}
		}
		for _, o := range fd.Occs {
			if o.TargetKey == "" || o.Location == nil {
				continue
			}
			// Locals are brace-scoped; never put them in the global inverted index.
			if strings.HasPrefix(o.TargetKey, "L|") {
				continue
			}
			snap.Refs[o.TargetKey] = append(snap.Refs[o.TargetKey], Ref{
				PathKey: p, Loc: o.Location, Role: o.Role,
			})
		}
	}

	// Normalize type keys using imports, then build subtype graph.
	for _, fd := range files {
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
	for _, sym := range snap.SymTab {
		if sym.Kind != "type" {
			continue
		}
		for _, sk := range sym.SuperKeys {
			snap.SubtypeIdx[sk] = append(snap.SubtypeIdx[sk], sym.Key)
		}
	}
	return snap
}

func normalizeTypeKey(snap *Snapshot, key string, imports []string) string {
	if key == "" {
		return key
	}
	if _, ok := snap.SymTab[key]; ok {
		return key
	}
	simple := simpleName(strings.TrimPrefix(key, "T|"))
	for _, imp := range imports {
		clean := strings.TrimSuffix(imp, ".*")
		if strings.HasSuffix(clean, "."+simple) {
			c := "T|" + clean
			if _, ok := snap.SymTab[c]; ok {
				return c
			}
		}
	}
	cands := snap.Simple[simple]
	if len(cands) == 1 {
		return cands[0]
	}
	if len(cands) > 1 {
		for _, c := range cands {
			pkg := packageOf(strings.TrimPrefix(c, "T|"))
			for _, imp := range imports {
				clean := strings.TrimSuffix(imp, ".*")
				if clean == pkg {
					return c
				}
			}
		}
	}
	return key
}

// --- Snapshot helpers implementing plugin.Core ---

func (snap *Snapshot) Symbol(key string) *model.Symbol {
	if snap == nil {
		return nil
	}
	return snap.SymTab[key]
}

func (snap *Snapshot) Symbols() map[string]*model.Symbol {
	if snap == nil {
		return nil
	}
	return snap.SymTab
}

func (snap *Snapshot) Methods(ownerKey, name string) []*model.Symbol {
	if snap == nil {
		return nil
	}
	keys := snap.MethodIdx[ownerKey+"#"+name]
	out := make([]*model.Symbol, 0, len(keys))
	for _, k := range keys {
		if s := snap.SymTab[k]; s != nil {
			out = append(out, s)
		}
	}
	return out
}

func (snap *Snapshot) Subtypes(typeKey string) []string {
	if snap == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	q := []string{typeKey}
	for len(q) > 0 {
		cur := q[0]
		q = q[1:]
		for _, child := range snap.SubtypeIdx[cur] {
			if !seen[child] {
				seen[child] = true
				out = append(out, child)
				q = append(q, child)
			}
		}
	}
	return out
}

func (snap *Snapshot) File(pathKey string) *model.FileSlice {
	if snap == nil {
		return nil
	}
	return snap.Files[pathKey]
}

func (snap *Snapshot) ResolveTypeKey(key string, fd *model.FileSlice) string {
	imps := []string(nil)
	if fd != nil {
		imps = fd.Imports
	}
	return normalizeTypeKey(snap, key, imps)
}

func (snap *Snapshot) FindField(typeKey, name string) *model.Symbol {
	if snap == nil || typeKey == "" || name == "" {
		return nil
	}
	direct := "F|" + typeKey + "|" + name
	if s := snap.SymTab[direct]; s != nil {
		return s
	}
	// Case-insensitive match on this type (getCInquiryCode <-> cInquiryCode).
	for _, s := range snap.SymTab {
		if s.Kind == "field" && s.OwnerKey == typeKey && strings.EqualFold(s.Name, name) {
			return s
		}
	}
	owner := snap.SymTab[typeKey]
	if owner == nil {
		return nil
	}
	for _, sk := range owner.SuperKeys {
		if f := snap.FindField(sk, name); f != nil {
			return f
		}
	}
	return nil
}

func (snap *Snapshot) FindMethodInHierarchy(typeKey, name string) *model.Symbol {
	if snap == nil {
		return nil
	}
	seen := map[string]bool{}
	q := []string{typeKey}
	for len(q) > 0 {
		cur := q[0]
		q = q[1:]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		if ms := snap.MethodIdx[cur+"#"+name]; len(ms) > 0 {
			if s := snap.SymTab[ms[0]]; s != nil {
				return s
			}
		}
		if sym := snap.SymTab[cur]; sym != nil {
			q = append(q, sym.SuperKeys...)
		}
	}
	return nil
}

func simpleName(name string) string {
	if i := strings.LastIndexAny(name, ".$"); i >= 0 {
		return name[i+1:]
	}
	return name
}

func packageOf(fqn string) string {
	if i := strings.LastIndex(fqn, "."); i >= 0 {
		return fqn[:i]
	}
	return ""
}
