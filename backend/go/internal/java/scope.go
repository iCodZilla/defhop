package java

import (
	"fmt"
	"defhop/internal/model"
	"strings"
)

// bindLocalScopes assigns unique keys L|{declOffset}|{name} to locals and
// wires non-member references to the declaration that is in scope (nearest
// enclosing block). Same-name locals in other methods/lambdas stay separate.
func bindLocalScopes(source string, occs []*model.Occ, inExcl func(int) bool) {
	if len(source) == 0 || len(occs) == 0 {
		return
	}
	depths := braceDepths(source, inExcl)

	type localDecl struct {
		occ *model.Occ
		end int
		key string
	}
	var locals []localDecl
	for _, o := range occs {
		if o == nil || o.Role != "declaration" {
			continue
		}
		if !strings.HasPrefix(o.TargetKey, "L|") {
			continue
		}
		key := fmt.Sprintf("L|%d|%s", o.Start, o.Name)
		o.TargetKey = key
		d := 0
		if o.Start < len(depths) {
			d = depths[o.Start]
		}
		end := len(source)
		for i := o.Start + 1; i < len(source); i++ {
			if depths[i] < d {
				end = i
				break
			}
		}
		locals = append(locals, localDecl{occ: o, end: end, key: key})
	}
	if len(locals) == 0 {
		return
	}

	for _, o := range occs {
		if o == nil || o.Role != "reference" || o.MemberRef {
			continue
		}
		// Already bound (imports, typed type-refs, etc.)
		if o.TargetKey != "" {
			continue
		}
		// name(...) is a method/ctor call — never bind to a same-named local.
		if isCallSite(source, o.End) {
			continue
		}
		var best *localDecl
		for i := range locals {
			ld := &locals[i]
			if ld.occ.Name != o.Name {
				continue
			}
			if ld.occ.Start <= o.Start && o.Start < ld.end {
				if best == nil || ld.occ.Start > best.occ.Start {
					best = ld
				}
			}
		}
		if best != nil {
			o.TargetKey = best.key
		}
	}
}

// isCallSite reports whether the identifier ending at end is followed by '('.
func isCallSite(source string, end int) bool {
	if end < 0 || end > len(source) {
		return false
	}
	i := end
	for i < len(source) && isWS(source[i]) {
		i++
	}
	return i < len(source) && source[i] == '('
}

// braceDepths[i] is the { } nesting depth at byte offset i (before processing i).
func braceDepths(source string, inExcl func(int) bool) []int {
	d := make([]int, len(source)+1)
	depth := 0
	for i := 0; i < len(source); i++ {
		d[i] = depth
		if inExcl != nil && inExcl(i) {
			continue
		}
		switch source[i] {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		}
	}
	d[len(source)] = depth
	return d
}

// LocalDeclLocation returns the declaration span for a scoped local key.
func LocalDeclLocation(fd *model.FileSlice, key string) *model.Location {
	if fd == nil || key == "" {
		return nil
	}
	for _, o := range fd.Occs {
		if o != nil && o.Role == "declaration" && o.TargetKey == key && o.Location != nil {
			return o.Location
		}
	}
	return nil
}

// LocalRefs returns references (and optionally the declaration) for one scoped local.
// Never matches by bare name — only by TargetKey — so other methods' same-named
// locals are excluded.
func LocalRefs(fd *model.FileSlice, key string, includeDecl bool) []*model.Location {
	if fd == nil || !strings.HasPrefix(key, "L|") {
		return nil
	}
	var out []*model.Location
	for _, o := range fd.Occs {
		if o == nil || o.TargetKey != key || o.Location == nil {
			continue
		}
		if o.Role == "declaration" && !includeDecl {
			continue
		}
		out = append(out, o.Location)
	}
	return out
}

// IsLocalKey reports whether key identifies a file-local variable.
func IsLocalKey(key string) bool {
	return strings.HasPrefix(key, "L|")
}

// scopedLocalAt finds the nearest local declaration named name that is in
// scope at offset (still inside the declaring { } block).
func scopedLocalAt(fd *model.FileSlice, name string, offset int, source string) *model.Occ {
	if fd == nil || name == "" {
		return nil
	}
	var candidates []*model.Occ
	for _, o := range fd.Occs {
		if o == nil || o.Role != "declaration" || o.Name != name || !IsLocalKey(o.TargetKey) {
			continue
		}
		if o.Start <= offset {
			candidates = append(candidates, o)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	if source == "" {
		// Nearest declaration before cursor (bindLocalScopes usually already set TargetKey).
		best := candidates[0]
		for _, o := range candidates[1:] {
			if o.Start > best.Start {
				best = o
			}
		}
		return best
	}
	depths := braceDepths(source, nil)
	var best *model.Occ
	for _, o := range candidates {
		if o.Start >= len(depths) {
			continue
		}
		d := depths[o.Start]
		inScope := true
		limit := offset
		if limit >= len(depths) {
			limit = len(depths) - 1
		}
		for i := o.Start + 1; i <= limit; i++ {
			if depths[i] < d {
				inScope = false
				break
			}
		}
		if !inScope {
			continue
		}
		if best == nil || o.Start > best.Start {
			best = o
		}
	}
	return best
}
