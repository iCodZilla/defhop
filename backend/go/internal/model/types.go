package model

// Location is a 0-based VSCode range.
type Location struct {
	Path         string `json:"path"`
	Line         int    `json:"line"`
	Character    int    `json:"character"`
	EndLine      int    `json:"endLine"`
	EndCharacter int    `json:"endCharacter"`
	Kind         string `json:"kind,omitempty"`
	Label        string `json:"label,omitempty"`
}

// Symbol is a declared entity.
// Keys: T|fqn · F|T|name · M|T|name|params · L|{offset}|name (scoped local)
type Symbol struct {
	Key           string
	Kind          string // type|method|constructor|field|variable
	Name          string
	OwnerKey      string
	Declaration   *Location
	Annotations   []string
	SuperKeys     []string
	ExtendsKey    string   // T|... of the extends clause (not implements)
	ExtendsArgs   []string // qualified type args of extends, e.g. ServiceImpl<M,T> -> [M,T]
	InterfaceType bool
	TypeKey       string // fields
	ReturnType    string // methods
	Imports       []string
	ConfigKeys    []string // @Value placeholders / @ConfigurationProperties keys
	ConfigPrefix  string   // @ConfigurationProperties prefix on types
	HttpPath      string   // type: class @RequestMapping; method: full path /a/b
	HttpMethod    string   // GET|POST|PUT|DELETE|PATCH|"" (any)
	FeignService  string   // @FeignClient name/value on types
}

// Occ is a source span that may resolve to a symbol.
type Occ struct {
	Start         int
	End           int
	Location      *Location
	Role          string // declaration|reference|property|config-key
	Name          string
	TargetKey     string
	OwnerTypeKey  string
	QualifierKey  string
	QualifierName string
	PropertyName  string // MapStruct prop or Spring config key
	MemberRef     bool
	RecvCallName  string
	RecvCallOwner string
}

// FileSlice is the indexed view of one source file.
type FileSlice struct {
	Path        string
	Package     string
	Imports     []string
	Symbols     []*Symbol
	Occs        []*Occ
	Gen         uint64 // overlay generation when parsed
	ContentHash string
}

// NavKind selects the navigation command.
type NavKind string

const (
	NavDef  NavKind = "DEF"
	NavRefs NavKind = "REFS"
	NavImpl NavKind = "IMPL"
	NavRoute NavKind = "ROUTE"
)

// NavRequest is a single navigation query.
type NavRequest struct {
	Kind              NavKind
	Path              string
	Text              string // may be empty when overlay already has it
	Line, Character   int
	IncludeDecl       bool
	Gen               uint64
}

// WorkspaceFacts are cheap Detect inputs (no heavy parsing).
type WorkspaceFacts struct {
	Root           string
	JavaFiles      []string
	XMLFiles       []string
	ConfigFiles    []string // application*.yml/yaml
	ResourceRoots  []string
	HasPom         bool
	AnnotationHits map[string]int // simple annotation name -> count (sampled)
	ImportHits     map[string]int // import prefix -> count
}
