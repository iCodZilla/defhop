package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"defhop/internal/hub"
	"defhop/internal/model"
	"defhop/internal/plugins/feign"
	"defhop/internal/plugins/lombok"
	"defhop/internal/plugins/mapstruct"
	"defhop/internal/plugins/mybatis"
	"defhop/internal/plugins/spring"
	"os"
	"path/filepath"
	"strings"
)

const backendVersion = "1.0.0"

func main() {
	fmt.Fprintf(os.Stderr, "[progress] backend %s starting\n", backendVersion)
	h := hub.New(
		mybatis.New(),
		lombok.New(),
		mapstruct.New(),
		spring.New(),
		feign.New(),
	)

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 2 {
			continue
		}
		id, cmd := parts[0], strings.ToUpper(parts[1])
		var args []string
		if len(parts) >= 3 && parts[2] != "" {
			for _, b64 := range strings.Split(parts[2], "\t") {
				decoded, err := base64.StdEncoding.DecodeString(b64)
				if err != nil {
					args = append(args, "")
				} else {
					args = append(args, string(decoded))
				}
			}
		}
		if cmd == "SHUTDOWN" {
			h.FlushCachePersist()
			break
		}

		payload, err := dispatch(h, cmd, args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[request] %s failed: %v\n", cmd, err)
			fmt.Fprintf(os.Stdout, `{"id":%s,"ok":false,"error":%s}`+"\n", quote(id), quote(err.Error()))
		} else {
			fmt.Fprintf(os.Stdout, `{"id":%s,"ok":true%s}`+"\n", id, payload)
		}
	}
}

func dispatch(h *hub.Hub, cmd string, args []string) (string, error) {
	switch cmd {
	case "INIT", "REINDEX":
		if len(args) < 1 || args[0] == "" {
			return "", fmt.Errorf("workspace root required")
		}
		root := args[0]
		if len(args) >= 4 {
			h.ConfigureExcludes(args[3])
		}
		cacheDir := ""
		if len(args) >= 5 && args[4] != "" {
			cacheDir = args[4]
		} else {
			cacheDir = filepath.Join(root, ".defhop-cache")
		}
		files, syms, refs, ms, plugins, err := h.Init(context.Background(), root, cacheDir)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf(
			`,"files":%d,"symbols":%d,"references":%d,"milliseconds":%d,"backendVersion":%s,"plugins":%s`,
			files, syms, refs, ms, quote(backendVersion), quoteList(plugins),
		), nil

	case "UPDATE":
		if len(args) < 2 {
			return `,"gen":0`, nil
		}
		gen := h.UpdateBuffer(args[0], args[1])
		return fmt.Sprintf(`,"gen":%d`, gen), nil

	case "STATUS":
		files, syms, refs, plugins, prog := h.Status()
		progJSON, _ := jsonMarshal(prog)
		return fmt.Sprintf(
			`,"files":%d,"symbols":%d,"references":%d,"plugins":%s,"backendVersion":%s,"progress":%s`,
			files, syms, refs, quoteList(plugins), quote(backendVersion), progJSON,
		), nil

	case "DEF", "REFS", "IMPL", "ROUTE":
		if len(args) < 4 {
			return `,"locations":[]`, nil
		}
		req := model.NavRequest{
			Kind:      model.NavKind(cmd),
			Path:      args[0],
			Text:      args[1],
			Line:      atoi(args[2]),
			Character: atoi(args[3]),
		}
		if cmd == "REFS" && len(args) >= 5 {
			req.IncludeDecl = args[4] == "true"
		}
		locs, navMenu := h.Navigate(req)
		out := `,"locations":` + locsJSON(locs)
		if navMenu != "" {
			out += `,"navMenu":` + quote(navMenu)
		}
		return out, nil

	default:
		return "", fmt.Errorf("unknown command: %s", cmd)
	}
}

func locsJSON(locs []*model.Location) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, loc := range locs {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b,
			`{"path":%s,"line":%d,"character":%d,"endLine":%d,"endCharacter":%d,"kind":%s,"label":%s}`,
			quote(loc.Path), loc.Line, loc.Character, loc.EndLine, loc.EndCharacter,
			quote(loc.Kind), quote(loc.Label),
		)
	}
	b.WriteByte(']')
	return b.String()
}

func quoteList(xs []string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range xs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(quote(x))
	}
	b.WriteByte(']')
	return b.String()
}

func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func jsonMarshal(v interface{}) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}", err
	}
	return string(b), nil
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		} else {
			break
		}
	}
	return n
}
