package spring

import (
	"defhop/internal/java"
	"defhop/internal/model"
	"path/filepath"
	"strings"
	"unicode"
)

// ConfigHit is one property key occurrence (config file).
type ConfigHit struct {
	Key string
	Loc *model.Location
}

// ParseConfigFile extracts dotted property keys from yml/yaml/properties.
func ParseConfigFile(path, text string) []ConfigHit {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".properties"):
		return parseProperties(path, text)
	case strings.HasSuffix(lower, ".yml"), strings.HasSuffix(lower, ".yaml"):
		return parseYAML(path, text)
	default:
		return nil
	}
}

func makeLoc(path string, line, col, endCol int, label string) *model.Location {
	return &model.Location{
		Path: filepath.ToSlash(path),
		Line: line, Character: col,
		EndLine: line, EndCharacter: endCol,
		Label: label,
	}
}

func parseProperties(path, text string) []ConfigHit {
	var out []ConfigHit
	lineNo := 0
	rest := text
	for len(rest) > 0 {
		nl := strings.IndexByte(rest, '\n')
		var raw string
		if nl < 0 {
			raw = rest
			rest = ""
		} else {
			raw = rest[:nl]
			rest = rest[nl+1:]
		}
		if strings.HasSuffix(raw, "\r") {
			raw = raw[:len(raw)-1]
		}
		trimmed := strings.TrimSpace(raw)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "!") {
			sep := -1
			for i, r := range trimmed {
				if r == '=' || r == ':' {
					sep = i
					break
				}
			}
			if sep > 0 {
				key := strings.TrimSpace(trimmed[:sep])
				if key != "" {
					keyPos := strings.Index(raw, key)
					if keyPos < 0 {
						keyPos = 0
					}
					out = append(out, ConfigHit{
						Key: key,
						Loc: makeLoc(path, lineNo, keyPos, keyPos+len(key), key),
					})
				}
			}
		}
		lineNo++
	}
	return out
}

func parseYAML(path, text string) []ConfigHit {
	type frame struct {
		indent int
		key    string
	}
	var stack []frame
	var out []ConfigHit
	lineNo := 0
	rest := text
	for len(rest) > 0 {
		nl := strings.IndexByte(rest, '\n')
		var raw string
		if nl < 0 {
			raw = rest
			rest = ""
		} else {
			raw = rest[:nl]
			rest = rest[nl+1:]
		}
		if strings.HasSuffix(raw, "\r") {
			raw = raw[:len(raw)-1]
		}
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			lineNo++
			continue
		}
		indent := 0
		for indent < len(raw) && (raw[indent] == ' ' || raw[indent] == '\t') {
			indent++
		}
		content := trimmed
		if strings.HasPrefix(content, "- ") {
			content = strings.TrimSpace(content[2:])
			indent += 2
		}
		colon := strings.Index(content, ":")
		if colon < 0 {
			lineNo++
			continue
		}
		key := strings.TrimSpace(content[:colon])
		if key == "" || !isYAMLKey(key) {
			lineNo++
			continue
		}
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		full := key
		if len(stack) > 0 {
			full = stack[len(stack)-1].key + "." + key
		}
		keyPos := strings.Index(raw, key)
		if keyPos < 0 {
			keyPos = indent
		}
		out = append(out, ConfigHit{
			Key: full,
			Loc: makeLoc(path, lineNo, keyPos, keyPos+len(key), full),
		})
		value := strings.TrimSpace(content[colon+1:])
		if j := strings.Index(value, " #"); j >= 0 {
			value = strings.TrimSpace(value[:j])
		}
		if value == "" || value == "|" || value == ">" {
			stack = append(stack, frame{indent: indent, key: full})
		}
		lineNo++
	}
	return out
}

func isYAMLKey(s string) bool {
	if s == "" || s == "-" {
		return false
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

// KeyVariants returns Spring relaxed-binding lookup variants for a property key.
// bank-import.enabled ↔ bankImport.enabled ↔ bank_import.enabled
func KeyVariants(key string) []string {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(k string) {
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, k)
	}
	add(key)

	parts := strings.Split(key, ".")
	mapParts := func(fn func(string) string) string {
		cp := make([]string, len(parts))
		for i, p := range parts {
			cp[i] = fn(p)
		}
		return strings.Join(cp, ".")
	}
	add(mapParts(java.ToKebab))
	add(mapParts(java.ToCamel))
	add(mapParts(func(s string) string { return java.ToKebab(java.ToCamel(s)) }))
	add(mapParts(func(s string) string {
		return strings.ReplaceAll(strings.ReplaceAll(s, "-", "_"), ".", "_")
	}))
	add(mapParts(func(s string) string {
		return strings.ReplaceAll(s, "_", "-")
	}))
	if n := len(parts); n > 0 {
		last := parts[n-1]
		cp := append([]string(nil), parts...)
		cp[n-1] = java.ToKebab(last)
		add(strings.Join(cp, "."))
		cp[n-1] = java.ToCamel(last)
		add(strings.Join(cp, "."))
	}
	return out
}

// IsConfigPath reports Spring-style config files we navigate.
// Includes application*/bootstrap* and any yml/yaml/properties under resources/.
func IsConfigPath(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	if !(strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml") ||
		strings.HasSuffix(lower, ".properties")) {
		return false
	}
	base := strings.ToLower(filepath.Base(path))
	if strings.HasPrefix(base, "application") || strings.HasPrefix(base, "bootstrap") {
		return true
	}
	return strings.Contains(lower, "/resources/")
}

// ResolveConfigKeyAt finds the dotted key under the cursor in a config file.
func ResolveConfigKeyAt(path, text string, offset int) string {
	hits := ParseConfigFile(path, text)
	var best *ConfigHit
	bestSpan := 1 << 30
	for i := range hits {
		h := &hits[i]
		if h.Loc == nil {
			continue
		}
		a := java.OffsetAt(text, h.Loc.Line, h.Loc.Character)
		b := java.OffsetAt(text, h.Loc.EndLine, h.Loc.EndCharacter)
		if offset >= a && offset <= b {
			span := b - a
			if best == nil || span < bestSpan {
				best = h
				bestSpan = span
			}
		}
	}
	if best != nil {
		return best.Key
	}
	line, _ := offsetLineCol(text, offset)
	var nearest *ConfigHit
	for i := range hits {
		h := &hits[i]
		if h.Loc != nil && h.Loc.Line == line {
			nearest = h
		}
	}
	if nearest != nil {
		return nearest.Key
	}
	return ""
}

func offsetLineCol(text string, offset int) (line, col int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(text) {
		offset = len(text)
	}
	for i := 0; i < offset; i++ {
		if text[i] == '\n' {
			line++
			col = 0
		} else {
			col++
		}
	}
	return line, col
}
