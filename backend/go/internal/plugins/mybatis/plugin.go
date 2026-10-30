package mybatis

import (
	"context"
	"defhop/internal/java"
	"defhop/internal/model"
	"defhop/internal/plugin"
	"defhop/internal/store"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Plugin provides:
//   - Mapper method ↔ XML statement navigation
//   - MyBatis-Plus ServiceImpl baseMapper → concrete Mapper type
//
// Loaded only when Detect hits. Framework-only BaseMapper/ServiceImpl APIs that
// are not present in the workspace index are intentionally not navigable.
type Plugin struct {
	mu          sync.RWMutex
	byPath      map[string]*Mapper
	byNamespace map[string][]*Mapper
	// serviceMappers: service impl type key -> mapper type key (MyBatis-Plus)
	serviceMappers map[string]string
}

func New() *Plugin {
	return &Plugin{
		byPath:         make(map[string]*Mapper),
		byNamespace:    make(map[string][]*Mapper),
		serviceMappers: make(map[string]string),
	}
}

func (p *Plugin) ID() string { return "mybatis" }

func (p *Plugin) Detect(ws *model.WorkspaceFacts) bool {
	if ws.ImportHits["org.apache.ibatis"] > 0 || ws.ImportHits["org.mybatis"] > 0 ||
		ws.ImportHits["com.baomidou.mybatisplus"] > 0 {
		return true
	}
	if ws.AnnotationHits["Mapper"] > 0 || ws.AnnotationHits["Select"] > 0 {
		return true
	}
	for _, cf := range ws.ConfigFiles {
		data, err := os.ReadFile(cf)
		if err != nil {
			continue
		}
		if len(ExtractMapperLocations(string(data))) > 0 {
			return true
		}
	}
	// Only probe resource-tree XMLs (skip pom/web.xml/etc.); stop on first hit.
	const maxProbe = 80
	probed := 0
	for _, x := range ws.XMLFiles {
		if !isLikelyMapperXML(x, ws) {
			continue
		}
		probed++
		if probed > maxProbe {
			break
		}
		data, err := os.ReadFile(x)
		if err != nil {
			continue
		}
		s := string(data)
		if strings.Contains(s, "<mapper") && strings.Contains(s, "namespace") {
			return true
		}
	}
	return false
}

func isLikelyMapperXML(path string, ws *model.WorkspaceFacts) bool {
	slash := strings.ToLower(filepath.ToSlash(path))
	if strings.Contains(slash, "/resources/") || strings.Contains(slash, "/mapper/") {
		return true
	}
	for _, root := range ws.ResourceRoots {
		r := strings.ToLower(filepath.ToSlash(root))
		if r != "" && strings.HasPrefix(slash, r) {
			return true
		}
	}
	return false
}

func (p *Plugin) Index(_ context.Context, core plugin.Core, ws *model.WorkspaceFacts) error {
	paths := discover(ws)
	if len(paths) == 0 {
		p.rebuildServiceMappers(core)
		return nil
	}
	workers := 4
	if n := len(paths); n < workers {
		workers = n
	}
	jobs := make(chan string, len(paths))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var parsed []*Mapper
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				data, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				if m := ParseMapper(path, string(data)); m != nil {
					mu.Lock()
					parsed = append(parsed, m)
					mu.Unlock()
				}
			}
		}()
	}
	for _, path := range paths {
		jobs <- path
	}
	close(jobs)
	wg.Wait()
	for _, m := range parsed {
		p.add(m)
	}
	p.rebuildServiceMappers(core)
	return nil
}

func (p *Plugin) rebuildServiceMappers(core plugin.Core) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.serviceMappers = make(map[string]string)
	if core == nil {
		return
	}
	for _, sym := range core.Symbols() {
		if sym.Kind != "type" || sym.InterfaceType {
			continue
		}
		if mk := mapperFromExtends(sym); mk != "" {
			p.serviceMappers[sym.Key] = mk
		}
	}
}

// mapperFromExtends reads ServiceImpl<M, T> — M is the first extends type arg.
// See com.baomidou.mybatisplus.extension.service.impl.ServiceImpl.
func mapperFromExtends(sym *model.Symbol) string {
	if sym == nil {
		return ""
	}
	if isServiceImplKey(sym.ExtendsKey) && len(sym.ExtendsArgs) > 0 {
		return "T|" + sym.ExtendsArgs[0]
	}
	return ""
}

func isServiceImplKey(key string) bool {
	name := strings.TrimPrefix(key, "T|")
	return name == "ServiceImpl" || strings.HasSuffix(name, ".ServiceImpl")
}

func discover(ws *model.WorkspaceFacts) []string {
	var patterns []string
	for _, cf := range ws.ConfigFiles {
		data, err := os.ReadFile(cf)
		if err != nil {
			continue
		}
		patterns = append(patterns, ExtractMapperLocations(string(data))...)
	}
	if len(patterns) == 0 {
		return ws.XMLFiles
	}
	seen := map[string]bool{}
	var out []string
	for _, pat := range patterns {
		rel := StripClasspathPrefix(pat)
		if rel == "" {
			continue
		}
		for _, root := range ws.ResourceRoots {
			for _, m := range ResolvePattern(root, rel) {
				k := strings.ToLower(m)
				if !seen[k] {
					seen[k] = true
					out = append(out, m)
				}
			}
		}
	}
	return out
}

func (p *Plugin) add(m *Mapper) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := strings.ToLower(m.Path)
	if old, ok := p.byPath[key]; ok {
		p.detachLocked(old)
	}
	p.byPath[key] = m
	p.byNamespace[m.Namespace] = append(p.byNamespace[m.Namespace], m)
}

func (p *Plugin) detachLocked(m *Mapper) {
	list := p.byNamespace[m.Namespace]
	for i, c := range list {
		if c == m {
			p.byNamespace[m.Namespace] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(p.byNamespace[m.Namespace]) == 0 {
		delete(p.byNamespace, m.Namespace)
	}
}

func (p *Plugin) OnFileChanged(ev plugin.ChangeEvent) {
	lower := strings.ToLower(ev.Path)
	if strings.HasSuffix(lower, ".xml") {
		if m := ParseMapper(ev.Path, ev.Text); m != nil {
			p.add(m)
			return
		}
		p.mu.Lock()
		key := strings.ToLower(filepath.ToSlash(ev.Path))
		if old, ok := p.byPath[key]; ok {
			p.detachLocked(old)
			delete(p.byPath, key)
		}
		p.mu.Unlock()
		return
	}
	// Java change: refresh ServiceImpl → Mapper bindings for types in this file.
	if ev.New == nil && ev.Old == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if ev.Old != nil {
		for _, sym := range ev.Old.Symbols {
			if sym.Kind == "type" {
				delete(p.serviceMappers, sym.Key)
			}
		}
	}
	if ev.New != nil {
		for _, sym := range ev.New.Symbols {
			if sym.Kind != "type" || sym.InterfaceType {
				continue
			}
			if mk := mapperFromExtends(sym); mk != "" {
				p.serviceMappers[sym.Key] = mk
			}
		}
	}
}

func (p *Plugin) Contribute(core plugin.Core, req model.NavRequest, primary *model.Symbol, fallback string) []*model.Location {
	lower := strings.ToLower(req.Path)
	if strings.HasSuffix(lower, ".xml") {
		return p.fromXML(core, req)
	}
	if req.Kind != model.NavDef && req.Kind != model.NavRoute {
		return nil
	}

	// MyBatis-Plus: baseMapper / getBaseMapper → concrete Mapper type.
	if locs := p.contributeBaseMapper(core, req, primary, fallback); len(locs) > 0 {
		return locs
	}

	if primary == nil || (primary.Kind != "method" && primary.Kind != "constructor") {
		return nil
	}
	// Annotation mappers (@Select/@Insert/…): SQL lives on the Java method.
	// Only ROUTE (Go to MyBatis SQL) — leave DEF to core (impls / declaration).
	if HasSQLAnnotation(primary.Annotations) {
		if req.Kind == model.NavRoute && primary.Declaration != nil {
			loc := *primary.Declaration
			loc.Kind = "mybatis-sql"
			loc.Label = SQLAnnotationLabel(primary.Annotations)
			return []*model.Location{&loc}
		}
		return nil
	}
	owner := strings.TrimPrefix(primary.OwnerKey, "T|")
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []*model.Location
	for _, m := range p.byNamespace[owner] {
		for _, s := range m.Statements {
			if s.ID == primary.Name && s.Location != nil {
				out = append(out, s.Location)
			}
		}
	}
	return out
}

func (p *Plugin) contributeBaseMapper(core plugin.Core, req model.NavRequest, primary *model.Symbol, fallback string) []*model.Location {
	fd := core.File(store.PathKey(req.Path))
	if fd == nil {
		return nil
	}
	offset := java.OffsetAt(req.Text, req.Line, req.Character)
	token := java.TokenAt(req.Text, offset)
	occ := java.OccAt(fd, offset, token)

	name := token
	if occ != nil && occ.Name != "" {
		name = occ.Name
	}
	if primary != nil && (primary.Name == "baseMapper" || primary.Name == "getBaseMapper") {
		name = primary.Name
	}
	if name == "" {
		name = fallback
	}

	ownerKey := ""
	if occ != nil {
		ownerKey = occ.OwnerTypeKey
	}
	if ownerKey == "" && primary != nil {
		ownerKey = primary.OwnerKey
	}

	// baseMapper.customMethod(...) — resolve on the concrete Mapper only.
	// Framework BaseMapper methods are absent from the workspace index → no jump.
	if occ != nil && occ.MemberRef && occ.QualifierName == "baseMapper" {
		mk := p.mapperOf(core, ownerKey)
		if mk == "" {
			return nil
		}
		if m := core.FindMethodInHierarchy(mk, occ.Name); m != nil && m.Declaration != nil {
			return []*model.Location{m.Declaration}
		}
		if f := core.FindField(mk, occ.Name); f != nil && f.Declaration != nil {
			return []*model.Location{f.Declaration}
		}
		for _, prop := range java.AccessorProps(occ.Name) {
			if f := core.FindField(mk, prop); f != nil && f.Declaration != nil {
				return []*model.Location{f.Declaration}
			}
		}
		return nil
	}

	if name != "baseMapper" && name != "getBaseMapper" {
		return nil
	}
	mk := p.mapperOf(core, ownerKey)
	if mk == "" {
		return nil
	}
	if s := core.Symbol(mk); s != nil && s.Declaration != nil {
		return []*model.Location{s.Declaration}
	}
	return nil
}

func (p *Plugin) mapperOf(core plugin.Core, typeKey string) string {
	if typeKey == "" {
		return ""
	}
	p.mu.RLock()
	if mk, ok := p.serviceMappers[typeKey]; ok {
		p.mu.RUnlock()
		return mk
	}
	p.mu.RUnlock()

	// Walk hierarchy for types indexed after plugin Index (or not yet cached).
	seen := map[string]bool{}
	q := []string{typeKey}
	for len(q) > 0 {
		cur := q[0]
		q = q[1:]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		p.mu.RLock()
		if mk, ok := p.serviceMappers[cur]; ok {
			p.mu.RUnlock()
			return mk
		}
		p.mu.RUnlock()
		sym := core.Symbol(cur)
		if sym == nil {
			continue
		}
		if mk := mapperFromExtends(sym); mk != "" {
			p.mu.Lock()
			p.serviceMappers[cur] = mk
			p.mu.Unlock()
			return mk
		}
		q = append(q, sym.SuperKeys...)
	}
	return ""
}

func (p *Plugin) fromXML(core plugin.Core, req model.NavRequest) []*model.Location {
	key := store.PathKey(req.Path)
	p.mu.RLock()
	m := p.byPath[key]
	p.mu.RUnlock()
	if m == nil {
		m = ParseMapper(req.Path, req.Text)
		if m != nil {
			p.add(m)
		}
	}
	if m == nil {
		return nil
	}
	offset := java.OffsetAt(req.Text, req.Line, req.Character)
	for _, tt := range m.TypeTokens {
		if offset >= tt.Start && offset <= tt.End {
			if sym := resolveType(core, tt.Value); sym != nil && sym.Declaration != nil {
				return []*model.Location{sym.Declaration}
			}
		}
	}
	for _, stmt := range m.Statements {
		if offset >= stmt.TagStart && offset <= stmt.TagEnd {
			return methodsFor(core, m.Namespace, stmt.ID)
		}
	}
	if offset >= m.NsStart && offset <= m.NsEnd {
		if sym := resolveType(core, m.Namespace); sym != nil && sym.Declaration != nil {
			return []*model.Location{sym.Declaration}
		}
	}
	return nil
}

func resolveType(core plugin.Core, name string) *model.Symbol {
	cleaned := strings.TrimSpace(name)
	if i := strings.Index(cleaned, "<"); i >= 0 {
		cleaned = cleaned[:i]
	}
	key := "T|" + cleaned
	if s := core.Symbol(key); s != nil {
		return s
	}
	simple := cleaned
	if i := strings.LastIndexAny(simple, ".$"); i >= 0 {
		simple = simple[i+1:]
	}
	var hit *model.Symbol
	for _, s := range core.Symbols() {
		if s.Kind == "type" && (s.Name == cleaned || strings.HasSuffix(s.Name, "."+simple)) {
			if hit != nil {
				return nil
			}
			hit = s
		}
	}
	return hit
}

func methodsFor(core plugin.Core, namespace, id string) []*model.Location {
	typeKey := "T|" + namespace
	var out []*model.Location
	for _, m := range core.Methods(typeKey, id) {
		if m.Declaration != nil {
			out = append(out, m.Declaration)
		}
	}
	return out
}
