package feign

import (
	"context"
	"defhop/internal/java"
	"defhop/internal/model"
	"defhop/internal/plugin"
	"strconv"
	"strings"
	"sync"
)

// Plugin indexes Spring MVC / OpenFeign HTTP mappings for bidirectional nav:
// Feign method ↔ Controller method (same path + compatible HTTP method).
type Plugin struct {
	mu       sync.RWMutex
	byPath   map[string][]*endpoint // normalized path -> endpoints
	bySymbol map[string]*endpoint   // method symbol key -> endpoint
}

type endpoint struct {
	Kind       string // feign | controller
	SymbolKey  string
	HttpMethod string
	Path       string
	Loc        *model.Location
	Service    string
}

func New() *Plugin {
	return &Plugin{
		byPath:   make(map[string][]*endpoint),
		bySymbol: make(map[string]*endpoint),
	}
}

func (p *Plugin) ID() string { return "feign" }

func (p *Plugin) Detect(ws *model.WorkspaceFacts) bool {
	if ws.ImportHits["org.springframework.cloud.openfeign"] > 0 {
		return true
	}
	if ws.AnnotationHits["FeignClient"] > 0 || ws.AnnotationHits["EnableFeignClients"] > 0 {
		return true
	}
	for _, f := range ws.JavaFiles {
		lower := strings.ToLower(f)
		if strings.Contains(lower, "/provider/") || strings.Contains(lower, "feign") {
			return true
		}
	}
	return false
}

func (p *Plugin) Index(_ context.Context, core plugin.Core, _ *model.WorkspaceFacts) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.byPath = make(map[string][]*endpoint)
	p.bySymbol = make(map[string]*endpoint)

	types := map[string]*model.Symbol{}
	for _, sym := range core.Symbols() {
		if sym != nil && sym.Kind == "type" {
			types[sym.Key] = sym
		}
	}
	for _, sym := range core.Symbols() {
		p.indexMethodLocked(sym, types)
	}
	return nil
}

func (p *Plugin) OnFileChanged(ev plugin.ChangeEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ev.Old != nil {
		for _, sym := range ev.Old.Symbols {
			p.removeMethodLocked(sym)
		}
	}
	if ev.New != nil {
		types := map[string]*model.Symbol{}
		for _, sym := range ev.New.Symbols {
			if sym != nil && sym.Kind == "type" {
				types[sym.Key] = sym
			}
		}
		for _, sym := range ev.New.Symbols {
			p.indexMethodLocked(sym, types)
		}
	}
}

func (p *Plugin) indexMethodLocked(sym *model.Symbol, types map[string]*model.Symbol) {
	if sym == nil || sym.Kind != "method" || sym.HttpPath == "" || sym.Declaration == nil {
		return
	}
	owner := types[sym.OwnerKey]
	if owner == nil {
		return
	}
	kind := ""
	svc := ""
	if owner.FeignService != "" || hasAnn(owner.Annotations, "FeignClient") {
		kind = "feign"
		svc = owner.FeignService
	} else if isController(owner) {
		kind = "controller"
	} else {
		return
	}
	p.removeMethodLocked(sym)
	ep := &endpoint{
		Kind:       kind,
		SymbolKey:  sym.Key,
		HttpMethod: strings.ToUpper(sym.HttpMethod),
		Path:       java.NormHttpPath(sym.HttpPath),
		Loc:        sym.Declaration,
		Service:    svc,
	}
	p.bySymbol[sym.Key] = ep
	p.byPath[ep.Path] = append(p.byPath[ep.Path], ep)
}

func (p *Plugin) removeMethodLocked(sym *model.Symbol) {
	if sym == nil {
		return
	}
	old := p.bySymbol[sym.Key]
	if old == nil {
		return
	}
	delete(p.bySymbol, sym.Key)
	list := p.byPath[old.Path]
	out := list[:0]
	for _, e := range list {
		if e != nil && e.SymbolKey != sym.Key {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		delete(p.byPath, old.Path)
	} else {
		p.byPath[old.Path] = out
	}
}

func (p *Plugin) Contribute(core plugin.Core, req model.NavRequest, primary *model.Symbol, _ string) []*model.Location {
	if primary == nil || primary.Kind != "method" {
		return nil
	}
	p.mu.RLock()
	ep := p.bySymbol[primary.Key]
	p.mu.RUnlock()
	if ep == nil {
		ep = p.classifyFallback(core, primary)
		if ep == nil {
			return nil
		}
	}

	switch req.Kind {
	case model.NavDef:
		// Feign method → Controller. Controller DEF stays with core (declaration).
		if ep.Kind == "feign" {
			return tagLocs(p.findOpposite(ep, "controller"), "feign-controller", "Controller")
		}
	case model.NavRoute:
		if ep.Kind == "feign" {
			return tagLocs(p.findOpposite(ep, "controller"), "feign-controller", "Controller")
		}
		if ep.Kind == "controller" {
			var out []*model.Location
			out = append(out, tagLocs(p.findPeers(ep, "controller"), "http-peer", "Controller")...)
			out = append(out, tagLocs(p.findOpposite(ep, "feign"), "feign-client", "Feign")...)
			return out
		}
	case model.NavRefs, model.NavImpl:
		// Controller → Feign providers + other Controllers sharing the path.
		if ep.Kind == "controller" {
			var out []*model.Location
			out = append(out, tagLocs(p.findOpposite(ep, "feign"), "feign-client", "Feign")...)
			out = append(out, tagLocs(p.findPeers(ep, "controller"), "http-peer", "Controller")...)
			return out
		}
		if ep.Kind == "feign" {
			return tagLocs(p.findOpposite(ep, "controller"), "feign-controller", "Controller")
		}
	}
	return nil
}

func tagLocs(locs []*model.Location, kind, label string) []*model.Location {
	out := make([]*model.Location, 0, len(locs))
	for _, l := range locs {
		if l == nil {
			continue
		}
		c := *l
		c.Kind = kind
		if c.Label == "" {
			c.Label = label
		}
		out = append(out, &c)
	}
	return out
}

// IsFeignMethod reports whether the symbol is a method on @FeignClient.
func IsFeignMethod(core plugin.Core, sym *model.Symbol) bool {
	if sym == nil || sym.Kind != "method" || sym.OwnerKey == "" {
		return false
	}
	owner := core.Symbol(sym.OwnerKey)
	if owner == nil {
		return false
	}
	return owner.FeignService != "" || hasAnn(owner.Annotations, "FeignClient")
}

func (p *Plugin) classifyFallback(core plugin.Core, primary *model.Symbol) *endpoint {
	if primary.HttpPath == "" {
		return nil
	}
	owner := core.Symbol(primary.OwnerKey)
	if owner == nil {
		return nil
	}
	kind := ""
	svc := ""
	if owner.FeignService != "" || hasAnn(owner.Annotations, "FeignClient") {
		kind = "feign"
		svc = owner.FeignService
	} else if isController(owner) {
		kind = "controller"
	} else {
		return nil
	}
	return &endpoint{
		Kind:       kind,
		SymbolKey:  primary.Key,
		HttpMethod: strings.ToUpper(primary.HttpMethod),
		Path:       java.NormHttpPath(primary.HttpPath),
		Loc:        primary.Declaration,
		Service:    svc,
	}
}

func (p *Plugin) findOpposite(ep *endpoint, toKind string) []*model.Location {
	return p.findMatching(ep, toKind, false)
}

func (p *Plugin) findPeers(ep *endpoint, kind string) []*model.Location {
	return p.findMatching(ep, kind, true)
}

func (p *Plugin) findMatching(ep *endpoint, toKind string, excludeSelf bool) []*model.Location {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []*model.Location
	seen := map[string]bool{}
	add := func(other *endpoint) {
		if other == nil || other.Kind != toKind || other.Loc == nil {
			return
		}
		if excludeSelf && other.SymbolKey == ep.SymbolKey {
			return
		}
		if !methodCompatible(ep.HttpMethod, other.HttpMethod) {
			return
		}
		id := other.Loc.Path + ":" + strconv.Itoa(other.Loc.Line) + ":" + strconv.Itoa(other.Loc.Character)
		if seen[id] {
			return
		}
		seen[id] = true
		out = append(out, other.Loc)
	}
	for _, other := range p.byPath[ep.Path] {
		add(other)
	}
	for path, list := range p.byPath {
		if path == ep.Path {
			continue
		}
		if !java.PathPatternEqual(ep.Path, path) {
			continue
		}
		for _, other := range list {
			add(other)
		}
	}
	return out
}

func methodCompatible(a, b string) bool {
	a, b = strings.ToUpper(a), strings.ToUpper(b)
	if a == "" || b == "" {
		return true
	}
	return a == b
}

func isController(s *model.Symbol) bool {
	for _, a := range s.Annotations {
		if a == "RestController" || a == "Controller" {
			return true
		}
	}
	return false
}

func hasAnn(anns []string, name string) bool {
	for _, a := range anns {
		if a == name {
			return true
		}
	}
	return false
}
