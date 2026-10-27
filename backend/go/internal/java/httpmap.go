package java

import (
	"strings"
)

var mappingAnnToMethod = map[string]string{
	"GetMapping":    "GET",
	"PostMapping":   "POST",
	"PutMapping":    "PUT",
	"DeleteMapping": "DELETE",
	"PatchMapping":  "PATCH",
}

// ExtractFeignService reads name / value from a @FeignClient annotation region.
func ExtractFeignService(region string) string {
	if p := namedStringArg(region, "name"); p != "" {
		return p
	}
	if p := namedStringArg(region, "value"); p != "" {
		return p
	}
	// @FeignClient("svc")
	return firstStringLiteral(region)
}

// ExtractClassRequestPath reads path from type-level @RequestMapping.
func ExtractClassRequestPath(region string) string {
	return firstMappingPath(region)
}

// ExtractMethodMapping reads HTTP method + path from method annotation region.
func ExtractMethodMapping(anns []string, region string) (httpMethod, path string) {
	for _, a := range anns {
		if m, ok := mappingAnnToMethod[a]; ok {
			return m, firstMappingPath(region)
		}
	}
	if hasAnn(anns, "RequestMapping") {
		path = firstMappingPath(region)
		httpMethod = requestMappingMethod(region)
		return httpMethod, path
	}
	return "", ""
}

func firstMappingPath(region string) string {
	for _, key := range []string{"path", "value"} {
		if p := namedStringArg(region, key); p != "" {
			return p
		}
		if arr := namedStringArrayFirst(region, key); arr != "" {
			return arr
		}
	}
	return firstStringLiteral(region)
}

func namedStringArrayFirst(region, name string) string {
	idx := strings.Index(region, name)
	for idx >= 0 {
		rest := region[idx+len(name):]
		k := 0
		for k < len(rest) && isSpace(rest[k]) {
			k++
		}
		if k < len(rest) && rest[k] == '=' {
			k++
			for k < len(rest) && isSpace(rest[k]) {
				k++
			}
			if k < len(rest) && rest[k] == '{' {
				inner := rest[k+1:]
				return firstStringLiteral(inner)
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

func requestMappingMethod(region string) string {
	// method = RequestMethod.POST  or method = { RequestMethod.GET, ... }
	i := strings.Index(region, "RequestMethod.")
	if i < 0 {
		return "" // any
	}
	rest := region[i+len("RequestMethod."):]
	j := 0
	for j < len(rest) && ((rest[j] >= 'A' && rest[j] <= 'Z') || (rest[j] >= 'a' && rest[j] <= 'z')) {
		j++
	}
	m := strings.ToUpper(rest[:j])
	switch m {
	case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS":
		return m
	default:
		return ""
	}
}

func firstStringLiteral(region string) string {
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

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// JoinHttpPath joins class-level and method-level Spring paths.
func JoinHttpPath(classPath, methodPath string) string {
	if methodPath == "" {
		return NormHttpPath(classPath)
	}
	// Absolute method path replaces class prefix (Spring behavior when method starts with / and
	// is often still joined — Spring joins both; absolute-looking paths still append after class).
	a := strings.TrimSuffix(NormHttpPath(classPath), "/")
	b := NormHttpPath(methodPath)
	if a == "" || a == "/" {
		return b
	}
	if b == "/" {
		return a
	}
	return NormHttpPath(a + b)
}

// NormHttpPath ensures a leading slash and no trailing slash (except root).
func NormHttpPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	if len(p) > 1 && strings.HasSuffix(p, "/") {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

// PathPatternEqual compares routes; {var} segments match any single segment.
func PathPatternEqual(a, b string) bool {
	a, b = NormHttpPath(a), NormHttpPath(b)
	if a == b {
		return true
	}
	as, bs := splitPath(a), splitPath(b)
	if len(as) != len(bs) {
		return false
	}
	for i := range as {
		if as[i] == bs[i] {
			continue
		}
		if isPathVar(as[i]) || isPathVar(bs[i]) {
			continue
		}
		return false
	}
	return true
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func isPathVar(s string) bool {
	return strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")
}
