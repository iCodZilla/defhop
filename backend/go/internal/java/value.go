package java

import (
	"defhop/internal/model"
	"strings"
)

// addValueConfigOccs scans the annotation region for @Value string literals and
// records navigable occurrences for each ${property.key} (supports :default).
func (r *parseResult) addValueConfigOccs(p, source string, annStart, annEnd int, field *model.Symbol) {
	for _, hit := range ScanValueKeys(source, annStart, annEnd) {
		loc := locAt(p, source, hit.Start, hit.End, "config-key", hit.Key)
		r.Occs = append(r.Occs, &model.Occ{
			Start: hit.Start, End: hit.End, Location: loc, Role: "config-key",
			Name: hit.Key, PropertyName: hit.Key,
		})
		// Tight per-segment occs so clicking "enabled" in bank-import.enabled works.
		for _, seg := range hit.Segments {
			sl := locAt(p, source, seg.Start, seg.End, "config-key", hit.Key)
			r.Occs = append(r.Occs, &model.Occ{
				Start: seg.Start, End: seg.End, Location: sl, Role: "config-key",
				Name: seg.Name, PropertyName: hit.Key,
			})
		}
		if field != nil {
			field.ConfigKeys = appendUniqueStr(field.ConfigKeys, hit.Key)
		}
	}
}

// ValueKeyHit is one placeholder key span (and optional dotted segments).
type ValueKeyHit struct {
	Key      string
	Start    int
	End      int
	Segments []struct {
		Name       string
		Start, End int
	}
}

// ScanValueKeys finds ${key} / ${key:default} (incl. SpEL-wrapped) in [annStart,annEnd).
func ScanValueKeys(source string, annStart, annEnd int) []ValueKeyHit {
	if annStart < 0 || annEnd > len(source) || annStart >= annEnd {
		return nil
	}
	region := source[annStart:annEnd]
	var out []ValueKeyHit
	i := 0
	for i < len(region) {
		if region[i] != '"' && region[i] != '\'' {
			i++
			continue
		}
		quote := region[i]
		j := i + 1
		for j < len(region) {
			if region[j] == '\\' {
				j += 2
				continue
			}
			if region[j] == quote {
				break
			}
			j++
		}
		if j >= len(region) {
			break
		}
		lit := region[i+1 : j]
		absBase := annStart + i + 1
		for _, ph := range FindPlaceholders(lit) {
			if ph.Key == "" {
				continue
			}
			hit := ValueKeyHit{
				Key: ph.Key, Start: absBase + ph.KeyStart, End: absBase + ph.KeyEnd,
			}
			rel := 0
			for _, part := range strings.Split(ph.Key, ".") {
				if part == "" {
					continue
				}
				// locate part within key span (keys are simple identifiers + hyphens)
				idx := strings.Index(ph.Key[rel:], part)
				if idx < 0 {
					continue
				}
				ps := absBase + ph.KeyStart + rel + idx
				pe := ps + len(part)
				hit.Segments = append(hit.Segments, struct {
					Name       string
					Start, End int
				}{Name: part, Start: ps, End: pe})
				rel = rel + idx + len(part)
			}
			out = append(out, hit)
		}
		i = j + 1
	}
	return out
}

// ValueKeyAt returns the config key under offset inside a @Value / SpEL string.
// Works without an indexed FileSlice (live buffer / stale cache).
func ValueKeyAt(source string, offset int) string {
	if source == "" || offset < 0 || offset >= len(source) {
		return ""
	}
	// Expand a window around the cursor that covers a typical annotation.
	start := offset - 400
	if start < 0 {
		start = 0
	}
	end := offset + 200
	if end > len(source) {
		end = len(source)
	}
	var best string
	bestSpan := 1 << 30
	for _, hit := range ScanValueKeys(source, start, end) {
		if offset >= hit.Start && offset <= hit.End {
			span := hit.End - hit.Start
			if span < bestSpan {
				bestSpan = span
				best = hit.Key
			}
		}
		for _, seg := range hit.Segments {
			if offset >= seg.Start && offset <= seg.End {
				span := seg.End - seg.Start
				if span < bestSpan {
					bestSpan = span
					best = hit.Key
				}
			}
		}
	}
	return best
}

// ValueKeysBeforeField scans backward from a field-name offset for @Value keys.
func ValueKeysBeforeField(source string, fieldNameOff int) []string {
	if fieldNameOff <= 0 || fieldNameOff > len(source) {
		return nil
	}
	start := fieldNameOff - 500
	if start < 0 {
		start = 0
	}
	seen := map[string]bool{}
	var out []string
	for _, hit := range ScanValueKeys(source, start, fieldNameOff) {
		if hit.Key == "" || seen[hit.Key] {
			continue
		}
		seen[hit.Key] = true
		out = append(out, hit.Key)
	}
	return out
}

// Placeholder is one ${key} or ${key:default} span inside a string literal.
type Placeholder struct {
	Key      string
	KeyStart int // offset within the literal (not including quotes)
	KeyEnd   int
}

// FindPlaceholders extracts Spring property placeholders from annotation values.
// Supports: ${a.b}, ${a.b:default}, nested ${a.${b}:x} (outer key only when simple).
func FindPlaceholders(lit string) []Placeholder {
	var out []Placeholder
	for i := 0; i < len(lit); i++ {
		if i+1 >= len(lit) || lit[i] != '$' || lit[i+1] != '{' {
			continue
		}
		start := i + 2
		depth := 1
		j := start
		for j < len(lit) && depth > 0 {
			if j+1 < len(lit) && lit[j] == '$' && lit[j+1] == '{' {
				depth++
				j += 2
				continue
			}
			if lit[j] == '}' {
				depth--
				if depth == 0 {
					break
				}
			}
			j++
		}
		if depth != 0 || j > len(lit) {
			break
		}
		inner := lit[start:j]
		key, keyRelStart, keyRelEnd := splitPlaceholderKey(inner)
		if key != "" {
			out = append(out, Placeholder{
				Key:      key,
				KeyStart: start + keyRelStart,
				KeyEnd:   start + keyRelEnd,
			})
		}
		// Also scan the default section for nested placeholders.
		if colon := indexTopColon(inner); colon >= 0 && colon+1 < len(inner) {
			def := inner[colon+1:]
			for _, nested := range FindPlaceholders(def) {
				out = append(out, Placeholder{
					Key:      nested.Key,
					KeyStart: start + colon + 1 + nested.KeyStart,
					KeyEnd:   start + colon + 1 + nested.KeyEnd,
				})
			}
		}
		i = j
	}
	return out
}

func splitPlaceholderKey(inner string) (key string, relStart, relEnd int) {
	colon := indexTopColon(inner)
	part := inner
	if colon >= 0 {
		part = inner[:colon]
	}
	// trim spaces
	s := 0
	for s < len(part) && (part[s] == ' ' || part[s] == '\t') {
		s++
	}
	e := len(part)
	for e > s && (part[e-1] == ' ' || part[e-1] == '\t') {
		e--
	}
	key = part[s:e]
	// Skip keys that still contain unresolved nested placeholders as a whole.
	if strings.Contains(key, "${") {
		return "", 0, 0
	}
	return key, s, e
}

func indexTopColon(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		if i+1 < len(s) && s[i] == '$' && s[i+1] == '{' {
			depth++
			i++
			continue
		}
		if s[i] == '}' && depth > 0 {
			depth--
			continue
		}
		if s[i] == ':' && depth == 0 {
			return i
		}
	}
	return -1
}

func hasAnn(anns []string, name string) bool {
	for _, a := range anns {
		if a == name {
			return true
		}
	}
	return false
}

func appendUniqueStr(xs []string, v string) []string {
	for _, x := range xs {
		if x == v {
			return xs
		}
	}
	return append(xs, v)
}

// ConfigPropertyKeys builds Spring relaxed-binding keys for a
// @ConfigurationProperties field (prefix + camel / kebab).
func ConfigPropertyKeys(prefix, field string) []string {
	if field == "" {
		return nil
	}
	p := strings.TrimSuffix(strings.TrimSpace(prefix), ".")
	camel := field
	kebab := ToKebab(field)
	var out []string
	if p == "" {
		out = append(out, camel)
		if kebab != camel {
			out = append(out, kebab)
		}
		return out
	}
	out = append(out, p+"."+camel)
	if kebab != camel {
		out = append(out, p+"."+kebab)
	}
	return out
}

// ToKebab converts camelCase / PascalCase to kebab-case.
func ToKebab(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r - 'A' + 'a')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ToCamel converts kebab-case / snake_case to camelCase.
func ToCamel(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	upper := false
	for i, r := range s {
		if r == '-' || r == '_' || r == '.' {
			upper = true
			continue
		}
		if upper {
			if r >= 'a' && r <= 'z' {
				b.WriteRune(r - 'a' + 'A')
			} else {
				b.WriteRune(r)
			}
			upper = false
			continue
		}
		if i == 0 && r >= 'A' && r <= 'Z' {
			b.WriteRune(r - 'A' + 'a')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ExtractConfigPrefix reads prefix / value from a @ConfigurationProperties region.
func ExtractConfigPrefix(region string) string {
	// prefix = "..." or prefix="..."
	for _, key := range []string{"prefix", "value"} {
		if p := namedStringArg(region, key); p != "" {
			return p
		}
	}
	// @ConfigurationProperties("foo") — first string literal
	for i := 0; i < len(region); i++ {
		if region[i] != '"' && region[i] != '\'' {
			continue
		}
		q := region[i]
		j := i + 1
		for j < len(region) && region[j] != q {
			if region[j] == '\\' {
				j++
			}
			j++
		}
		if j < len(region) {
			return region[i+1 : j]
		}
	}
	return ""
}

func namedStringArg(region, name string) string {
	idx := strings.Index(region, name)
	for idx >= 0 {
		rest := region[idx+len(name):]
		k := 0
		for k < len(rest) && (rest[k] == ' ' || rest[k] == '\t' || rest[k] == '\n' || rest[k] == '\r') {
			k++
		}
		if k < len(rest) && rest[k] == '=' {
			k++
			for k < len(rest) && (rest[k] == ' ' || rest[k] == '\t') {
				k++
			}
			if k < len(rest) && (rest[k] == '"' || rest[k] == '\'') {
				q := rest[k]
				k++
				start := k
				for k < len(rest) && rest[k] != q {
					k++
				}
				return rest[start:k]
			}
		}
		next := strings.Index(region[idx+1:], name)
		if next < 0 {
			break
		}
		idx = idx + 1 + next
	}
	return ""
}
