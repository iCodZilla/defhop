package mapstruct

import (
	"context"
	"defhop/internal/model"
	"defhop/internal/plugin"
	"strings"
)

// Plugin prefers generated XxxImpl for @Mapper abstract/interface methods.
type Plugin struct{}

func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string { return "mapstruct" }

func (p *Plugin) Detect(ws *model.WorkspaceFacts) bool {
	if ws.ImportHits["org.mapstruct"] > 0 {
		return true
	}
	if ws.AnnotationHits["Mapping"] > 0 || ws.AnnotationHits["Mappings"] > 0 {
		return true
	}
	// Path hint from hub scan (package .../mapstruct/... or generated *Impl).
	for _, f := range ws.JavaFiles {
		lower := strings.ToLower(f)
		if strings.Contains(lower, "mapstruct") {
			return true
		}
	}
	return false
}

func (p *Plugin) Index(context.Context, plugin.Core, *model.WorkspaceFacts) error { return nil }

func (p *Plugin) OnFileChanged(plugin.ChangeEvent) {}

func (p *Plugin) Contribute(core plugin.Core, req model.NavRequest, primary *model.Symbol, _ string) []*model.Location {
	if primary == nil || (req.Kind != model.NavDef && req.Kind != model.NavImpl && req.Kind != model.NavRoute) {
		return nil
	}
	if primary.Kind != "method" && primary.Kind != "constructor" {
		return nil
	}
	owner := core.Symbol(primary.OwnerKey)
	if owner == nil || !isMapper(owner) {
		return nil
	}

	candidates := append([]string(nil), core.Subtypes(primary.OwnerKey)...)
	implKey := "T|" + owner.Name + "Impl"
	candidates = append(candidates, implKey)

	wantSig := methodParamsSig(primary)
	var exact, preferred, fallback []*model.Location
	seen := map[string]bool{}
	for _, sub := range candidates {
		if seen[sub] {
			continue
		}
		seen[sub] = true
		impl := core.Symbol(sub)
		if impl == nil || impl.InterfaceType {
			continue
		}
		isImplName := strings.HasSuffix(simpleName(impl.Name), "Impl")
		for _, m := range core.Methods(sub, primary.Name) {
			if m.Declaration == nil {
				continue
			}
			if wantSig != "" && methodParamsSig(m) == wantSig {
				exact = append(exact, m.Declaration)
				continue
			}
			if isImplName {
				preferred = append(preferred, m.Declaration)
			} else {
				fallback = append(fallback, m.Declaration)
			}
		}
		if isImplName && len(exact) == 0 && len(preferred) == 0 && impl.Declaration != nil {
			fallback = append(fallback, impl.Declaration)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	if len(preferred) > 0 {
		return preferred
	}
	return fallback
}

// methodParamsSig returns the parameter signature segment of a method key
// (M|owner|name|params → params).
func methodParamsSig(m *model.Symbol) string {
	if m == nil || m.Kind != "method" && m.Kind != "constructor" {
		return ""
	}
	prefix := "M|" + m.OwnerKey + "|" + m.Name + "|"
	if strings.HasPrefix(m.Key, prefix) {
		return m.Key[len(prefix):]
	}
	// OwnerKey is T|fqn; Key embeds it.
	parts := strings.Split(m.Key, "|")
	if len(parts) >= 5 && parts[0] == "M" {
		return strings.Join(parts[4:], "|")
	}
	return ""
}

func simpleName(fqn string) string {
	if i := strings.LastIndex(fqn, "."); i >= 0 {
		return fqn[i+1:]
	}
	return fqn
}

func isMapper(s *model.Symbol) bool {
	for _, a := range s.Annotations {
		if a == "Mapper" {
			return true
		}
	}
	return false
}
