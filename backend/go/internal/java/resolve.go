package java

import (
	"defhop/internal/model"
	"defhop/internal/store"
	"strings"
)

// Resolve finds the symbol under the cursor in a file slice against a snapshot.
// source enables brace-scope checks for locals (may be empty if unavailable).
func Resolve(snap *store.Snapshot, fd *model.FileSlice, offset int, token, source string) (*model.Symbol, string) {
	if snap == nil || fd == nil {
		return nil, token
	}
	occ := OccAt(fd, offset, token)
	if occ == nil {
		if token == "" {
			return nil, ""
		}
		// Field/type annotation sites often have no Occ yet ? resolve simple type names.
		if token[0] >= 'A' && token[0] <= 'Z' {
			if q := snap.ResolveTypeKey("T|"+token, fd); snap.SymTab[q] != nil {
				return snap.SymTab[q], q
			}
		}
		var matches []*model.Symbol
		for _, sym := range snap.SymTab {
			if sym.Name == token && sym.Declaration != nil {
				matches = append(matches, sym)
			}
		}
		if len(matches) == 1 {
			return matches[0], matches[0].Key
		}
		return nil, token
	}

	call := source != "" && isCallSite(source, occ.End)
	// Local binding must not win over name(...) method calls (e.g. String curDate
	// shadowing curDate()). Cached indexes may still have TargetKey=L|? ? override.
	if IsLocalKey(occ.TargetKey) && !call {
		decl := occ.Location
		if occ.Role != "declaration" {
			if loc := LocalDeclLocation(fd, occ.TargetKey); loc != nil {
				decl = loc
			}
		}
		return &model.Symbol{
			Key: occ.TargetKey, Kind: "variable", Name: occ.Name,
			Declaration: decl,
		}, occ.TargetKey
	}
	if occ.TargetKey != "" && !IsLocalKey(occ.TargetKey) {
		if sym := snap.SymTab[occ.TargetKey]; sym != nil {
			return sym, occ.TargetKey
		}
	}

	if occ.Role == "config-key" {
		return nil, occ.PropertyName
	}

	if occ.Role == "property" && occ.PropertyName != "" && occ.QualifierKey != "" {
		q := snap.ResolveTypeKey(occ.QualifierKey, fd)
		if field := snap.FindField(q, occ.PropertyName); field != nil {
			return field, field.Key
		}
		if dot := strings.Index(occ.PropertyName, "."); dot > 0 {
			if base := snap.FindField(q, occ.PropertyName[:dot]); base != nil && base.TypeKey != "" {
				if field := snap.FindField(base.TypeKey, occ.PropertyName[dot+1:]); field != nil {
					return field, field.Key
				}
			}
		}
		return nil, occ.PropertyName
	}

	if occ.MemberRef && occ.QualifierKey == "" && occ.RecvCallName != "" {
		ownerKey := occ.RecvCallOwner
		if ownerKey != "" {
			for _, m := range snap.Methods(ownerKey, occ.RecvCallName) {
				if m.ReturnType != "" {
					rt := snap.ResolveTypeKey(m.ReturnType, fd)
					if res := resolveMemberOn(snap, rt, occ.Name); res != nil {
						return res, res.Key
					}
				}
			}
		}
	}

	if occ.QualifierKey != "" {
		q := snap.ResolveTypeKey(occ.QualifierKey, fd)
		if res := resolveMemberOn(snap, q, occ.Name); res != nil {
			return res, res.Key
		}
	}

	if occ.MemberRef && occ.QualifierKey == "" && occ.OwnerTypeKey != "" && occ.QualifierName != "" {
		if f := snap.FindField(occ.OwnerTypeKey, occ.QualifierName); f != nil && f.TypeKey != "" {
			if res := resolveMemberOn(snap, f.TypeKey, occ.Name); res != nil {
				return res, res.Key
			}
		}
		if q := snap.ResolveTypeKey("T|"+occ.QualifierName, fd); snap.SymTab[q] != nil {
			if res := resolveMemberOn(snap, q, occ.Name); res != nil {
				return res, res.Key
			}
		}
	}

	name := occ.Name
	if name == "" {
		name = token
	}
	if name == "" {
		return nil, name
	}
	if occ.MemberRef || occ.QualifierKey != "" {
		return nil, name
	}

	if name[0] >= 'A' && name[0] <= 'Z' {
		if q := snap.ResolveTypeKey("T|"+name, fd); snap.SymTab[q] != nil {
			return snap.SymTab[q], q
		}
	}

	// Unqualified call: prefer method on enclosing type (before same-named locals).
	if call {
		if occ.OwnerTypeKey != "" {
			if m := snap.FindMethodInHierarchy(occ.OwnerTypeKey, name); m != nil {
				return m, m.Key
			}
		}
		for _, sym := range fd.Symbols {
			if sym.Kind == "method" && sym.Name == name && sym.Declaration != nil {
				return sym, sym.Key
			}
		}
	}

	// Unbound name: pick the nearest in-scope local declaration (same rules as bind).
	if !call {
		if best := scopedLocalAt(fd, name, offset, source); best != nil {
			return &model.Symbol{Key: best.TargetKey, Kind: "variable", Name: name,
				Declaration: best.Location}, best.TargetKey
		}
	}

	for _, sym := range fd.Symbols {
		if sym.Kind == "field" && sym.Name == name && sym.Declaration != nil {
			return sym, sym.Key
		}
	}

	// import static Owner.CONST / Owner.Inner.CONST
	if f := resolveStaticImportMember(snap, fd, name); f != nil {
		return f, f.Key
	}

	var candidates []*model.Symbol
	for _, sym := range fd.Symbols {
		if (sym.Kind == "method" || sym.Kind == "constructor") && sym.Name == name {
			candidates = append(candidates, sym)
		}
	}
	if len(candidates) == 0 {
		for _, sym := range snap.SymTab {
			if (sym.Kind == "method" || sym.Kind == "constructor") && sym.Name == name {
				candidates = append(candidates, sym)
			}
		}
	}
	if len(candidates) == 1 && candidates[0].Declaration != nil {
		return candidates[0], candidates[0].Key
	}
	for _, c := range candidates {
		if c.Declaration != nil && strings.EqualFold(c.Declaration.Path, fd.Path) {
			return c, c.Key
		}
	}
	return nil, name
}

// resolveStaticImportMember resolves unqualified names brought in by
// `import static pkg.Type.MEMBER` or `import static pkg.Outer.Inner.MEMBER`.
func resolveStaticImportMember(snap *store.Snapshot, fd *model.FileSlice, name string) *model.Symbol {
	if snap == nil || fd == nil || name == "" {
		return nil
	}
	suffix := "." + name
	for _, imp := range fd.Imports {
		if strings.HasSuffix(imp, ".*") || !strings.HasSuffix(imp, suffix) {
			continue
		}
		owner := strings.TrimSuffix(imp, suffix)
		if owner == "" {
			continue
		}
		ownerKey := snap.ResolveTypeKey("T|"+owner, fd)
		if f := snap.FindField(ownerKey, name); f != nil {
			return f
		}
		// Static method imported the same way.
		if m := snap.FindMethodInHierarchy(ownerKey, name); m != nil {
			return m
		}
	}
	return nil
}

func resolveMemberOn(snap *store.Snapshot, typeKey, name string) *model.Symbol {
	// Prefer backing field for bean accessors (getCInquiryCode -> cInquiryCode),
	// including Lombok-generated getters that do not exist as source methods.
	for _, prop := range AccessorProps(name) {
		if f := snap.FindField(typeKey, prop); f != nil {
			return f
		}
	}
	if f := snap.FindField(typeKey, name); f != nil {
		return f
	}
	return snap.FindMethodInHierarchy(typeKey, name)
}

// AccessorProp maps getX/setX/isX to the primary property name.
func AccessorProp(name string) string {
	ps := AccessorProps(name)
	if len(ps) == 0 {
		return ""
	}
	return ps[0]
}

// AccessorProps returns candidate property names for a bean accessor.
// getCInquiryCode yields both "cInquiryCode" (Lombok field style) and
// "CInquiryCode" (JavaBeans two-leading-capitals rule).
func AccessorProps(name string) []string {
	raw := accessorRaw(name)
	if raw == "" {
		return nil
	}
	decap := strings.ToLower(raw[:1]) + raw[1:]
	out := []string{decap}
	if raw != decap {
		out = append(out, raw)
	}
	return out
}

func accessorRaw(name string) string {
	if len(name) < 3 {
		return ""
	}
	switch {
	case strings.HasPrefix(name, "get") && len(name) > 3 && name[3] >= 'A' && name[3] <= 'Z':
		return name[3:]
	case strings.HasPrefix(name, "set") && len(name) > 3 && name[3] >= 'A' && name[3] <= 'Z':
		return name[3:]
	case strings.HasPrefix(name, "is") && len(name) > 2 && name[2] >= 'A' && name[2] <= 'Z':
		return name[2:]
	default:
		return ""
	}
}

// OccAt finds the tightest occurrence covering offset.
func OccAt(fd *model.FileSlice, offset int, token string) *model.Occ {
	var best *model.Occ
	var nameMatch *model.Occ
	nameDist := 1 << 30
	for _, occ := range fd.Occs {
		if offset >= occ.Start && offset < occ.End {
			if best == nil || (occ.End-occ.Start) < (best.End-best.Start) ||
				((occ.End-occ.Start) == (best.End-best.Start) && occ.Role == "declaration") {
				best = occ
			}
		}
		if offset == occ.End {
			best = occ
		}
		if token != "" && occ.Name == token {
			d := occ.Start - offset
			if d < 0 {
				d = -d
			}
			if d < nameDist {
				nameDist = d
				nameMatch = occ
			}
		}
	}
	if best == nil {
		best = nameMatch
	}
	return best
}

// Implementations returns concrete subtypes / method overrides.
func Implementations(snap *store.Snapshot, sym *model.Symbol) []*model.Location {
	if snap == nil || sym == nil {
		return nil
	}
	var out []*model.Location
	if sym.Kind == "type" {
		for _, sub := range snap.Subtypes(sym.Key) {
			if impl := snap.SymTab[sub]; impl != nil && impl.Declaration != nil && !impl.InterfaceType {
				out = append(out, impl.Declaration)
			}
		}
		return out
	}
	if sym.Kind == "method" || sym.Kind == "constructor" {
		for _, key := range implementationMethodKeys(snap, sym) {
			if impl := snap.SymTab[key]; impl != nil && impl.Declaration != nil {
				out = append(out, impl.Declaration)
			}
		}
	}
	if sym.Kind == "field" && sym.TypeKey != "" {
		if iface := snap.SymTab[sym.TypeKey]; iface != nil && iface.InterfaceType {
			for _, sub := range snap.Subtypes(sym.TypeKey) {
				if impl := snap.SymTab[sub]; impl != nil && impl.Declaration != nil && !impl.InterfaceType {
					out = append(out, impl.Declaration)
				}
			}
		}
	}
	return out
}

func implementationMethodKeys(snap *store.Snapshot, sym *model.Symbol) []string {
	var res []string
	prefix := "M|" + sym.OwnerKey + "|" + sym.Name + "|"
	params := ""
	if strings.HasPrefix(sym.Key, prefix) {
		params = sym.Key[len(prefix):]
	}
	for _, sub := range snap.Subtypes(sym.OwnerKey) {
		implSym := snap.SymTab[sub]
		if implSym == nil || implSym.InterfaceType {
			continue
		}
		exact := "M|" + sub + "|" + sym.Name + "|" + params
		if _, ok := snap.SymTab[exact]; ok {
			res = append(res, exact)
		} else {
			for _, m := range snap.Methods(sub, sym.Name) {
				res = append(res, m.Key)
			}
		}
	}
	return res
}

// References uses the inverted index.
func References(snap *store.Snapshot, sym *model.Symbol, includeDecl bool) []*model.Location {
	if snap == nil || sym == nil {
		return nil
	}
	keys := map[string]bool{sym.Key: true}
	if sym.Kind == "method" || sym.Kind == "constructor" {
		for _, k := range implementationMethodKeys(snap, sym) {
			keys[k] = true
		}
	}
	var out []*model.Location
	for k := range keys {
		for _, ref := range snap.Refs[k] {
			if ref.Role == "declaration" && !includeDecl {
				continue
			}
			if ref.Loc != nil {
				out = append(out, ref.Loc)
			}
		}
	}
	return out
}
