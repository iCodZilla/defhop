package java

import (
	"defhop/internal/model"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// Parse produces a FileSlice for one Java source. Pure tokenizer — no JVM.
func Parse(filePath, source string, gen uint64) *model.FileSlice {
	if strings.HasPrefix(source, "\ufeff") {
		source = source[len("\ufeff"):]
	}
	r := &parseResult{Source: source}
	r.run(filePath, source)
	return &model.FileSlice{
		Path:    filepath.ToSlash(filePath),
		Package: r.Package,
		Imports: r.imports,
		Symbols: r.Symbols,
		Occs:    r.Occs,
		Gen:     gen,
	}
}

type typeRange struct {
	start, end int
	key        string
}

type parseResult struct {
	Source           string
	Package          string
	Symbols          []*model.Symbol
	Occs             []*model.Occ
	imports          []string
	typeRanges       []typeRange
	elemTypes        map[string]string
	methodElem       map[string]string
	typeConfigPrefix map[string]string // typeKey -> @ConfigurationProperties prefix
	typeHttpPrefix   map[string]string // typeKey -> class @RequestMapping path
}

func (r *parseResult) run(filePath, source string) {
	p := filepath.ToSlash(filePath)
	z := lxNew(source)

	vnt := make(map[string]string)
	r.elemTypes = make(map[string]string)
	r.methodElem = make(map[string]string)

	tk := z.nx()
	for tk.t == tID && tk.s == "package" {
		tk = z.nx()
		var sb strings.Builder
		for tk.t != tSEMI && tk.t != tEOF {
			if tk.t == tID || tk.t == tDOT {
				sb.WriteString(tk.s)
			}
			tk = z.nx()
		}
		r.Package = sb.String()
		tk = z.nx()
	}

	var imps []string
	for tk.t == tID && tk.s == "import" {
		tk = z.nx()
		isStatic := false
		if tk.t == tID && tk.s == "static" {
			isStatic = true
			tk = z.nx()
		}
		var sb strings.Builder
		type seg struct {
			name string
			off  int
		}
		var segs []seg
		for tk.t != tSEMI && tk.t != tEOF {
			if tk.t == tID {
				segs = append(segs, seg{name: tk.s, off: tk.a})
				sb.WriteString(tk.s)
			} else if tk.t == tDOT || tk.t == tSTAR {
				sb.WriteString(tk.s)
			}
			tk = z.nx()
		}
		full := sb.String()
		imps = append(imps, full)
		if len(segs) > 0 && !strings.HasSuffix(full, "*") {
			typeEnd := len(segs)
			if isStatic {
				typeEnd--
			}
			if typeEnd > 0 {
				parts := make([]string, typeEnd)
				for i, s := range segs[:typeEnd] {
					parts[i] = s.name
				}
				typeFQN := strings.Join(parts, ".")
				simple := segs[typeEnd-1]
				r.addTypeRefAt(typeFQN, simple.name, "T|"+typeFQN, simple.off, p, source)
			}
		}
		tk = z.nx()
	}
	r.imports = imps

	for tk.t != tEOF {
		annStart := tk.a
		var topAnns []string
		tk = skipAnns(z, tk, &topAnns)
		for tk.t == tID && isMod(tk.s) {
			tk = z.nx()
		}
		if tk.t == tID && isTypeKW(tk.s) {
			tk = r.parseType(tk, z, p, source, imps, vnt, topAnns, annStart, "")
			continue
		}
		if tk.t == tEOF {
			break
		}
		tk = z.nx()
	}
	// Single-pass lexer: string/number spans + comment regions collected during parse.
	excl := make([]iv, 0, len(z.lits)+len(z.regions))
	excl = append(excl, z.lits...)
	excl = append(excl, z.regions...)
	r.scanRefs(p, source, excl, vnt)
}

func skipAnns(z *lx, tk tok, anns *[]string) tok {
	for tk.t == tAT {
		tk = z.nx()
		name := tk.s
		for {
			nt := z.nx()
			if nt.t == tDOT {
				nt = z.nx()
				if nt.t == tID {
					name = nt.s
				}
				continue
			}
			tk = nt
			break
		}
		value := ""
		if tk.t == tLP {
			value = annStringArg(z)
			skP(z)
			tk = z.nx()
		}
		if anns != nil && name != "" {
			*anns = append(*anns, name)
			if value != "" {
				*anns = append(*anns, name+":"+value)
			}
		}
	}
	return tk
}

// annStringArg peeks the first string literal inside annotation (...), without consuming.
func annStringArg(z *lx) string {
	if z == nil {
		return ""
	}
	save := z.pos
	defer func() { z.pos = save }()
	lit := func(t tok) string {
		if t.t != tSTR || t.b <= t.a+1 || t.b > len(z.src) {
			return ""
		}
		// tokens keep quotes in [a,b)
		inner := string(z.src[t.a+1 : t.b-1])
		return inner
	}
	tk := z.nx()
	if s := lit(tk); s != "" {
		return s
	}
	// value = "..." or name = "..."
	if tk.t == tID {
		eq := z.nx()
		if eq.t == tASGN {
			if s := lit(z.nx()); s != "" {
				return s
			}
		}
	}
	return ""
}

func (r *parseResult) parseType(tk tok, z *lx, p, source string, imps []string,
	vnt map[string]string, anns []string, annStart int, enclosingFQN string) tok {

	isIF := tk.s == "interface"
	isRecord := tk.s == "record"
	typeKWStart := tk.a
	tk = z.nx()
	if tk.t != tID {
		return tk
	}
	nm := tk.s
	fqn := nm
	switch {
	case enclosingFQN != "":
		// Nested type: Outer.Inner (matches static-import FQNs).
		fqn = enclosingFQN + "." + nm
	case r.Package != "":
		fqn = r.Package + "." + nm
	}
	typeKey := "T|" + fqn
	l := locAt(p, source, tk.a, tk.b, "type", fqn)
	sy := &model.Symbol{Key: typeKey, Kind: "type", Name: fqn,
		Declaration: l, InterfaceType: isIF, Annotations: anns, Imports: imps}
	if annStart < typeKWStart {
		region := source[annStart:typeKWStart]
		if hasAnn(anns, "ConfigurationProperties") {
			if prefix := ExtractConfigPrefix(region); prefix != "" {
				sy.ConfigPrefix = prefix
				if r.typeConfigPrefix == nil {
					r.typeConfigPrefix = map[string]string{}
				}
				r.typeConfigPrefix[typeKey] = prefix
			}
		}
		if hasAnn(anns, "FeignClient") {
			sy.FeignService = ExtractFeignService(region)
		}
		if hasAnn(anns, "RequestMapping") {
			if cp := ExtractClassRequestPath(region); cp != "" {
				sy.HttpPath = NormHttpPath(cp)
				if r.typeHttpPrefix == nil {
					r.typeHttpPrefix = map[string]string{}
				}
				r.typeHttpPrefix[typeKey] = sy.HttpPath
			}
		}
	}
	r.Symbols = append(r.Symbols, sy)
	r.Occs = append(r.Occs, &model.Occ{Start: tk.a, End: tk.b, Location: l,
		Role: "declaration", Name: nm, TargetKey: typeKey})

	tk = z.nx()
	if tk.t == tLT {
		tk = skAng(z, tk)
	}
	// record Name(components) [implements ...] { body }
	if isRecord && tk.t == tLP {
		_, comps, next := r.parseParams(z, tk, vnt, imps)
		tk = next
		for _, c := range comps {
			if c.Name == "" {
				continue
			}
			ftk := "T|" + c.Type
			fkey := "F|" + typeKey + "|" + c.Name
			fl := locAt(p, source, c.Start, c.End, "field", c.Name)
			if fl == nil {
				fl = l
			}
			r.Symbols = append(r.Symbols, &model.Symbol{
				Key: fkey, Kind: "field", Name: c.Name, OwnerKey: typeKey,
				Declaration: fl, TypeKey: ftk, Imports: imps,
			})
			r.Occs = append(r.Occs, &model.Occ{Start: c.Start, End: c.End, Location: fl,
				Role: "declaration", Name: c.Name, TargetKey: fkey})
			// Accessor: same name as component (Point::x / p.x()).
			mkey := "M|" + typeKey + "|" + c.Name + "|"
			r.Symbols = append(r.Symbols, &model.Symbol{
				Key: mkey, Kind: "method", Name: c.Name, OwnerKey: typeKey,
				Declaration: fl, ReturnType: ftk, Imports: imps,
			})
			r.methodElem[typeKey+"#"+c.Name] = ftk
		}
	}
	if tk.t == tID && tk.s == "extends" {
		tk = z.nx()
		scOff := tk.a
		var sc string
		sc, tk = readType(z, tk)
		var args []string
		if tk.t == tLT {
			args, tk = r.readTypeArgs(z, tk, p, source, imps)
		}
		if sc != "" && sc != "Object" {
			superKey := "T|" + ql(sc, r.Package, imps)
			sy.SuperKeys = append(sy.SuperKeys, superKey)
			sy.ExtendsKey = superKey
			sy.ExtendsArgs = args
			r.addTypeRef(sc, superKey, scOff, p, source)
		}
	}
	if tk.t == tID && tk.s == "implements" {
		tk = z.nx()
		for {
			ifOff := tk.a
			var ifn string
			ifn, tk = readType(z, tk)
			for tk.t == tLT {
				tk = skAng(z, tk)
			}
			if ifn != "" {
				ifKey := "T|" + ql(ifn, r.Package, imps)
				sy.SuperKeys = append(sy.SuperKeys, ifKey)
				r.addTypeRef(ifn, ifKey, ifOff, p, source)
			}
			if tk.t == tCOMMA {
				tk = z.nx()
				continue
			}
			break
		}
	}
	// sealed … permits A, B — skip so the body still indexes.
	if tk.t == tID && tk.s == "permits" {
		tk = z.nx()
		for {
			_, tk = readType(z, tk)
			for tk.t == tLT {
				tk = skAng(z, tk)
			}
			if tk.t == tCOMMA {
				tk = z.nx()
				continue
			}
			break
		}
	}
	if tk.t != tLB {
		return tk
	}
	bodyStart := tk.a
	tk = z.nx()
	for tk.t != tRB && tk.t != tEOF {
		if tk.t == tLB {
			tk = skBlk(z, tk)
			continue
		}
		if tk.t == tSEMI {
			tk = z.nx()
			continue
		}
		tk = r.member(tk, z, p, source, typeKey, imps, vnt)
	}
	if tk.t == tRB {
		bodyEnd := tk.b
		tk = z.nx()
		r.typeRanges = append(r.typeRanges, typeRange{bodyStart, bodyEnd, typeKey})
	}
	return tk
}

func (r *parseResult) member(tk tok, z *lx, p, source, typeKey string,
	imps []string, vnt map[string]string) tok {

	var anns []string
	memberStart := tk.a
	tk = skipAnns(z, tk, &anns)
	for tk.t == tID && isMod(tk.s) {
		if tk.s == "final" {
			anns = append(anns, "final")
		}
		tk = z.nx()
	}
	if tk.t == tID && isTypeKW(tk.s) {
		outer := strings.TrimPrefix(typeKey, "T|")
		return r.parseType(tk, z, p, source, imps, vnt, anns, memberStart, outer)
	}

	var head []tok
	for tk.t == tID || tk.t == tDOT || tk.t == tLBR || tk.t == tRBR ||
		tk.t == tLT || tk.t == tGT || tk.t == tCOMMA {
		head = append(head, tk)
		tk = z.nx()
	}
	if len(head) == 0 {
		if tk.t == tLB {
			return skBlk(z, tk)
		}
		if tk.t == tEOF {
			return tk
		}
		return z.nx()
	}
	nameIdx := -1
	for i := len(head) - 1; i >= 0; i-- {
		if head[i].t == tID {
			nameIdx = i
			break
		}
	}
	if nameIdx < 0 {
		if tk.t == tEOF {
			return tk
		}
		return z.nx()
	}
	nmTok := head[nameIdx]
	nm := nmTok.s
	var ftype, gtype strings.Builder
	inGen := false
	for i := 0; i < nameIdx; i++ {
		t := head[i]
		if t.t == tLT {
			inGen = true
			gtype.WriteString("<")
			continue
		}
		if t.t == tGT {
			inGen = false
			gtype.WriteString(">")
			continue
		}
		if t.t == tCOMMA {
			if inGen {
				gtype.WriteString(",")
			}
			continue
		}
		if inGen {
			gtype.WriteString(t.s)
		} else {
			ftype.WriteString(t.s)
		}
	}
	ft := ftype.String()
	ftFull := ft
	if gtype.Len() > 0 {
		ftFull = ft + gtype.String()
	}

	if tk.t == tLP {
		kind := "method"
		if ft == "" {
			kind = "constructor"
		}
		paramsSig, params, next := r.parseParams(z, tk, vnt, imps)
		tk = next
		mkey := "M|" + typeKey + "|" + nm + "|" + paramsSig
		l := locAt(p, source, nmTok.a, nmTok.b, kind, nm)
		sy := &model.Symbol{Key: mkey, Kind: kind, Name: nm, OwnerKey: typeKey,
			Declaration: l, Annotations: anns}
		if kind == "method" && ft != "" {
			sy.ReturnType = "T|" + ql(ft, r.Package, imps)
			if fe := fieldElem(ftFull, r.Package, imps); fe != "" {
				sy.ElemType = fe
				r.methodElem[typeKey+"#"+nm] = fe
			}
		}
		if kind == "method" && memberStart < nmTok.a {
			hm, hp := ExtractMethodMapping(anns, source[memberStart:nmTok.a])
			if hp != "" || hm != "" {
				prefix := r.typeHttpPrefix[typeKey]
				sy.HttpMethod = hm
				sy.HttpPath = JoinHttpPath(prefix, hp)
			}
		}
		r.Symbols = append(r.Symbols, sy)
		r.Occs = append(r.Occs, &model.Occ{Start: nmTok.a, End: nmTok.b, Location: l,
			Role: "declaration", Name: nm, TargetKey: mkey})

		if hasMapping(anns) {
			r.addMapStructProps(p, source, memberStart, nmTok.a, ft, params, imps)
		}
		if tk.t == tID && tk.s == "throws" {
			for tk.t != tLB && tk.t != tSEMI && tk.t != tEOF {
				tk = z.nx()
			}
		}
		if tk.t == tLB {
			return skBlk(z, tk)
		}
		return tk
	}

	fkey := "F|" + typeKey + "|" + nm
	ftk := "T|" + ql(ft, r.Package, imps)
	vnt[nm] = ftk
	if fe := fieldElem(ftFull, r.Package, imps); fe != "" {
		r.elemTypes[nm] = fe
		r.methodElem[typeKey+"#"+elemGetter(nm)] = fe
		r.methodElem[typeKey+"#"+nm] = fe
	}
	l := locAt(p, source, nmTok.a, nmTok.b, "field", nm)
	sy := &model.Symbol{Key: fkey, Kind: "field", Name: nm, OwnerKey: typeKey,
		Declaration: l, TypeKey: ftk, Annotations: anns, Imports: imps}
	if fe := fieldElem(ftFull, r.Package, imps); fe != "" {
		sy.ElemType = fe
	}
	if hasAnn(anns, "Value") {
		r.addValueConfigOccs(p, source, memberStart, nmTok.a, sy)
	}
	if prefix := r.typeConfigPrefix[typeKey]; prefix != "" {
		for _, k := range ConfigPropertyKeys(prefix, nm) {
			sy.ConfigKeys = appendUniqueStr(sy.ConfigKeys, k)
		}
	}
	r.Symbols = append(r.Symbols, sy)
	r.Occs = append(r.Occs, &model.Occ{Start: nmTok.a, End: nmTok.b, Location: l,
		Role: "declaration", Name: nm, TargetKey: fkey})
	// Type name in "private Foo bar" → reference Occ so F12 on Foo works.
	if typeTok := lastTypeID(head[:nameIdx]); typeTok != nil && ft != "" {
		tl := locAt(p, source, typeTok.a, typeTok.b, "type", typeTok.s)
		r.Occs = append(r.Occs, &model.Occ{Start: typeTok.a, End: typeTok.b, Location: tl,
			Role: "reference", Name: typeTok.s, TargetKey: ftk})
	}
	if tk.t == tASGN {
		tk = skInit(z, tk)
	}
	return tk
}

type paramDecl struct {
	Type       string
	Name       string
	Start, End int
}

func (r *parseResult) parseParams(z *lx, tk tok, vnt map[string]string, imps []string) (string, []paramDecl, tok) {
	var params []paramDecl
	var sigParts []string
	if tk.t == tLP {
		tk = z.nx()
	}
	for tk.t != tRP && tk.t != tEOF {
		tk = skipAnns(z, tk, nil)
		var pt []tok
		for tk.t != tRP && tk.t != tCOMMA && tk.t != tEOF {
			if tk.t == tAT {
				tk = skipAnns(z, tk, nil)
				continue
			}
			if tk.t == tLP {
				skP(z)
				tk = z.nx()
				continue
			}
			if tk.t == tLT || tk.t == tGT {
				d := 0
				for tk.t != tEOF {
					if tk.t == tLT {
						d++
					}
					if tk.t == tGT {
						d--
					}
					pt = append(pt, tk)
					tk = z.nx()
					if d <= 0 && (tk.t == tID || tk.t == tCOMMA || tk.t == tRP) {
						break
					}
				}
				continue
			}
			if tk.t == tID || tk.t == tDOT || tk.t == tLBR || tk.t == tRBR {
				pt = append(pt, tk)
				tk = z.nx()
				continue
			}
			tk = z.nx()
		}
		if tk.t == tCOMMA {
			tk = z.nx()
		}
		ni := -1
		for i := len(pt) - 1; i >= 0; i-- {
			if pt[i].t == tID {
				ni = i
				break
			}
		}
		if ni > 0 {
			var tb strings.Builder
			generic := false
			gb := strings.Builder{}
			for i := 0; i < ni; i++ {
				t := pt[i]
				if t.t == tLT {
					generic = true
				}
				if generic {
					gb.WriteString(t.s)
				}
				if t.t == tLT {
					break
				}
				if t.t == tGT || t.t == tCOMMA {
					continue
				}
				if !generic {
					tb.WriteString(t.s)
				}
			}
			pType := ql(strings.TrimSpace(tb.String()), r.Package, imps)
			vnt[pt[ni].s] = "T|" + pType
			if generic {
				if fe := fieldElem(gb.String(), r.Package, imps); fe != "" {
					r.elemTypes[pt[ni].s] = fe
				}
			}
			params = append(params, paramDecl{
				Type: pType, Name: pt[ni].s, Start: pt[ni].a, End: pt[ni].b,
			})
			sigParts = append(sigParts, pType)
		}
	}
	if tk.t == tRP {
		tk = z.nx()
	}
	return strings.Join(sigParts, ","), params, tk
}

func (r *parseResult) innermostType(pos int) string {
	best, bestSize := "", 1<<30
	for _, tr := range r.typeRanges {
		if pos >= tr.start && pos < tr.end && (tr.end-tr.start) < bestSize {
			bestSize = tr.end - tr.start
			best = tr.key
		}
	}
	return best
}

func (r *parseResult) addTypeRef(typeText, superKey string, startOff int, p, source string) {
	simple := typeText
	if i := strings.LastIndex(simple, "."); i >= 0 {
		simple = simple[i+1:]
	}
	r.addTypeRefAt(typeText, simple, superKey, startOff+len(typeText)-len(simple), p, source)
}

func (r *parseResult) addTypeRefAt(typeFQN, simple, superKey string, startOff int, p, source string) {
	_ = typeFQN
	if simple == "" || startOff < 0 || startOff+len(simple) > len(source) {
		return
	}
	if source[startOff:startOff+len(simple)] != simple {
		return
	}
	l := locAt(p, source, startOff, startOff+len(simple), "reference", simple)
	r.Occs = append(r.Occs, &model.Occ{Start: startOff, End: startOff + len(simple),
		Location: l, Role: "reference", Name: simple, TargetKey: superKey})
}

func hasMapping(anns []string) bool {
	for _, a := range anns {
		if a == "Mapping" || a == "Mappings" {
			return true
		}
	}
	return false
}

func (r *parseResult) addMapStructProps(p, source string, annStart, annEnd int,
	returnType string, params []paramDecl, imps []string) {
	if annStart < 0 || annEnd > len(source) || annStart >= annEnd {
		return
	}
	region := source[annStart:annEnd]
	targetOwner := ""
	if returnType != "" {
		targetOwner = ql(returnType, r.Package, imps)
	}
	sourceOwner := ""
	for _, pd := range params {
		if pd.Type != "" {
			sourceOwner = pd.Type
			break
		}
	}
	i := 0
	for i < len(region) {
		c := region[i]
		if !idS(c) {
			i++
			continue
		}
		a := i
		for i < len(region) && idP(region[i]) {
			i++
		}
		key := region[a:i]
		j := i
		for j < len(region) && isWS(region[j]) {
			j++
		}
		if j >= len(region) || region[j] != '=' {
			continue
		}
		j++
		for j < len(region) && isWS(region[j]) {
			j++
		}
		if j >= len(region) || region[j] != '"' {
			continue
		}
		k := j + 1
		for k < len(region) && region[k] != '"' {
			k++
		}
		if k >= len(region) {
			continue
		}
		value := region[j+1 : k]
		if value == "" {
			continue
		}
		var owner string
		switch key {
		case "target":
			owner = targetOwner
		case "source":
			owner = sourceOwner
		default:
			continue
		}
		if owner == "" {
			continue
		}
		valA, valB := annStart+j+1, annStart+k
		l := locAt(p, source, valA, valB, "property", value)
		r.Occs = append(r.Occs, &model.Occ{Start: valA, End: valB, Location: l,
			Role: "property", Name: value, PropertyName: value, QualifierKey: "T|" + owner})
		i = k + 1
	}
}

func isWS(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func readType(z *lx, tk tok) (string, tok) {
	var sb strings.Builder
	if tk.t == tID {
		sb.WriteString(tk.s)
		tk = z.nx()
	}
	for tk.t == tID || tk.t == tDOT {
		sb.WriteString(tk.s)
		tk = z.nx()
	}
	return strings.TrimSpace(sb.String()), tk
}

// readTypeArgs parses <A, B<C>, D> starting at tLT. Returns qualified arg names.
func (r *parseResult) readTypeArgs(z *lx, tk tok, p, source string, imps []string) ([]string, tok) {
	if tk.t != tLT {
		return nil, tk
	}
	var args []string
	tk = z.nx()
	for tk.t != tGT && tk.t != tEOF {
		if tk.t == tCOMMA {
			tk = z.nx()
			continue
		}
		if tk.t != tID {
			// wildcard '?' etc. — skip one token
			tk = z.nx()
			continue
		}
		off := tk.a
		var name string
		name, tk = readType(z, tk)
		for tk.t == tLT {
			tk = skAng(z, tk)
		}
		if name != "" {
			q := ql(name, r.Package, imps)
			args = append(args, q)
			r.addTypeRef(name, "T|"+q, off, p, source)
		}
	}
	if tk.t == tGT {
		tk = z.nx()
	}
	return args, tk
}

func lastTypeID(head []tok) *tok {
	for i := len(head) - 1; i >= 0; i-- {
		if head[i].t == tID {
			t := head[i]
			return &t
		}
	}
	return nil
}

func ql(name, pkg string, imps []string) string {
	if strings.Contains(name, ".") {
		return name
	}
	for _, imp := range imps {
		imp = strings.TrimSuffix(imp, ".*")
		if strings.HasSuffix(imp, "."+name) {
			return imp
		}
	}
	if pkg != "" {
		return pkg + "." + name
	}
	return name
}

func fieldElem(ft, pkg string, imps []string) string {
	e := genElem(ft)
	if e == "" || e == "?" {
		return ""
	}
	return "T|" + ql(e, pkg, imps)
}

func genElem(ft string) string {
	lt := strings.Index(ft, "<")
	if lt < 0 {
		return ""
	}
	depth, start := 0, -1
	for i := lt; i < len(ft); i++ {
		switch ft[i] {
		case '<':
			depth++
			if depth == 1 {
				start = i + 1
			}
		case '>':
			depth--
			if depth == 0 && start >= 0 {
				return trimGenHead(ft[start:i])
			}
		case ',':
			if depth == 1 && start >= 0 {
				return trimGenHead(ft[start:i])
			}
		}
	}
	return ""
}

func trimGenHead(s string) string {
	if j := strings.Index(s, "<"); j >= 0 {
		s = s[:j]
	}
	return strings.TrimSpace(s)
}

func elemGetter(field string) string {
	if field == "" {
		return field
	}
	return "get" + strings.ToUpper(field[:1]) + field[1:]
}

func locAt(p, source string, a, b int, kind, label string) *model.Location {
	ln, ls := 0, 0
	for i := 0; i < a && i < len(source); i++ {
		if source[i] == '\n' {
			ln++
			ls = i + 1
		}
	}
	return &model.Location{Path: p, Line: ln, Character: u16col(source, ls, a),
		EndLine: ln, EndCharacter: u16col(source, ls, b), Kind: kind, Label: label}
}

func u16col(src string, lineStart, target int) int {
	col := 0
	for i := lineStart; i < target && i < len(src); {
		r, size := utf8.DecodeRuneInString(src[i:])
		if size == 0 {
			break
		}
		if r > 0xFFFF {
			col += 2
		} else {
			col++
		}
		i += size
	}
	return col
}

func skP(z *lx) {
	d := 1
	for d > 0 {
		t := z.nx()
		if t.t == tEOF {
			return
		}
		if t.t == tLP {
			d++
		}
		if t.t == tRP {
			d--
		}
	}
}

func skBlk(z *lx, tk tok) tok {
	if tk.t != tLB {
		return tk
	}
	d := 1
	for d > 0 {
		t := z.nx()
		if t.t == tEOF {
			return t
		}
		if t.t == tLB {
			d++
		}
		if t.t == tRB {
			d--
		}
	}
	return z.nx()
}

func skAng(z *lx, tk tok) tok {
	if tk.t != tLT {
		return tk
	}
	d := 1
	for d > 0 {
		t := z.nx()
		if t.t == tEOF {
			return t
		}
		if t.t == tLT {
			d++
		}
		if t.t == tGT {
			d--
		}
	}
	return z.nx()
}

func skInit(z *lx, tk tok) tok {
	d := 0
	for {
		t := z.nx()
		if t.t == tEOF {
			return t
		}
		if t.t == tLB || t.t == tLP {
			d++
		}
		if t.t == tRB || t.t == tRP {
			d--
		}
		if t.t == tSEMI && d <= 0 {
			return z.nx()
		}
		if t.t == tCOMMA && d <= 0 {
			return t
		}
	}
}

// Exported helpers used by resolve / position utils.
func OffsetAt(text string, line, character int) int {
	if text == "" {
		return 0
	}
	if line < 0 {
		line = 0
	}
	if character < 0 {
		character = 0
	}
	off := 0
	for cur := 0; cur < line && off < len(text); cur++ {
		nl := strings.IndexByte(text[off:], '\n')
		if nl < 0 {
			return len(text)
		}
		off += nl + 1
	}
	units := 0
	for off < len(text) && units < character {
		if text[off] == '\n' || text[off] == '\r' {
			break
		}
		r, size := utf8.DecodeRuneInString(text[off:])
		if size == 0 {
			break
		}
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
		off += size
	}
	if off > len(text) {
		return len(text)
	}
	return off
}

func TokenAt(text string, offset int) string {
	if text == "" || offset < 0 || offset >= len(text) {
		return ""
	}
	idx := offset
	if !isIdentPart(text[idx]) && idx > 0 && isIdentPart(text[idx-1]) {
		idx--
	}
	start := idx
	for start > 0 && isIdentPart(text[start-1]) {
		start--
	}
	end := idx
	for end < len(text) && isIdentPart(text[end]) {
		end++
	}
	if start >= end {
		return ""
	}
	return text[start:end]
}

func isIdentPart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '$'
}

// Ensure excl sort used by scanRefs.
var _ = sort.Ints
