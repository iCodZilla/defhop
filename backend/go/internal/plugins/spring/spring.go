package spring

import (
	"context"
	"defhop/internal/java"
	"defhop/internal/model"
	"defhop/internal/plugin"
	"defhop/internal/store"
	"os"
	"strconv"
	"strings"
	"sync"
)

var componentAnns = map[string]bool{
	"Component": true, "Service": true, "Repository": true,
	"Controller": true, "RestController": true, "Configuration": true,
	"Bean": true, "ConfigurationProperties": true,
}

var injectAnns = map[string]bool{
	"Autowired": true, "Resource": true, "Inject": true,
}

// Plugin indexes Spring beans and @Value / @ConfigurationProperties ↔ config files.
type Plugin struct {
	mu       sync.RWMutex
	beans    map[string][]string          // interface/type key -> implementing type keys
	cfgLocs  map[string][]*model.Location // property key -> config-file locations
	javaLocs map[string][]*model.Location // property key -> java @Value / field locations
	cfgFiles map[string]string            // pathKey -> last indexed text (for UPDATE)
}

func New() *Plugin {
	return &Plugin{
		beans:    make(map[string][]string),
		cfgLocs:  make(map[string][]*model.Location),
		javaLocs: make(map[string][]*model.Location),
		cfgFiles: make(map[string]string),
	}
}

func (p *Plugin) ID() string { return "spring" }

func (p *Plugin) Detect(ws *model.WorkspaceFacts) bool {
	if ws.ImportHits["org.springframework"] > 0 {
		return true
	}
	for _, a := range []string{"SpringBootApplication", "Component", "Service",
		"Repository", "Controller", "RestController", "Autowired", "Bean",
		"Value", "ConfigurationProperties"} {
		if ws.AnnotationHits[a] > 0 {
			return true
		}
	}
	return len(ws.ConfigFiles) > 0 && ws.ImportHits["org.springframework"] > 0
}

func (p *Plugin) Index(_ context.Context, core plugin.Core, ws *model.WorkspaceFacts) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.beans = make(map[string][]string)
	p.cfgLocs = make(map[string][]*model.Location)
	p.javaLocs = make(map[string][]*model.Location)
	p.cfgFiles = make(map[string]string)

	for _, sym := range core.Symbols() {
		p.registerTypeLocked(sym)
		p.indexSymbolKeysLocked(sym)
	}
	// Collect config-key occs from all java files via symbols' owning files — walk Symbols is enough
	// for ConfigKeys; also scan File slices for config-key Occs.
	seen := map[string]bool{}
	for _, sym := range core.Symbols() {
		if sym == nil || sym.Declaration == nil {
			continue
		}
		pk := store.PathKey(sym.Declaration.Path)
		if seen[pk] {
			continue
		}
		seen[pk] = true
		if fd := core.File(pk); fd != nil {
			p.indexJavaOccsLocked(fd)
		}
	}

	for _, cf := range ws.ConfigFiles {
		data, err := os.ReadFile(cf)
		if err != nil {
			continue
		}
		text := string(data)
		p.cfgFiles[store.PathKey(cf)] = text
		for _, hit := range ParseConfigFile(cf, text) {
			p.addCfgLocked(hit.Key, hit.Loc)
		}
	}
	return nil
}

func (p *Plugin) OnFileChanged(ev plugin.ChangeEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ev.Old != nil {
		for _, sym := range ev.Old.Symbols {
			p.unregisterTypeLocked(sym)
			p.removeJavaKeysLocked(sym)
		}
		p.removeJavaOccsLocked(ev.Old)
	}
	if ev.New != nil {
		for _, sym := range ev.New.Symbols {
			p.registerTypeLocked(sym)
			p.indexSymbolKeysLocked(sym)
		}
		p.indexJavaOccsLocked(ev.New)
	}
	if IsConfigPath(ev.Path) {
		p.reindexConfigLocked(ev.Path, ev.Text)
	}
}

func (p *Plugin) reindexConfigLocked(path, text string) {
	pk := store.PathKey(path)
	// Drop previous locs for this file.
	for k, locs := range p.cfgLocs {
		out := locs[:0]
		for _, l := range locs {
			if l != nil && store.PathKey(l.Path) != pk {
				out = append(out, l)
			}
		}
		if len(out) == 0 {
			delete(p.cfgLocs, k)
		} else {
			p.cfgLocs[k] = out
		}
	}
	p.cfgFiles[pk] = text
	for _, hit := range ParseConfigFile(path, text) {
		p.addCfgLocked(hit.Key, hit.Loc)
	}
}

func (p *Plugin) registerTypeLocked(sym *model.Symbol) {
	if sym == nil || sym.Kind != "type" || sym.InterfaceType {
		return
	}
	if !isComponent(sym) {
		return
	}
	p.beans[sym.Key] = appendUnique(p.beans[sym.Key], sym.Key)
	for _, sk := range sym.SuperKeys {
		p.beans[sk] = appendUnique(p.beans[sk], sym.Key)
	}
}

func (p *Plugin) unregisterTypeLocked(sym *model.Symbol) {
	if sym == nil || sym.Kind != "type" || sym.InterfaceType {
		return
	}
	removeBean := func(key, impl string) {
		list := p.beans[key]
		out := list[:0]
		for _, x := range list {
			if x != impl {
				out = append(out, x)
			}
		}
		if len(out) == 0 {
			delete(p.beans, key)
		} else {
			p.beans[key] = out
		}
	}
	removeBean(sym.Key, sym.Key)
	for _, sk := range sym.SuperKeys {
		removeBean(sk, sym.Key)
	}
}

func (p *Plugin) indexSymbolKeysLocked(sym *model.Symbol) {
	if sym == nil || len(sym.ConfigKeys) == 0 || sym.Declaration == nil {
		return
	}
	for _, k := range sym.ConfigKeys {
		p.addJavaLocked(k, sym.Declaration)
	}
}

func (p *Plugin) removeJavaKeysLocked(sym *model.Symbol) {
	if sym == nil || len(sym.ConfigKeys) == 0 || sym.Declaration == nil {
		return
	}
	for _, k := range sym.ConfigKeys {
		p.removeJavaLocLocked(k, sym.Declaration)
	}
}

func (p *Plugin) indexJavaOccsLocked(fd *model.FileSlice) {
	if fd == nil {
		return
	}
	for _, occ := range fd.Occs {
		if occ != nil && occ.Role == "config-key" && occ.Location != nil && occ.PropertyName != "" {
			p.addJavaLocked(occ.PropertyName, occ.Location)
		}
	}
}

func (p *Plugin) removeJavaOccsLocked(fd *model.FileSlice) {
	if fd == nil {
		return
	}
	for _, occ := range fd.Occs {
		if occ != nil && occ.Role == "config-key" && occ.Location != nil && occ.PropertyName != "" {
			p.removeJavaLocLocked(occ.PropertyName, occ.Location)
		}
	}
}

func (p *Plugin) addCfgLocked(key string, loc *model.Location) {
	if key == "" || loc == nil {
		return
	}
	for _, v := range KeyVariants(key) {
		p.cfgLocs[v] = appendUniqueLoc(p.cfgLocs[v], loc)
	}
}

func (p *Plugin) addJavaLocked(key string, loc *model.Location) {
	if key == "" || loc == nil {
		return
	}
	for _, v := range KeyVariants(key) {
		p.javaLocs[v] = appendUniqueLoc(p.javaLocs[v], loc)
	}
}

func (p *Plugin) removeJavaLocLocked(key string, loc *model.Location) {
	if key == "" || loc == nil {
		return
	}
	for _, v := range KeyVariants(key) {
		list := p.javaLocs[v]
		out := list[:0]
		for _, x := range list {
			if !sameLoc(x, loc) {
				out = append(out, x)
			}
		}
		if len(out) == 0 {
			delete(p.javaLocs, v)
		} else {
			p.javaLocs[v] = out
		}
	}
}

func (p *Plugin) Contribute(core plugin.Core, req model.NavRequest, primary *model.Symbol, _ string) []*model.Location {
	// Config file → Java
	if IsConfigPath(req.Path) {
		return p.navFromConfig(req)
	}

	// @Value placeholder / config-key occ → config file
	if locs := p.navConfigKeyOcc(core, req); len(locs) > 0 {
		return locs
	}

	// Live fallback: cursor inside "${...}" even when FileSlice cache lacks config-key occs.
	if req.Text != "" && (req.Kind == model.NavDef || req.Kind == model.NavRefs || req.Kind == model.NavRoute) {
		off := java.OffsetAt(req.Text, req.Line, req.Character)
		if key := java.ValueKeyAt(req.Text, off); key != "" {
			if locs := p.lookupCfg(key); len(locs) > 0 {
				return locs
			}
		}
	}

	// Field with ConfigKeys (@Value / @ConfigurationProperties) → config
	keys := fieldConfigKeys(primary, req)
	if len(keys) > 0 &&
		(req.Kind == model.NavDef || req.Kind == model.NavRefs || req.Kind == model.NavRoute) {
		var out []*model.Location
		for _, k := range keys {
			out = append(out, p.lookupCfg(k)...)
		}
		if len(out) > 0 && (req.Kind == model.NavDef || req.Kind == model.NavRoute) {
			return out
		}
		if req.Kind == model.NavRefs {
			out = append(out, p.lookupJava(keys[0])...)
			return out
		}
	}

	if primary == nil {
		return nil
	}
	switch req.Kind {
	case model.NavDef, model.NavImpl:
		if primary.Kind == "field" && isInjected(primary) && primary.TypeKey != "" {
			return p.impls(core, primary.TypeKey)
		}
		if primary.Kind == "type" && primary.InterfaceType {
			return p.impls(core, primary.Key)
		}
		if (primary.Kind == "method" || primary.Kind == "constructor") && primary.OwnerKey != "" {
			if owner := core.Symbol(primary.OwnerKey); owner != nil && owner.InterfaceType {
				// OpenFeign clients are handled by the feign plugin (→ Controller),
				// not by bean-impl resolution (which would land on Fallback classes).
				if hasAnnName(owner.Annotations, "FeignClient") {
					return nil
				}
				var out []*model.Location
				p.mu.RLock()
				implKeys := append([]string(nil), p.beans[primary.OwnerKey]...)
				p.mu.RUnlock()
				if len(implKeys) == 0 {
					for _, sub := range core.Subtypes(primary.OwnerKey) {
						if s := core.Symbol(sub); s != nil && !s.InterfaceType {
							implKeys = append(implKeys, sub)
						}
					}
				}
				for _, ik := range implKeys {
					for _, m := range core.Methods(ik, primary.Name) {
						if m.Declaration != nil {
							out = append(out, m.Declaration)
						}
					}
				}
				if len(out) > 0 {
					return out
				}
			}
		}
	}
	return nil
}

func (p *Plugin) navConfigKeyOcc(core plugin.Core, req model.NavRequest) []*model.Location {
	if req.Text == "" {
		return nil
	}
	offset := java.OffsetAt(req.Text, req.Line, req.Character)
	key := ""
	if fd := core.File(store.PathKey(req.Path)); fd != nil {
		token := java.TokenAt(req.Text, offset)
		if occ := java.OccAt(fd, offset, token); occ != nil && occ.Role == "config-key" && occ.PropertyName != "" {
			key = occ.PropertyName
		}
	}
	if key == "" {
		key = java.ValueKeyAt(req.Text, offset)
	}
	if key == "" {
		return nil
	}
	switch req.Kind {
	case model.NavDef, model.NavRoute:
		return p.lookupCfg(key)
	case model.NavRefs:
		var out []*model.Location
		out = append(out, p.lookupCfg(key)...)
		out = append(out, p.lookupJava(key)...)
		return out
	}
	return nil
}

func fieldConfigKeys(primary *model.Symbol, req model.NavRequest) []string {
	if primary == nil || primary.Kind != "field" {
		return nil
	}
	if len(primary.ConfigKeys) > 0 {
		return primary.ConfigKeys
	}
	hasValue := false
	for _, a := range primary.Annotations {
		if a == "Value" {
			hasValue = true
			break
		}
	}
	if !hasValue || req.Text == "" || primary.Declaration == nil {
		return nil
	}
	off := java.OffsetAt(req.Text, primary.Declaration.Line, primary.Declaration.Character)
	return java.ValueKeysBeforeField(req.Text, off)
}

func (p *Plugin) navFromConfig(req model.NavRequest) []*model.Location {
	if req.Kind != model.NavDef && req.Kind != model.NavRefs && req.Kind != model.NavRoute {
		return nil
	}
	text := req.Text
	if text == "" {
		p.mu.RLock()
		text = p.cfgFiles[store.PathKey(req.Path)]
		p.mu.RUnlock()
	}
	if text == "" {
		return nil
	}
	offset := java.OffsetAt(text, req.Line, req.Character)
	key := ResolveConfigKeyAt(req.Path, text, offset)
	if key == "" {
		return nil
	}
	switch req.Kind {
	case model.NavDef, model.NavRoute:
		return p.lookupJava(key)
	case model.NavRefs:
		var out []*model.Location
		out = append(out, p.lookupJava(key)...)
		out = append(out, p.lookupCfg(key)...)
		return out
	}
	return nil
}

func (p *Plugin) lookupCfg(key string) []*model.Location {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []*model.Location
	seen := map[string]bool{}
	for _, v := range KeyVariants(key) {
		for _, l := range p.cfgLocs[v] {
			if l == nil {
				continue
			}
			id := l.Path + ":" + strconv.Itoa(l.Line) + ":" + strconv.Itoa(l.Character)
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, l)
		}
	}
	return out
}

func (p *Plugin) lookupJava(key string) []*model.Location {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []*model.Location
	seen := map[string]bool{}
	for _, v := range KeyVariants(key) {
		for _, l := range p.javaLocs[v] {
			if l == nil {
				continue
			}
			id := l.Path + ":" + strconv.Itoa(l.Line) + ":" + strconv.Itoa(l.Character)
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, l)
		}
	}
	return out
}

func (p *Plugin) impls(core plugin.Core, typeKey string) []*model.Location {
	p.mu.RLock()
	keys := append([]string(nil), p.beans[typeKey]...)
	p.mu.RUnlock()
	if len(keys) == 0 {
		for _, sub := range core.Subtypes(typeKey) {
			if s := core.Symbol(sub); s != nil && !s.InterfaceType {
				keys = append(keys, sub)
			}
		}
	}
	var out []*model.Location
	for _, k := range keys {
		if s := core.Symbol(k); s != nil && s.Declaration != nil && !s.InterfaceType {
			out = append(out, s.Declaration)
		}
	}
	return out
}

func isComponent(s *model.Symbol) bool {
	for _, a := range s.Annotations {
		if componentAnns[a] {
			return true
		}
		if strings.HasSuffix(a, "Service") || strings.HasSuffix(a, "Component") {
			return true
		}
	}
	return false
}

func isInjected(s *model.Symbol) bool {
	for _, a := range s.Annotations {
		if injectAnns[a] {
			return true
		}
	}
	return false
}

func hasAnnName(anns []string, name string) bool {
	for _, a := range anns {
		if a == name {
			return true
		}
	}
	return false
}

func appendUnique(xs []string, v string) []string {
	for _, x := range xs {
		if x == v {
			return xs
		}
	}
	return append(xs, v)
}

func appendUniqueLoc(xs []*model.Location, v *model.Location) []*model.Location {
	for _, x := range xs {
		if sameLoc(x, v) {
			return xs
		}
	}
	return append(xs, v)
}

func sameLoc(a, b *model.Location) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Path == b.Path && a.Line == b.Line && a.Character == b.Character
}
