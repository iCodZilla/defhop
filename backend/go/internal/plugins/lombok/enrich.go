package lombok

import (
	"defhop/internal/model"
	"strings"
	"unicode"
)

// Enrich appends synthetic Lombok accessors / builder / log into the FileSlice
// so they participate in SymTab, MethodIdx, Outline, and Refs resolution.
func Enrich(fd *model.FileSlice) {
	if fd == nil || len(fd.Symbols) == 0 {
		return
	}
	existing := map[string]bool{}
	for _, s := range fd.Symbols {
		if s != nil && s.Key != "" {
			existing[s.Key] = true
		}
	}
	var extra []*model.Symbol
	for _, sym := range fd.Symbols {
		if sym == nil || sym.Kind != "type" {
			continue
		}
		anns := annSet(sym.Annotations)
		if len(anns) == 0 {
			continue
		}
		wantGetter := anns["Data"] || anns["Getter"] || anns["Value"]
		wantSetter := anns["Data"] || anns["Setter"]
		if wantGetter || wantSetter {
			for _, f := range fd.Symbols {
				if f == nil || f.Kind != "field" || f.OwnerKey != sym.Key {
					continue
				}
				if strings.HasPrefix(f.Name, "$") || f.Name == "log" && anns["Slf4j"] {
					continue
				}
				if wantGetter {
					gn := getterName(f)
					gk := methodKey(sym.Key, gn)
					if !existing[gk] {
						extra = append(extra, &model.Symbol{
							Key: gk, Kind: "method", Name: gn, OwnerKey: sym.Key,
							ReturnType:  f.TypeKey,
							Declaration: declOr(f.Declaration, sym.Declaration),
							Annotations: []string{"lombok"},
						})
						existing[gk] = true
					}
				}
				if wantSetter && !anns["Value"] {
					sn := "set" + exportName(f.Name)
					sk := methodKey(sym.Key, sn)
					if !existing[sk] {
						extra = append(extra, &model.Symbol{
							Key: sk, Kind: "method", Name: sn, OwnerKey: sym.Key,
							Declaration: declOr(f.Declaration, sym.Declaration),
							Annotations: []string{"lombok"},
						})
						existing[sk] = true
					}
				}
			}
		}
		if anns["Builder"] || anns["SuperBuilder"] {
			bk := methodKey(sym.Key, "builder")
			if !existing[bk] {
				extra = append(extra, &model.Symbol{
					Key: bk, Kind: "method", Name: "builder", OwnerKey: sym.Key,
					ReturnType:  sym.Key + "$Builder",
					Declaration: sym.Declaration,
					Annotations: []string{"lombok"},
				})
				existing[bk] = true
			}
			buildK := methodKey(sym.Key+"$Builder", "build")
			if !existing[buildK] {
				// Lightweight placeholder so Customer.builder().build() can resolve build.
				extra = append(extra, &model.Symbol{
					Key: buildK, Kind: "method", Name: "build", OwnerKey: sym.Key + "$Builder",
					ReturnType:  sym.Key,
					Declaration: sym.Declaration,
					Annotations: []string{"lombok"},
				})
				existing[buildK] = true
			}
		}
		simple := typeSimpleName(sym)
		collectFields := func(requiredOnly bool) []*model.Symbol {
			var fields []*model.Symbol
			for _, f := range fd.Symbols {
				if f == nil || f.Kind != "field" || f.OwnerKey != sym.Key {
					continue
				}
				if strings.HasPrefix(f.Name, "$") || f.Name == "log" {
					continue
				}
				if requiredOnly && !fieldRequired(f) {
					continue
				}
				fields = append(fields, f)
			}
			return fields
		}
		addCtorSig := func(sig string) {
			ck := ctorKey(sym.Key, simple, sig)
			if existing[ck] {
				return
			}
			extra = append(extra, &model.Symbol{
				Key: ck, Kind: "constructor", Name: simple, OwnerKey: sym.Key,
				Declaration: sym.Declaration,
				Annotations: []string{"lombok"},
			})
			existing[ck] = true
		}
		if anns["NoArgsConstructor"] {
			addCtorSig("")
		}
		if anns["RequiredArgsConstructor"] {
			addCtorSig(ctorParamSig(collectFields(true)))
		}
		if anns["AllArgsConstructor"] {
			addCtorSig(ctorParamSig(collectFields(false)))
		}
		if anns["Slf4j"] {
			fk := "F|" + sym.Key + "|log"
			if !existing[fk] {
				extra = append(extra, &model.Symbol{
					Key: fk, Kind: "field", Name: "log", OwnerKey: sym.Key,
					TypeKey:     "T|org.slf4j.Logger",
					Declaration: sym.Declaration,
					Annotations: []string{"lombok"},
				})
				existing[fk] = true
			}
		}
	}
	if len(extra) > 0 {
		fd.Symbols = append(fd.Symbols, extra...)
	}
}

func annSet(anns []string) map[string]bool {
	out := map[string]bool{}
	for _, a := range anns {
		if i := strings.LastIndexByte(a, '.'); i >= 0 {
			a = a[i+1:]
		}
		out[a] = true
	}
	return out
}

func methodKey(owner, name string) string {
	return "M|" + owner + "|" + name + "|"
}

func ctorKey(owner, name, paramsSig string) string {
	return "M|" + owner + "|" + name + "|" + paramsSig
}

func typeSimpleName(sym *model.Symbol) string {
	if sym == nil {
		return ""
	}
	n := sym.Name
	if i := strings.LastIndexAny(n, ".$"); i >= 0 {
		n = n[i+1:]
	}
	if n == "" {
		n = strings.TrimPrefix(sym.Key, "T|")
		if i := strings.LastIndexAny(n, ".$"); i >= 0 {
			n = n[i+1:]
		}
	}
	return n
}

func fieldRequired(f *model.Symbol) bool {
	if f == nil {
		return false
	}
	for _, a := range f.Annotations {
		base := a
		if i := strings.LastIndexByte(a, '.'); i >= 0 {
			base = a[i+1:]
		}
		if base == "final" || base == "NonNull" || base == "NotNull" || base == "Nonnull" {
			return true
		}
	}
	return false
}

func ctorParamSig(fields []*model.Symbol) string {
	if len(fields) == 0 {
		return ""
	}
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		t := strings.TrimPrefix(f.TypeKey, "T|")
		if t == "" {
			t = "Object"
		}
		parts = append(parts, t)
	}
	return strings.Join(parts, ",")
}

func getterName(f *model.Symbol) string {
	n := f.Name
	// Primitive boolean → isX; Boolean boxed → getX (Lombok default).
	if strings.TrimPrefix(f.TypeKey, "T|") == "boolean" {
		if strings.HasPrefix(n, "is") && len(n) > 2 && unicode.IsUpper(rune(n[2])) {
			return n
		}
		return "is" + exportName(n)
	}
	return "get" + exportName(n)
}

func exportName(n string) string {
	if n == "" {
		return n
	}
	r := []rune(n)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func declOr(a, b *model.Location) *model.Location {
	if a != nil {
		return a
	}
	return b
}
