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
		argc := -1
		var argTypes []string
		if call {
			argc = callArgCount(source, occ.End)
			argTypes = callArgTypes(source, occ.End, snap, fd)
		}
		if res := resolveMemberViaTypeKeyArity(snap, fd, occ.QualifierKey, occ.Name, argc, argTypes); res != nil {
			return res, res.Key
		}
	}

	if occ.MemberRef && occ.QualifierName != "" {
		// Instance field first: this.foo.bar / local.foo where QualifierName is the field.
		if occ.OwnerTypeKey != "" {
			if f := snap.FindField(occ.OwnerTypeKey, occ.QualifierName); f != nil && f.TypeKey != "" {
				if res := resolveMemberOn(snap, f.TypeKey, occ.Name); res != nil {
					return res, res.Key
				}
			}
		}
		// Type.member — Result.success / com.x.Result.success (qn is simple "Result").
		if res := resolveMemberViaTypeName(snap, fd, occ.QualifierName, occ.Name); res != nil {
			return res, res.Key
		}
	}

	// Multi-hop chains: a.b.c / a.getB().getC().run — walk the receiver expression.
	if occ.MemberRef && source != "" && occ.Start > 0 {
		dot := occ.Start - 1
		for dot >= 0 && isWS(source[dot]) {
			dot--
		}
		if dot >= 0 && (source[dot] == '.' || (dot >= 1 && source[dot] == ':' && source[dot-1] == ':')) {
			if source[dot] == ':' {
				dot-- // position at first colon of ::
			}
			argc := -1
			var argTypes []string
			if call {
				argc = callArgCount(source, occ.End)
				argTypes = callArgTypes(source, occ.End, snap, fd)
			}
			if rt := typeKeyOfReceiver(snap, fd, source, dot); rt != "" {
				if res := resolveMemberViaTypeKeyArity(snap, fd, rt, occ.Name, argc, argTypes); res != nil {
					return res, res.Key
				}
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
	// Overloads: match by argument count at the call site (parseDate(x,y) → 2-arg).
	if call {
		argc := callArgCount(source, occ.End)
		argTypes := callArgTypes(source, occ.End, snap, fd)
		if occ.OwnerTypeKey != "" {
			if m := pickMethodByArity(snap, occ.OwnerTypeKey, name, argc, argTypes); m != nil {
				return m, m.Key
			}
		}
		if m := pickMethodFromFile(fd, name, argc, argTypes); m != nil {
			return m, m.Key
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

// pickMethodByArity chooses an overload by arity, then simple argument types.
func pickMethodByArity(snap *store.Snapshot, ownerKey, name string, argc int, argTypes []string) *model.Symbol {
	if snap == nil || ownerKey == "" || name == "" {
		return nil
	}
	var matched, any []*model.Symbol
	seen := map[string]bool{}
	q := []string{ownerKey}
	for len(q) > 0 {
		cur := q[0]
		q = q[1:]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		for _, m := range snap.Methods(cur, name) {
			any = append(any, m)
			if argc >= 0 && methodParamCount(m) == argc {
				matched = append(matched, m)
			}
		}
		if sym := snap.SymTab[cur]; sym != nil {
			q = append(q, sym.SuperKeys...)
		}
	}
	if best := bestOverload(matched, argTypes); best != nil {
		return best
	}
	if len(any) > 0 {
		return any[0]
	}
	return nil
}

func pickMethodFromFile(fd *model.FileSlice, name string, argc int, argTypes []string) *model.Symbol {
	if fd == nil {
		return nil
	}
	var matched, any []*model.Symbol
	for _, sym := range fd.Symbols {
		if sym == nil || sym.Kind != "method" || sym.Name != name || sym.Declaration == nil {
			continue
		}
		any = append(any, sym)
		if argc >= 0 && methodParamCount(sym) == argc {
			matched = append(matched, sym)
		}
	}
	if best := bestOverload(matched, argTypes); best != nil {
		return best
	}
	if len(any) > 0 {
		return any[0]
	}
	return nil
}

func bestOverload(cands []*model.Symbol, argTypes []string) *model.Symbol {
	if len(cands) == 0 {
		return nil
	}
	if len(cands) == 1 || len(argTypes) == 0 {
		return cands[0]
	}
	best, bestScore := cands[0], -1
	for _, m := range cands {
		score := overloadScore(m, argTypes)
		if score > bestScore {
			bestScore = score
			best = m
		}
	}
	return best
}

func overloadScore(m *model.Symbol, argTypes []string) int {
	params := methodParamTypes(m)
	if len(params) != len(argTypes) {
		return 0
	}
	score := 0
	for i, want := range params {
		got := argTypes[i]
		if got == "" {
			continue // null / unknown — neutral
		}
		ws, gs := simpleTypeName(want), simpleTypeName(got)
		if ws == gs {
			score += 3
			continue
		}
		if compatibleSimple(ws, gs) {
			score += 1
			continue
		}
		score -= 2
	}
	return score
}

func methodParamTypes(sym *model.Symbol) []string {
	if sym == nil {
		return nil
	}
	prefix := "M|" + sym.OwnerKey + "|" + sym.Name + "|"
	if !strings.HasPrefix(sym.Key, prefix) {
		return nil
	}
	params := strings.TrimPrefix(sym.Key, prefix)
	if params == "" {
		return nil
	}
	parts := strings.Split(params, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func simpleTypeName(t string) string {
	t = strings.TrimPrefix(t, "T|")
	if i := strings.LastIndexAny(t, ".$"); i >= 0 {
		t = t[i+1:]
	}
	return t
}

func compatibleSimple(param, arg string) bool {
	if param == arg {
		return true
	}
	// Primitive widenings / boxing (best-effort).
	switch param {
	case "int", "Integer", "long", "Long", "short", "Short", "byte", "Byte":
		return arg == "int" || arg == "Integer" || arg == "long" || arg == "Long" ||
			arg == "short" || arg == "Short" || arg == "byte" || arg == "Byte"
	case "double", "Double", "float", "Float":
		return arg == "double" || arg == "Double" || arg == "float" || arg == "Float" ||
			arg == "int" || arg == "Integer"
	case "CharSequence":
		return arg == "String" || arg == "CharSequence"
	case "Object":
		return true
	}
	return false
}

// callArgTypes returns best-effort type keys for each argument of the call after nameEnd.
func callArgTypes(source string, nameEnd int, snap *store.Snapshot, fd *model.FileSlice) []string {
	if source == "" || nameEnd < 0 || nameEnd > len(source) {
		return nil
	}
	i := nameEnd
	for i < len(source) && isWS(source[i]) {
		i++
	}
	if i >= len(source) || source[i] != '(' {
		return nil
	}
	i++
	for i < len(source) && isWS(source[i]) {
		i++
	}
	if i < len(source) && source[i] == ')' {
		return nil
	}
	var args []string
	start := i
	depth := 1
	inStr, inChar := false, false
	esc := false
	flush := func(end int) {
		raw := strings.TrimSpace(source[start:end])
		args = append(args, inferExprType(raw, snap, fd, start))
	}
	for i < len(source) && depth > 0 {
		c := source[i]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			i++
			continue
		}
		if inChar {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '\'' {
				inChar = false
			}
			i++
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '\'':
			inChar = true
		case '(', '[', '{':
			depth++
		case ')':
			depth--
			if depth == 0 {
				flush(i)
			}
		case ']', '}':
			if depth > 1 {
				depth--
			}
		case ',':
			if depth == 1 {
				flush(i)
				start = i + 1
			}
		}
		i++
	}
	return args
}

func inferExprType(expr string, snap *store.Snapshot, fd *model.FileSlice, approxOff int) string {
	expr = strings.TrimSpace(expr)
	if expr == "" || expr == "null" {
		return ""
	}
	if len(expr) >= 2 && expr[0] == '"' {
		return "T|java.lang.String"
	}
	if len(expr) >= 3 && expr[0] == '\'' {
		return "T|char"
	}
	if expr == "true" || expr == "false" {
		return "T|boolean"
	}
	if isJavaIntLit(expr) {
		return "T|int"
	}
	if isJavaFloatLit(expr) {
		return "T|double"
	}
	// Simple identifier.
	if idS(expr[0]) {
		ok := true
		for i := 1; i < len(expr); i++ {
			if !idP(expr[i]) {
				ok = false
				break
			}
		}
		if ok {
			if loc := scopedLocalAt(fd, expr, approxOff, ""); loc != nil && loc.TypeKey != "" {
				if snap != nil {
					return snap.ResolveTypeKey(loc.TypeKey, fd)
				}
				return loc.TypeKey
			}
			if expr[0] >= 'A' && expr[0] <= 'Z' && snap != nil && fd != nil {
				return snap.ResolveTypeKey("T|"+ql(expr, fd.Package, fd.Imports), fd)
			}
			if fd != nil {
				owner := enclosingTypeAt(fd, approxOff)
				if f := snap.FindField(owner, expr); f != nil && f.TypeKey != "" {
					return snap.ResolveTypeKey(f.TypeKey, fd)
				}
			}
		}
	}
	return ""
}

func isJavaIntLit(s string) bool {
	if s == "" {
		return false
	}
	i := 0
	if s[0] == '-' || s[0] == '+' {
		i++
	}
	if i >= len(s) {
		return false
	}
	digits := false
	for ; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			digits = true
			continue
		}
		if c == 'L' || c == 'l' {
			return digits && i == len(s)-1
		}
		return false
	}
	return digits
}

func isJavaFloatLit(s string) bool {
	if !strings.ContainsAny(s, ".eEfFdD") {
		return false
	}
	i := 0
	if s[0] == '-' || s[0] == '+' {
		i++
	}
	seenDot, seenDig := false, false
	for ; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			seenDig = true
			continue
		}
		if c == '.' && !seenDot {
			seenDot = true
			continue
		}
		if (c == 'f' || c == 'F' || c == 'd' || c == 'D') && i == len(s)-1 {
			return seenDig
		}
		if c == 'e' || c == 'E' {
			return seenDig
		}
		return false
	}
	return seenDig && seenDot
}

func methodParamCount(sym *model.Symbol) int {
	if sym == nil {
		return -1
	}
	prefix := "M|" + sym.OwnerKey + "|" + sym.Name + "|"
	if !strings.HasPrefix(sym.Key, prefix) {
		return -1
	}
	params := strings.TrimPrefix(sym.Key, prefix)
	if params == "" {
		return 0
	}
	return strings.Count(params, ",") + 1
}

// callArgCount returns how many arguments are in the (...) after an identifier
// ending at nameEnd. Returns -1 when not a call or the list cannot be parsed.
func callArgCount(source string, nameEnd int) int {
	if source == "" || nameEnd < 0 || nameEnd > len(source) {
		return -1
	}
	i := nameEnd
	for i < len(source) && isWS(source[i]) {
		i++
	}
	if i >= len(source) || source[i] != '(' {
		return -1
	}
	i++ // past '('
	for i < len(source) && isWS(source[i]) {
		i++
	}
	if i < len(source) && source[i] == ')' {
		return 0
	}
	depth := 1
	args := 1
	inStr, inChar := false, false
	esc := false
	for i < len(source) && depth > 0 {
		c := source[i]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			i++
			continue
		}
		if inChar {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '\'' {
				inChar = false
			}
			i++
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '\'':
			inChar = true
		case '(', '[', '{':
			depth++
		case ')':
			depth--
		case ']', '}':
			if depth > 1 {
				depth--
			}
		case ',':
			if depth == 1 {
				args++
			}
		}
		i++
	}
	if depth != 0 {
		return args // best-effort on incomplete source
	}
	return args
}

// resolveMemberViaTypeKey resolves Owner.member after normalizing type keys / imports.
func resolveMemberViaTypeKey(snap *store.Snapshot, fd *model.FileSlice, typeKey, name string) *model.Symbol {
	return resolveMemberViaTypeKeyArity(snap, fd, typeKey, name, -1, nil)
}

func resolveMemberViaTypeKeyArity(snap *store.Snapshot, fd *model.FileSlice, typeKey, name string, argc int, argTypes []string) *model.Symbol {
	if typeKey == "" || name == "" {
		return nil
	}
	q := snap.ResolveTypeKey(typeKey, fd)
	if argc >= 0 {
		if res := pickMethodByArity(snap, q, name, argc, argTypes); res != nil {
			return res
		}
	}
	if res := resolveMemberOn(snap, q, name); res != nil {
		return res
	}
	// Ambiguous simple key (T|Result) — try every indexed type with that simple name.
	simple := typeKey
	if i := strings.LastIndex(simple, "|"); i >= 0 {
		simple = simple[i+1:]
	}
	if i := strings.LastIndexAny(simple, ".$"); i >= 0 {
		simple = simple[i+1:]
	}
	return resolveMemberViaTypeName(snap, fd, simple, name)
}

// resolveMemberViaTypeName finds Type.member when imports are missing or Result is ambiguous.
// Prefers the imported / uniquely resolved type; else the only candidate that owns the member.
func resolveMemberViaTypeName(snap *store.Snapshot, fd *model.FileSlice, typeName, member string) *model.Symbol {
	if typeName == "" || member == "" || snap == nil {
		return nil
	}
	q := snap.ResolveTypeKey("T|"+typeName, fd)
	if snap.SymTab[q] != nil {
		if res := resolveMemberOn(snap, q, member); res != nil {
			return res
		}
	}
	seen := map[string]bool{}
	var hits []*model.Symbol
	for _, cand := range snap.Simple[typeName] {
		if res := resolveMemberOn(snap, cand, member); res != nil && !seen[res.Key] {
			seen[res.Key] = true
			hits = append(hits, res)
		}
	}
	if len(hits) == 1 {
		return hits[0]
	}
	return nil
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

// typeKeyOfReceiver resolves the type of the expression left of '.' / '::' at dotPos.
func typeKeyOfReceiver(snap *store.Snapshot, fd *model.FileSlice, source string, dotPos int) string {
	tk, _ := typeAndElemOfReceiver(snap, fd, source, dotPos)
	return tk
}

// typeAndElemOfReceiver also returns the first type-arg (List<T> → T) when known.
func typeAndElemOfReceiver(snap *store.Snapshot, fd *model.FileSlice, source string, dotPos int) (typeKey, elemType string) {
	if snap == nil || fd == nil || source == "" || dotPos <= 0 || dotPos > len(source) {
		return "", ""
	}
	j := dotPos - 1
	for j >= 0 && isWS(source[j]) {
		j--
	}
	if j < 0 {
		return "", ""
	}

	if source[j] == ')' {
		open := matchParenOpen(source, j)
		if open < 0 {
			return "", ""
		}
		e := open - 1
		for e >= 0 && isWS(source[e]) {
			e--
		}
		if e < 0 || !idP(source[e]) {
			return "", ""
		}
		en := e
		for e >= 0 && idP(source[e]) {
			e--
		}
		callName := source[e+1 : en+1]
		w0 := e
		for w0 >= 0 && isWS(source[w0]) {
			w0--
		}
		w2 := w0
		for w2 >= 0 && idP(source[w2]) {
			w2--
		}
		if w0 > w2 && source[w2+1:w0+1] == "new" {
			tk := snap.ResolveTypeKey("T|"+ql(callName, fd.Package, fd.Imports), fd)
			return tk, ""
		}
		owner, ownerElem := "", ""
		recvDot := e
		for recvDot >= 0 && isWS(source[recvDot]) {
			recvDot--
		}
		if recvDot >= 0 && source[recvDot] == '.' {
			owner, ownerElem = typeAndElemOfReceiver(snap, fd, source, recvDot)
		} else if recvDot >= 1 && source[recvDot] == ':' && source[recvDot-1] == ':' {
			owner, ownerElem = typeAndElemOfReceiver(snap, fd, source, recvDot-1)
		} else {
			owner = enclosingTypeAt(fd, open)
		}
		if owner == "" {
			return "", ""
		}
		argc := callArgCount(source, en+1)
		argTypes := callArgTypes(source, en+1, snap, fd)
		// Collection/Optional extractors: list.get(i) → element type.
		if ownerElem != "" && isElemExtractor(callName) {
			return snap.ResolveTypeKey(ownerElem, fd), ""
		}
		m := pickMethodByArity(snap, owner, callName, argc, argTypes)
		if m == nil {
			m = snap.FindMethodInHierarchy(owner, callName)
		}
		if m != nil && m.ReturnType != "" {
			rt := snap.ResolveTypeKey(m.ReturnType, fd)
			el := m.ElemType
			if el == "" && isElemFactory(callName) {
				if at := firstNonEmpty(argTypes); at != "" {
					el = at
				}
			}
			return rt, snap.ResolveTypeKey(el, fd)
		}
		// Optional.of(x) when Optional is not indexed — keep owner + arg elem.
		if isElemFactory(callName) {
			if at := firstNonEmpty(argTypes); at != "" {
				return owner, snap.ResolveTypeKey(at, fd)
			}
			return owner, ""
		}
		for _, prop := range AccessorProps(callName) {
			if f := snap.FindField(owner, prop); f != nil && f.TypeKey != "" {
				return snap.ResolveTypeKey(f.TypeKey, fd), snap.ResolveTypeKey(f.ElemType, fd)
			}
		}
		return "", ""
	}

	if !idP(source[j]) {
		return "", ""
	}
	st := j
	for j >= 0 && idP(source[j]) {
		j--
	}
	base := source[j+1 : st+1]
	if base == "this" || base == "super" {
		return enclosingTypeAt(fd, st), ""
	}
	k := j
	for k >= 0 && isWS(source[k]) {
		k--
	}
	if k >= 0 && source[k] == '.' {
		outer, _ := typeAndElemOfReceiver(snap, fd, source, k)
		if outer == "" {
			return "", ""
		}
		if f := snap.FindField(outer, base); f != nil && f.TypeKey != "" {
			return snap.ResolveTypeKey(f.TypeKey, fd), snap.ResolveTypeKey(f.ElemType, fd)
		}
		if len(base) > 0 {
			getter := "get" + strings.ToUpper(base[:1]) + base[1:]
			if m := snap.FindMethodInHierarchy(outer, getter); m != nil && m.ReturnType != "" {
				return snap.ResolveTypeKey(m.ReturnType, fd), snap.ResolveTypeKey(m.ElemType, fd)
			}
			if f := snap.FindField(outer, base); f != nil && f.TypeKey != "" {
				return snap.ResolveTypeKey(f.TypeKey, fd), snap.ResolveTypeKey(f.ElemType, fd)
			}
		}
		return "", ""
	}
	if loc := scopedLocalAt(fd, base, st, source); loc != nil && loc.TypeKey != "" {
		return snap.ResolveTypeKey(loc.TypeKey, fd), snap.ResolveTypeKey(loc.ElemType, fd)
	}
	if len(base) > 0 && base[0] >= 'A' && base[0] <= 'Z' {
		return snap.ResolveTypeKey("T|"+ql(base, fd.Package, fd.Imports), fd), ""
	}
	owner := enclosingTypeAt(fd, st)
	if f := snap.FindField(owner, base); f != nil && f.TypeKey != "" {
		return snap.ResolveTypeKey(f.TypeKey, fd), snap.ResolveTypeKey(f.ElemType, fd)
	}
	return "", ""
}

func isElemExtractor(name string) bool {
	switch name {
	case "get", "getFirst", "getLast", "remove", "elementAt", "orElse", "orElseGet",
		"findFirst", "findAny", "iterator":
		return true
	default:
		return false
	}
}

func isElemFactory(name string) bool {
	switch name {
	case "of", "ofNullable", "just", "some":
		return true
	default:
		return false
	}
}

func firstNonEmpty(ss []string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func matchParenOpen(source string, closeIdx int) int {
	if closeIdx < 0 || closeIdx >= len(source) || source[closeIdx] != ')' {
		return -1
	}
	depth := 0
	for i := closeIdx; i >= 0; i-- {
		switch source[i] {
		case ')':
			depth++
		case '(':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func enclosingTypeAt(fd *model.FileSlice, offset int) string {
	if fd == nil {
		return ""
	}
	for i := len(fd.Occs) - 1; i >= 0; i-- {
		o := fd.Occs[i]
		if o != nil && o.Start <= offset && o.OwnerTypeKey != "" {
			return o.OwnerTypeKey
		}
	}
	for _, sym := range fd.Symbols {
		if sym != nil && sym.Kind == "type" && sym.Key != "" {
			return sym.Key
		}
	}
	return ""
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
