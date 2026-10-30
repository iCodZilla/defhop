package lombok

import (
	"context"
	"defhop/internal/java"
	"defhop/internal/model"
	"defhop/internal/plugin"
)

// Plugin maps Lombok-style accessors (getX/setX/isX) onto fields.
// Also activates for other "bean accessor" libraries that follow the same naming.
type Plugin struct{}

func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string { return "lombok" }

func (p *Plugin) Detect(ws *model.WorkspaceFacts) bool {
	if ws.ImportHits["lombok."] > 0 {
		return true
	}
	for _, a := range []string{
		"Data", "Getter", "Setter", "Builder", "SuperBuilder", "Slf4j", "Value", "Accessors",
		"NoArgsConstructor", "AllArgsConstructor", "RequiredArgsConstructor",
	} {
		if ws.AnnotationHits[a] > 0 {
			return true
		}
	}
	return false
}

func (p *Plugin) Index(context.Context, plugin.Core, *model.WorkspaceFacts) error { return nil }

func (p *Plugin) OnFileChanged(plugin.ChangeEvent) {}

func (p *Plugin) Contribute(core plugin.Core, req model.NavRequest, primary *model.Symbol, fallback string) []*model.Location {
	if req.Kind != model.NavDef || primary == nil {
		return nil
	}
	// If resolve already landed on a method that looks like an accessor,
	// retarget to the backing field when present on the owner type.
	if primary.Kind != "method" || primary.OwnerKey == "" {
		return nil
	}
	for _, prop := range java.AccessorProps(primary.Name) {
		if f := core.FindField(primary.OwnerKey, prop); f != nil && f.Declaration != nil {
			return []*model.Location{f.Declaration}
		}
	}
	return nil
}
