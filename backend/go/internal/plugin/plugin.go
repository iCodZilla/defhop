package plugin

import (
	"context"
	"defhop/internal/model"
	"sync"
)

// Core is the read API plugins use against the Java index.
type Core interface {
	Symbol(key string) *model.Symbol
	Symbols() map[string]*model.Symbol
	Methods(ownerKey, name string) []*model.Symbol
	Subtypes(typeKey string) []string
	File(pathKey string) *model.FileSlice
	ResolveTypeKey(key string, fd *model.FileSlice) string
	FindField(typeKey, name string) *model.Symbol
	FindMethodInHierarchy(typeKey, name string) *model.Symbol
}

// ChangeEvent notifies plugins that a file was replaced.
type ChangeEvent struct {
	PathKey string
	Path    string // original path
	Text    string // latest buffer (java or xml)
	Old     *model.FileSlice // java only; may be nil
	New     *model.FileSlice // java only; may be nil (deleted)
}

// Plugin is a lazily-detectable navigation contributor.
type Plugin interface {
	ID() string
	Detect(ws *model.WorkspaceFacts) bool
	Index(ctx context.Context, core Core, ws *model.WorkspaceFacts) error
	OnFileChanged(ev ChangeEvent)
	Contribute(core Core, req model.NavRequest, primary *model.Symbol, fallback string) []*model.Location
}

// Registry holds registered plugins and which ones are active.
type Registry struct {
	mu      sync.RWMutex
	all     []Plugin
	active  map[string]Plugin
	order   []string
}

func NewRegistry(plugins ...Plugin) *Registry {
	r := &Registry{active: make(map[string]Plugin)}
	r.all = append(r.all, plugins...)
	return r
}

// DetectAndActivate runs Detect on every plugin; only hits become active.
func (r *Registry) DetectAndActivate(ws *model.WorkspaceFacts) []Plugin {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active = make(map[string]Plugin)
	r.order = nil
	var hit []Plugin
	for _, p := range r.all {
		if p.Detect(ws) {
			r.active[p.ID()] = p
			r.order = append(r.order, p.ID())
			hit = append(hit, p)
		}
	}
	return hit
}

// Active returns currently loaded plugins in registration order.
func (r *Registry) Active() []Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Plugin, 0, len(r.order))
	for _, id := range r.order {
		if p := r.active[id]; p != nil {
			out = append(out, p)
		}
	}
	return out
}

// IDs lists active plugin ids.
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}
