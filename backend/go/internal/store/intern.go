package store

import "defhop/internal/model"

// InternFileSlice deduplicates hot strings on one file slice.
// Not used on the cold INIT / PutFile hot path (too many Occ fields); snapshot
// build already interns SymTab / Refs keys. Kept for optional offline compaction.
func InternFileSlice(fd *model.FileSlice) {
	if fd == nil {
		return
	}
	fd.Path = Intern(fd.Path)
	fd.Package = Intern(fd.Package)
	fd.ContentHash = Intern(fd.ContentHash)
	for i, imp := range fd.Imports {
		fd.Imports[i] = Intern(imp)
	}
	for _, sym := range fd.Symbols {
		InternSymbol(sym)
	}
	for _, o := range fd.Occs {
		InternOcc(o)
	}
}

// InternSymbol interns keys and common identity fields on a symbol.
func InternSymbol(sym *model.Symbol) {
	if sym == nil {
		return
	}
	sym.Key = Intern(sym.Key)
	sym.Kind = Intern(sym.Kind)
	sym.Name = Intern(sym.Name)
	sym.OwnerKey = Intern(sym.OwnerKey)
	sym.TypeKey = Intern(sym.TypeKey)
	sym.ElemType = Intern(sym.ElemType)
	sym.ReturnType = Intern(sym.ReturnType)
	sym.ExtendsKey = Intern(sym.ExtendsKey)
	sym.ConfigPrefix = Intern(sym.ConfigPrefix)
	sym.HttpPath = Intern(sym.HttpPath)
	sym.HttpMethod = Intern(sym.HttpMethod)
	sym.FeignService = Intern(sym.FeignService)
	for i, a := range sym.Annotations {
		sym.Annotations[i] = Intern(a)
	}
	for i, sk := range sym.SuperKeys {
		sym.SuperKeys[i] = Intern(sk)
	}
	for i, a := range sym.ExtendsArgs {
		sym.ExtendsArgs[i] = Intern(a)
	}
	for i, k := range sym.ConfigKeys {
		sym.ConfigKeys[i] = Intern(k)
	}
	for i, imp := range sym.Imports {
		sym.Imports[i] = Intern(imp)
	}
	InternLocation(sym.Declaration)
}

// InternOcc interns occurrence identity strings.
func InternOcc(o *model.Occ) {
	if o == nil {
		return
	}
	o.Role = Intern(o.Role)
	o.Name = Intern(o.Name)
	o.TargetKey = Intern(o.TargetKey)
	o.OwnerTypeKey = Intern(o.OwnerTypeKey)
	o.QualifierKey = Intern(o.QualifierKey)
	o.QualifierName = Intern(o.QualifierName)
	o.PropertyName = Intern(o.PropertyName)
	o.RecvCallName = Intern(o.RecvCallName)
	o.RecvCallOwner = Intern(o.RecvCallOwner)
	o.TypeKey = Intern(o.TypeKey)
	o.ElemType = Intern(o.ElemType)
	InternLocation(o.Location)
}

// InternLocation interns path/kind/label on a location.
func InternLocation(loc *model.Location) {
	if loc == nil {
		return
	}
	loc.Path = Intern(loc.Path)
	loc.Kind = Intern(loc.Kind)
	loc.Label = Intern(loc.Label)
}
