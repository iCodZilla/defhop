package main

import (
	"bufio"
	"context"
	"defhop/internal/deps"
	"defhop/internal/hub"
	"defhop/internal/model"
	"defhop/internal/plugins/feign"
	"defhop/internal/plugins/lombok"
	"defhop/internal/plugins/mapstruct"
	"defhop/internal/plugins/mybatis"
	"defhop/internal/plugins/spring"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// backendVersion is injected at build time:
//
//	go build -ldflags "-X main.backendVersion=$(package.json version)"
var backendVersion = "dev"

// protocolVersion is bumped whenever the line protocol changes. The VS Code
// client refuses to run a backend with a different version.
const protocolVersion = 1

const (
	// Reject absurd requests instead of letting bufio.Scanner kill the process.
	maxRequestLine = 32 << 20
	scannerMaxLine = 64 << 20
)

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
	scanner.Buffer(make([]byte, 1024*1024), scannerMaxLine)

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
		if len(line) > maxRequestLine {
			fmt.Fprintf(os.Stdout,
				`{"id":%s,"ok":false,"error":"request too large; open the file to navigate it"}`+"\n",
				quote(id))
			continue
		}
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
			indexes.stopAndWait(8 * time.Second)
			h.FlushCachePersist()
			fmt.Fprintf(os.Stdout, `{"id":%s,"ok":true}`+"\n", id)
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
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "[io] stdin closed: %v\n", err)
	}
}

// indexArgs is the shared INIT/REINDEX/REBUILD argument bag.
type indexArgs struct {
	roots    []string
	cacheDir string
	jdk      string
	hasJDK   bool
	excludes string
}

func parseIndexArgs(args []string) (indexArgs, error) {
	if len(args) < 1 || args[0] == "" {
		return indexArgs{}, fmt.Errorf("workspace root required")
	}
	root := args[0]
	ia := indexArgs{roots: []string{root}}
	if len(args) >= 4 {
		ia.excludes = args[3]
	}
	if len(args) >= 5 && args[4] != "" {
		ia.cacheDir = args[4]
	} else {
		ia.cacheDir = filepath.Join(root, ".defhop")
	}
	if len(args) >= 6 && args[5] != "" {
		for _, extra := range strings.Split(args[5], "\n") {
			extra = strings.TrimSpace(extra)
			if extra != "" && !strings.EqualFold(filepath.Clean(extra), filepath.Clean(root)) {
				ia.roots = append(ia.roots, extra)
			}
		}
	}
	if len(args) >= 7 {
		ia.jdk = args[6]
		ia.hasJDK = true // empty is meaningful: clear a previously set JDK
	}
	return ia, nil
}

func runIndex(ctx context.Context, h *hub.Hub, cmd string, ia indexArgs) (files, syms, refs int, ms int64, plugins []string, err error) {
	if ia.excludes != "" {
		h.ConfigureExcludes(ia.excludes)
	}
	if ia.hasJDK {
		deps.SetJDKHome(ia.jdk)
		h.ClearExternalLocateCache()
	}
	if cmd == "REBUILD" {
		return h.RebuildRoots(ctx, ia.roots, ia.cacheDir)
	}
	return h.InitRoots(ctx, ia.roots, ia.cacheDir)
}

// indexRunner keeps at most one background full index, cancelable from the
// command loop while it runs.
type indexRunner struct {
	mu     sync.Mutex
	runMu  sync.Mutex
	gen    uint64
	cancel context.CancelFunc
}

var indexes = &indexRunner{}

// begin cancels any running index and reserves the slot for a new run.
func (r *indexRunner) begin() (context.Context, context.CancelFunc, uint64) {
	r.mu.Lock()
	if r.cancel != nil {
		r.cancel()
	}
	r.gen++
	gen := r.gen
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.mu.Unlock()
	return ctx, cancel, gen
}

func (r *indexRunner) finish(gen uint64, cancel context.CancelFunc) {
	cancel()
	r.mu.Lock()
	if r.gen == gen {
		r.cancel = nil // finished on its own; CANCEL must report false
	}
	r.mu.Unlock()
}

// run executes one full index on the calling goroutine (INIT must answer with
// counts, but still reports indexing=true to STATUS/PING while it runs).
func (r *indexRunner) run(h *hub.Hub, cmd string, ia indexArgs) (files, syms, refs int, ms int64, plugins []string, err error) {
	ctx, cancel, gen := r.begin()
	defer r.finish(gen, cancel)
	r.runMu.Lock()
	defer r.runMu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, 0, 0, 0, nil, err
	}
	return runIndex(ctx, h, cmd, ia)
}

func (r *indexRunner) start(h *hub.Hub, cmd string, ia indexArgs) {
	ctx, cancel, gen := r.begin()
	go func() {
		defer r.finish(gen, cancel)
		r.runMu.Lock()
		defer r.runMu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if _, _, _, _, _, err := runIndex(ctx, h, cmd, ia); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "[index] %s failed: %v\n", cmd, err)
		}
	}()
}

func (r *indexRunner) stop() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel == nil {
		return false
	}
	r.cancel()
	r.cancel = nil
	return true
}

func (r *indexRunner) running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancel != nil
}

// stopAndWait cancels the current run and waits (bounded) for it to release
// the index lock, so cache writes finish before the process exits.
func (r *indexRunner) stopAndWait(d time.Duration) {
	r.stop()
	done := make(chan struct{})
	go func() {
		r.runMu.Lock()
		r.runMu.Unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		fmt.Fprintf(os.Stderr, "[index] shutdown: index still running after %s\n", d)
	}
}

func dispatch(h *hub.Hub, cmd string, args []string) (string, error) {
	switch cmd {
	case "PING":
		return fmt.Sprintf(`,"backendVersion":%s,"protocolVersion":%d,"indexing":%t`,
			quote(backendVersion), protocolVersion, indexes.running()), nil

	case "CANCEL":
		return fmt.Sprintf(`,"cancelled":%t`, indexes.stop()), nil

	case "INIT", "REINDEX", "REBUILD":
		ia, err := parseIndexArgs(args)
		if err != nil {
			return "", err
		}
		// REINDEX/REBUILD run in the background: navigation keeps answering
		// from the previous snapshot instead of queueing behind a full scan.
		if cmd == "REINDEX" || cmd == "REBUILD" {
			indexes.start(h, cmd, ia)
			return `,"started":true`, nil
		}
		files, syms, refs, ms, plugins, err := indexes.run(h, cmd, ia)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf(
			`,"files":%d,"symbols":%d,"references":%d,"milliseconds":%d,"backendVersion":%s,"protocolVersion":%d,"plugins":%s,"roots":%d`,
			files, syms, refs, ms, quote(backendVersion), protocolVersion, quoteList(plugins), len(ia.roots),
		), nil

	case "UPDATE":
		if len(args) < 2 {
			return `,"gen":0`, nil
		}
		// Optional 3rd arg "sync": IntelliJ-style commit — index before reply
		// (bulk AI / paste). Default stays async so keystrokes do not block IPC.
		sync := len(args) >= 3 && (args[2] == "sync" || args[2] == "1" || args[2] == "true")
		var gen uint64
		if sync {
			gen = h.IndexBufferSync(args[0], args[1])
		} else {
			gen = h.UpdateBuffer(args[0], args[1])
		}
		return fmt.Sprintf(`,"gen":%d`, gen), nil

	case "INDEX":
		// Incremental index of disk files (create) — avoids full REINDEX.
		n := 0
		for _, p := range args {
			if p != "" && h.IndexDiskFile(p) {
				n++
			}
		}
		return fmt.Sprintf(`,"indexed":%d`, n), nil

	case "REMOVE":
		// Drop paths from overlay + index (delete) — avoids full REINDEX.
		for _, p := range args {
			if p != "" {
				h.RemovePath(p)
			}
		}
		return `,"removed":true`, nil

	case "DROPOVERLAY":
		for _, p := range args {
			if p != "" {
				h.DropOverlay(p)
			}
		}
		return "", nil

	case "STATUS":
		files, syms, refs, plugins, prog := h.Status()
		progJSON, _ := jsonMarshal(prog)
		return fmt.Sprintf(
			`,"files":%d,"symbols":%d,"references":%d,"plugins":%s,"backendVersion":%s,"protocolVersion":%d,"indexing":%t,"progress":%s`,
			files, syms, refs, quoteList(plugins), quote(backendVersion), protocolVersion, indexes.running(), progJSON,
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

	case "OUTLINE":
		if len(args) < 1 || args[0] == "" {
			return `,"symbols":[]`, nil
		}
		items := h.Outline(args[0])
		b, err := json.Marshal(items)
		if err != nil {
			return `,"symbols":[]`, nil
		}
		return `,"symbols":` + string(b), nil

	case "HIERARCHY":
		if len(args) < 3 {
			return `,"item":null,"supers":[],"subtypes":[]`, nil
		}
		item, supers, subtypes := h.TypeHierarchy(args[0], atoi(args[1]), atoi(args[2]))
		ib, _ := json.Marshal(item)
		sb, _ := json.Marshal(supers)
		tb, _ := json.Marshal(subtypes)
		return fmt.Sprintf(`,"item":%s,"supers":%s,"subtypes":%s`, string(ib), string(sb), string(tb)), nil

	case "HOVER":
		if len(args) < 3 {
			return `,"hover":null`, nil
		}
		info := h.Hover(args[0], atoi(args[1]), atoi(args[2]))
		b, _ := json.Marshal(info)
		return `,"hover":` + string(b), nil

	case "HIGHLIGHT":
		if len(args) < 3 {
			return `,"locations":[]`, nil
		}
		locs := h.DocumentHighlights(args[0], atoi(args[1]), atoi(args[2]))
		return `,"locations":` + locsJSON(locs), nil

	case "CALLS":
		if len(args) < 3 {
			return `,"item":null,"incoming":[],"outgoing":[]`, nil
		}
		item, incoming, outgoing := h.CallHierarchy(args[0], atoi(args[1]), atoi(args[2]))
		ib, _ := json.Marshal(item)
		nb, _ := json.Marshal(incoming)
		ob, _ := json.Marshal(outgoing)
		return fmt.Sprintf(`,"item":%s,"incoming":%s,"outgoing":%s`, string(ib), string(nb), string(ob)), nil

	case "SYMBOLS":
		query := ""
		if len(args) >= 1 {
			query = args[0]
		}
		limit := 100
		if len(args) >= 2 && args[1] != "" {
			limit = atoi(args[1])
			if limit <= 0 {
				limit = 100
			}
		}
		items := h.WorkspaceSymbols(query, limit)
		b, err := json.Marshal(items)
		if err != nil {
			return `,"symbols":[]`, nil
		}
		return `,"symbols":` + string(b), nil

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
