package mybatis

import (
	"defhop/internal/model"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

type Statement struct {
	ID       string
	Location *model.Location
	TagStart int
	TagEnd   int
}

type TypeToken struct {
	Value      string
	Start, End int
}

type Mapper struct {
	Path       string
	Namespace  string
	NsStart    int
	NsEnd      int
	Statements []*Statement
	TypeTokens []*TypeToken
	Text       string
}

var (
	tagPattern    = regexp.MustCompile(`<([A-Za-z_][A-Za-z0-9_.:-]*)\b([^>]*?)>`)
	attrDQPattern = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_.:-]*)\s*=\s*"(.*?)"`)
	attrSQPattern = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_.:-]*)\s*=\s*'(.*?)'`)
	typeTokenRe   = regexp.MustCompile(`[A-Za-z_$][A-Za-z0-9_$]*(?:\.[A-Za-z_$][A-Za-z0-9_$]*)*`)
	statementTags = map[string]bool{
		"select": true, "insert": true, "update": true, "delete": true,
		"sql": true, "selectkey": true, "resultmap": true, "parametermap": true,
	}
	typeAttrs = map[string]bool{
		"resulttype": true, "parametertype": true, "javatype": true,
		"oftype": true, "type": true, "namespace": true,
	}
)

func ParseMapper(filePath, text string) *Mapper {
	if !strings.Contains(text, "<mapper") {
		return nil
	}
	m := &Mapper{Path: filepath.ToSlash(filePath), Text: text}
	for _, tm := range tagPattern.FindAllStringSubmatchIndex(text, -1) {
		tagName := text[tm[2]:tm[3]]
		attrs := parseAttrs(text[tm[4]:tm[5]], tm[4])
		lower := strings.ToLower(tagName)
		if statementTags[lower] {
			if id := findAttr(attrs, "id"); id != nil {
				m.Statements = append(m.Statements, &Statement{
					ID: id.Value, Location: locate(text, id.ValueStart, id.ValueEnd, m.Path),
					TagStart: tm[0], TagEnd: tm[1],
				})
			}
		}
		if lower == "mapper" && m.Namespace == "" {
			if ns := findAttr(attrs, "namespace"); ns != nil {
				m.Namespace = strings.TrimSpace(ns.Value)
				m.NsStart, m.NsEnd = ns.ValueStart, ns.ValueEnd
			}
		}
		for _, a := range attrs {
			if !typeAttrs[strings.ToLower(a.Name)] {
				continue
			}
			for _, mm := range typeTokenRe.FindAllStringIndex(a.Value, -1) {
				c := a.Value[mm[0]:mm[1]]
				if !looksLikeType(c) {
					continue
				}
				m.TypeTokens = append(m.TypeTokens, &TypeToken{
					Value: c, Start: a.ValueStart + mm[0], End: a.ValueStart + mm[1],
				})
			}
		}
	}
	if m.Namespace == "" {
		return nil
	}
	return m
}

type xmlAttr struct {
	Name                 string
	Value                string
	ValueStart, ValueEnd int
}

func parseAttrs(region string, regionStart int) []xmlAttr {
	matches := attrDQPattern.FindAllStringSubmatchIndex(region, -1)
	matches = append(matches, attrSQPattern.FindAllStringSubmatchIndex(region, -1)...)
	var attrs []xmlAttr
	for _, m := range matches {
		attrs = append(attrs, xmlAttr{
			Name: region[m[2]:m[3]], Value: region[m[4]:m[5]],
			ValueStart: regionStart + m[4], ValueEnd: regionStart + m[5],
		})
	}
	return attrs
}

func findAttr(attrs []xmlAttr, name string) *xmlAttr {
	for i := range attrs {
		if strings.EqualFold(attrs[i].Name, name) {
			return &attrs[i]
		}
	}
	return nil
}

func looksLikeType(v string) bool {
	if v == "" || strings.HasPrefix(v, "java.") || strings.HasPrefix(v, "javax.") {
		return false
	}
	return strings.Contains(v, ".") || (v[0] >= 'A' && v[0] <= 'Z') || strings.Contains(v, "$")
}

func locate(src string, begin, end int, path string) *model.Location {
	if begin < 0 {
		begin = 0
	}
	if end > len(src) {
		end = len(src)
	}
	if begin >= end {
		end = begin + 1
		if end > len(src) {
			end = len(src)
		}
	}
	line, ls := 0, 0
	for i := 0; i < begin && i < len(src); i++ {
		if src[i] == '\n' {
			line++
			ls = i + 1
		}
	}
	return &model.Location{
		Path: path, Line: line, Character: u16col(src, ls, begin),
		EndLine: line, EndCharacter: u16col(src, ls, end), Kind: "xml",
	}
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

func ExtractMapperLocations(yml string) []string {
	var patterns []string
	section := ""
	for _, raw := range strings.Split(yml, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " \t"))
		i := strings.Index(trimmed, ":")
		if i < 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:i])
		value := strings.TrimSpace(trimmed[i+1:])
		if j := strings.Index(value, "#"); j >= 0 {
			value = strings.TrimSpace(value[:j])
		}
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		if indent == 0 {
			if key == "mybatis" || key == "mybatis-plus" {
				section = key
			} else {
				section = ""
			}
			continue
		}
		if section != "" && key == "mapper-locations" {
			for _, p := range strings.Split(value, ",") {
				p = strings.TrimSpace(p)
				if p != "" {
					patterns = append(patterns, p)
				}
			}
		}
	}
	return patterns
}

func StripClasspathPrefix(pattern string) string {
	for _, prefix := range []string{"classpath*:", "classpath:", "file:"} {
		if strings.HasPrefix(pattern, prefix) {
			return strings.TrimPrefix(pattern, prefix)
		}
	}
	return pattern
}

func ResolvePattern(root, pattern string) []string {
	pattern = strings.TrimPrefix(pattern, "/")
	if pattern == "" {
		return nil
	}
	full := filepath.Join(root, filepath.FromSlash(pattern))
	if !strings.Contains(pattern, "**") {
		matches, _ := filepath.Glob(full)
		return matches
	}
	var out []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		if matchAnt(pattern, filepath.ToSlash(rel)) {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func matchAnt(pattern, path string) bool {
	return matchAntSeg(strings.Split(pattern, "/"), strings.Split(path, "/"))
}

func matchAntSeg(pat, seg []string) bool {
	if len(pat) == 0 {
		return len(seg) == 0
	}
	if pat[0] == "**" {
		for skip := 0; skip <= len(seg); skip++ {
			if matchAntSeg(pat[1:], seg[skip:]) {
				return true
			}
		}
		return false
	}
	if len(seg) == 0 {
		return false
	}
	ok, err := filepath.Match(pat[0], seg[0])
	if err != nil || !ok {
		return false
	}
	return matchAntSeg(pat[1:], seg[1:])
}

var sqlAnnotations = map[string]bool{
	"select": true, "insert": true, "update": true, "delete": true,
	"selectprovider": true, "insertprovider": true,
	"updateprovider": true, "deleteprovider": true,
}

func HasSQLAnnotation(anns []string) bool {
	for _, a := range anns {
		base := a
		if i := strings.IndexByte(a, ':'); i >= 0 {
			base = a[:i]
		}
		if sqlAnnotations[strings.ToLower(base)] {
			return true
		}
	}
	return false
}

func SQLAnnotationLabel(anns []string) string {
	for _, a := range anns {
		base := a
		if i := strings.IndexByte(a, ':'); i >= 0 {
			base = a[:i]
		}
		low := strings.ToLower(base)
		if sqlAnnotations[low] {
			return "@" + base
		}
	}
	return "@SQL"
}
