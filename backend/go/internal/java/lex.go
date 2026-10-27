package java

import "unicode"

type tt int

const (
	tEOF tt = iota
	tID
	tDOT
	tSEMI
	tCOMMA
	tLP
	tRP
	tLB
	tRB
	tLBR
	tRBR
	tAT
	tSTR
	tNUM
	tASGN
	tLT
	tGT
	tSTAR
)

var kws = map[string]bool{
	"abstract": true, "assert": true, "boolean": true, "break": true, "byte": true,
	"case": true, "catch": true, "char": true, "class": true, "const": true, "continue": true,
	"default": true, "do": true, "double": true, "else": true, "enum": true, "extends": true,
	"false": true, "final": true, "finally": true, "float": true, "for": true, "if": true,
	"implements": true, "import": true, "instanceof": true, "int": true, "interface": true,
	"long": true, "native": true, "new": true, "null": true, "package": true, "private": true,
	"protected": true, "public": true, "return": true, "short": true, "static": true,
	"strictfp": true, "super": true, "switch": true, "synchronized": true, "this": true,
	"throw": true, "throws": true, "transient": true, "true": true, "try": true, "void": true,
	"volatile": true, "while": true, "record": true, "sealed": true, "var": true, "yield": true,
}

type tok struct {
	t    tt
	s    string
	a, b int
}

type iv struct{ a, b int }

type lx struct {
	src     []byte
	pos     int
	regions []iv
}

func lxNew(src string) *lx { return &lx{src: []byte(src)} }

func (z *lx) nx() tok {
	z.skip()
	if z.pos >= len(z.src) {
		return tok{t: tEOF, a: z.pos, b: z.pos}
	}
	a := z.pos
	c := z.src[z.pos]
	if c == '"' || c == '\'' {
		z.pos++
		for z.pos < len(z.src) {
			if z.src[z.pos] == '\\' {
				z.pos += 2
				continue
			}
			if z.src[z.pos] == c {
				z.pos++
				break
			}
			z.pos++
		}
		return tok{t: tSTR, a: a, b: z.pos}
	}
	if c >= '0' && c <= '9' {
		for z.pos < len(z.src) && (isDig(z.src[z.pos]) || z.src[z.pos] == '.' ||
			z.src[z.pos] == 'x' || z.src[z.pos] == 'X' || z.src[z.pos] == 'L' ||
			z.src[z.pos] == 'l' || z.src[z.pos] == 'f' || z.src[z.pos] == 'F' ||
			z.src[z.pos] == 'd' || z.src[z.pos] == 'D' || z.src[z.pos] == 'e' ||
			z.src[z.pos] == 'E' || (z.src[z.pos] >= 'a' && z.src[z.pos] <= 'f') ||
			(z.src[z.pos] >= 'A' && z.src[z.pos] <= 'F')) {
			z.pos++
		}
		return tok{t: tNUM, a: a, b: z.pos}
	}
	if idS(c) {
		z.pos++
		for z.pos < len(z.src) && idP(z.src[z.pos]) {
			z.pos++
		}
		return tok{t: tID, s: string(z.src[a:z.pos]), a: a, b: z.pos}
	}
	z.pos++
	switch c {
	case '.':
		return tok{t: tDOT, s: ".", a: a, b: z.pos}
	case ';':
		return tok{t: tSEMI, a: a, b: z.pos}
	case ',':
		return tok{t: tCOMMA, a: a, b: z.pos}
	case '(':
		return tok{t: tLP, a: a, b: z.pos}
	case ')':
		return tok{t: tRP, a: a, b: z.pos}
	case '{':
		return tok{t: tLB, a: a, b: z.pos}
	case '}':
		return tok{t: tRB, a: a, b: z.pos}
	case '[':
		return tok{t: tLBR, a: a, b: z.pos}
	case ']':
		return tok{t: tRBR, a: a, b: z.pos}
	case '@':
		return tok{t: tAT, a: a, b: z.pos}
	case '=':
		return tok{t: tASGN, a: a, b: z.pos}
	case '<':
		return tok{t: tLT, a: a, b: z.pos}
	case '>':
		return tok{t: tGT, a: a, b: z.pos}
	case '*':
		return tok{t: tSTAR, s: "*", a: a, b: z.pos}
	default:
		return z.nx()
	}
}

func (z *lx) skip() {
	for z.pos < len(z.src) {
		c := z.src[z.pos]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			z.pos++
			continue
		}
		if c == '/' && z.pos+1 < len(z.src) {
			n := z.src[z.pos+1]
			if n == '/' {
				st := z.pos
				z.pos += 2
				for z.pos < len(z.src) && z.src[z.pos] != '\n' {
					z.pos++
				}
				z.regions = append(z.regions, iv{st, z.pos})
				continue
			}
			if n == '*' {
				st := z.pos
				z.pos += 2
				for z.pos+1 < len(z.src) && !(z.src[z.pos] == '*' && z.src[z.pos+1] == '/') {
					z.pos++
				}
				if z.pos+1 < len(z.src) {
					z.pos += 2
				} else {
					z.pos = len(z.src)
				}
				z.regions = append(z.regions, iv{st, z.pos})
				continue
			}
		}
		break
	}
}

func idS(c byte) bool { return c == '_' || c == '$' || unicode.IsLetter(rune(c)) }
func idP(c byte) bool { return idS(c) || (c >= '0' && c <= '9') }
func isDig(c byte) bool { return c >= '0' && c <= '9' }

func isTypeKW(s string) bool {
	return s == "class" || s == "interface" || s == "enum" || s == "record"
}

func isMod(s string) bool {
	switch s {
	case "public", "private", "protected", "static", "abstract", "final",
		"synchronized", "transient", "volatile", "native", "strictfp",
		"default", "sealed":
		return true
	}
	return false
}

var primTypes = map[string]bool{
	"boolean": true, "byte": true, "char": true, "double": true,
	"float": true, "int": true, "long": true, "short": true, "var": true,
}

func typeWord(w string) bool {
	if !kws[w] {
		return true
	}
	return primTypes[w]
}
