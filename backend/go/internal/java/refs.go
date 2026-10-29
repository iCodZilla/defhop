package java

import (
	"defhop/internal/model"
	"sort"
	"strings"
)

func (r *parseResult) scanRefs(p, source string, excl []iv, vnt map[string]string) {
	sort.Slice(excl, func(a, b int) bool { return excl[a].a < excl[b].a })
	exclStart := make([]int, len(excl))
	for i, e := range excl {
		exclStart[i] = e.a
	}
	inExcl := func(off int) bool {
		i := sort.SearchInts(exclStart, off+1) - 1
		return i >= 0 && off < excl[i].b
	}
	var declSpans []iv
	for _, o := range r.Occs {
		if o.Role == "declaration" {
			declSpans = append(declSpans, iv{o.Start, o.End})
		}
	}
	sort.Slice(declSpans, func(a, b int) bool { return declSpans[a].a < declSpans[b].a })
	declStart := make([]int, len(declSpans))
	for i, s := range declSpans {
		declStart[i] = s.a
	}
	inDecl := func(off int) bool {
		i := sort.SearchInts(declStart, off+1) - 1
		return i >= 0 && off < declSpans[i].b
	}
	lineAt := newLineIndex(source)

	prevWord := func(off int) string {
		j := off - 1
		for j >= 0 && isWS(source[j]) {
			j--
		}
		if j >= 0 && source[j] == '>' {
			depth := 0
			for j >= 0 {
				if source[j] == '>' {
					depth++
				}
				if source[j] == '<' {
					depth--
					if depth == 0 {
						j--
						break
					}
				}
				j--
			}
			for j >= 0 && isWS(source[j]) {
				j--
			}
		}
		if j < 0 || inExcl(j) {
			return ""
		}
		end := j + 1
		for j >= 0 && idP(source[j]) && !inExcl(j) {
			j--
		}
		if j+1 >= end {
			return ""
		}
		return source[j+1 : end]
	}
	nextCh := func(off int) byte {
		for j := off; j < len(source); j++ {
			if !isWS(source[j]) {
				return source[j]
			}
		}
		return 0
	}

	for i := 0; i < len(source); i++ {
		if inExcl(i) || inDecl(i) {
			continue
		}
		if !idS(source[i]) {
			continue
		}
		a := i
		for i < len(source) && idP(source[i]) && !inExcl(i) {
			i++
		}
		b := i
		w := source[a:b]
		if kws[w] {
			continue
		}
		ln, ls := lineAt(a)
		nc := nextCh(b)
		pw := prevWord(a)

		if !kws[w] && (nc == '-' || nc == ':') {
			arrow := false
			if nc == '-' {
				q := b
				for q < len(source) && isWS(source[q]) {
					q++
				}
				arrow = q+1 < len(source) && source[q] == '-' && source[q+1] == '>'
			}
			isEnhFor := nc == ':' && pw != "" && idS(pw[0]) && typeWord(pw)
			if arrow || isEnhFor {
				et := ""
				if isEnhFor {
					et = "T|" + ql(pw, r.Package, r.imports)
				} else if t, ok := r.elemTypes[prevRecv(source, a)]; ok {
					et = t
				}
				if et != "" {
					vnt[w] = et
				}
				l := &model.Location{Path: p, Line: ln, Character: u16col(source, ls, a),
					EndLine: ln, EndCharacter: u16col(source, ls, b), Kind: "variable", Label: w}
				r.Occs = append(r.Occs, &model.Occ{Start: a, End: b, Location: l,
					Role: "declaration", Name: w, TargetKey: "L|" + w})
				continue
			}
		}

		if (nc == '=' || nc == ';' || nc == ',' || nc == ')') && pw != "" &&
			idS(pw[0]) && typeWord(pw) {
			vnt[w] = "T|" + ql(pw, r.Package, r.imports)
			l := &model.Location{Path: p, Line: ln, Character: u16col(source, ls, a),
				EndLine: ln, EndCharacter: u16col(source, ls, b), Kind: "variable", Label: w}
			r.Occs = append(r.Occs, &model.Occ{Start: a, End: b, Location: l,
				Role: "declaration", Name: w, TargetKey: "L|" + w})
			continue
		}

		qk, qn := "", ""
		isMember := false
		rcName, rcOwner := "", ""
		j := a - 1
		for j >= 0 && isWS(source[j]) {
			j--
		}
		// Member access via '.' or method reference via '::'
		if j >= 1 && source[j] == ':' && source[j-1] == ':' && !inExcl(j) {
			isMember = true
			j -= 2
			for j >= 0 && isWS(source[j]) {
				j--
			}
		} else if j >= 0 && source[j] == '.' && !inExcl(j) {
			isMember = true
			j--
			for j >= 0 && isWS(source[j]) {
				j--
			}
		}
		if isMember {
			if j >= 0 && source[j] == ')' {
				// chained call — take last call name / new Type
				k := j
				depth := 0
				p0 := k
				for p0 >= 0 {
					switch source[p0] {
					case ')':
						depth++
					case '(':
						depth--
						if depth == 0 {
							goto matched
						}
					}
					p0--
				}
			matched:
				if p0 >= 0 {
					e := p0 - 1
					for e >= 0 && isWS(source[e]) {
						e--
					}
					if e >= 0 && idP(source[e]) {
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
							qk = "T|" + ql(callName, r.Package, r.imports)
						} else {
							rcName = callName
							d := e
							for d >= 0 && isWS(source[d]) {
								d--
							}
							if d >= 0 && source[d] == '.' {
								d--
								for d >= 0 && isWS(source[d]) {
									d--
								}
								if d >= 0 && idP(source[d]) {
									s2 := d
									for s2 >= 0 && (idP(source[s2]) || source[s2] == '.') {
										s2--
									}
									qtext := source[s2+1 : d+1]
									if i := strings.LastIndex(qtext, "."); i >= 0 {
										rcOwner = "T|" + ql(qtext[:i], r.Package, r.imports)
									} else if t, ok := vnt[qtext]; ok {
										rcOwner = t
									} else {
										rcOwner = "T|" + ql(qtext, r.Package, r.imports)
									}
								}
							}
						}
					}
				}
			} else if j >= 0 && idS(source[j]) {
				st := j
				for j >= 0 && idP(source[j]) && !inExcl(j) {
					j--
				}
				base := source[j+1 : st+1]
				qn = base
				if base == "this" || base == "super" {
					qk = r.innermostType(a)
				} else if t, ok := vnt[base]; ok {
					qk = t
				} else if len(base) > 0 && base[0] >= 'A' && base[0] <= 'Z' {
					// Type.member / Type::member (Result.success, Foo::bar)
					qk = "T|" + ql(base, r.Package, r.imports)
				}
			}
		}

		owner := r.innermostType(a)
		l := &model.Location{Path: p, Line: ln, Character: u16col(source, ls, a),
			EndLine: ln, EndCharacter: u16col(source, ls, b), Kind: "reference", Label: w}
		r.Occs = append(r.Occs, &model.Occ{Start: a, End: b, Location: l, Role: "reference",
			Name: w, QualifierKey: qk, QualifierName: qn, MemberRef: isMember,
			OwnerTypeKey: owner, RecvCallName: rcName, RecvCallOwner: rcOwner})
	}

	// Unique local keys + bind references to the in-scope declaration only.
	bindLocalScopes(source, r.Occs, inExcl)
}

func prevRecv(src string, lamA int) string {
	k := lamA - 1
	for k >= 0 && isWS(src[k]) {
		k--
	}
	if k < 0 || src[k] != '(' {
		return ""
	}
	e := k - 1
	for e >= 0 && isWS(src[e]) {
		e--
	}
	if e < 0 || !idP(src[e]) {
		return ""
	}
	for e >= 0 && idP(src[e]) {
		e--
	}
	d := e
	if d < 0 || src[d] != '.' {
		return ""
	}
	d--
	for d >= 0 && isWS(src[d]) {
		d--
	}
	if d < 0 || !idP(src[d]) {
		return ""
	}
	ds := d
	for d >= 0 && idP(src[d]) {
		d--
	}
	return src[d+1 : ds+1]
}

func newLineIndex(source string) func(off int) (int, int) {
	ln, ls, cur := 0, 0, 0
	return func(off int) (int, int) {
		if off < cur {
			ln, ls, cur = 0, 0, 0
		}
		for cur < off && cur < len(source) {
			if source[cur] == '\n' {
				ln++
				ls = cur + 1
			}
			cur++
		}
		return ln, ls
	}
}
