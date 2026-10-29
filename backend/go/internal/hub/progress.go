package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// Plugin IDs reported in progress (UI may rename mybatis → MyBatis / MyBatis-Plus).
var knownPluginIDs = []string{"spring", "mybatis", "lombok", "mapstruct", "feign"}

var pluginStepLabel = map[string]string{
	"spring":    "Spring",
	"mybatis":    "MyBatis",
	"lombok":    "Lombok",
	"mapstruct": "MapStruct",
	"feign":     "OpenFeign",
}

// ProgressSnap is emitted on stderr as [progress]{json} for the status bar.
type ProgressSnap struct {
	Pct      int                   `json:"pct"`
	Phase    string                `json:"phase"` // scan|hash|java|plugins|done
	Step     string                `json:"step"`  // short label for left status bar
	NavReady bool                  `json:"navReady"`
	Message  string                `json:"message,omitempty"`
	Java     PhaseProg             `json:"java"`
	Plugins  map[string]PluginProg `json:"plugins"`
}

type PhaseProg struct {
	Pct   int    `json:"pct"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
	State string `json:"state"` // pending|running|ready
}

type PluginProg struct {
	State string `json:"state"` // pending|skipped|indexing|ready|error
	Pct   int    `json:"pct"`
}

type progressTracker struct {
	mu      sync.Mutex
	snap    ProgressSnap
	lastPct int
}

func newProgressTracker() *progressTracker {
	plugins := make(map[string]PluginProg, len(knownPluginIDs))
	for _, id := range knownPluginIDs {
		plugins[id] = PluginProg{State: "pending", Pct: 0}
	}
	return &progressTracker{
		snap: ProgressSnap{
			Pct:     1,
			Phase:   "scan",
			Step:    "扫描工作区",
			Java:    PhaseProg{State: "pending"},
			Plugins: plugins,
		},
	}
}

func (t *progressTracker) Snapshot() ProgressSnap {
	t.mu.Lock()
	defer t.mu.Unlock()
	return cloneSnap(t.snap)
}

func cloneSnap(s ProgressSnap) ProgressSnap {
	out := s
	out.Plugins = make(map[string]PluginProg, len(s.Plugins))
	for k, v := range s.Plugins {
		out.Plugins[k] = v
	}
	return out
}

func (t *progressTracker) emitLocked() {
	// Never go backwards — keeps UI smooth when phases overlap.
	if t.snap.Pct < t.lastPct {
		t.snap.Pct = t.lastPct
	}
	if t.snap.Pct > 100 {
		t.snap.Pct = 100
	}
	t.lastPct = t.snap.Pct
	data, err := json.Marshal(t.snap)
	if err != nil {
		return
	}
	fmt.Fprintf(os.Stderr, "[progress]%s\n", data)
}

func (t *progressTracker) setScan(msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.snap.Phase = "scan"
	switch {
	case strings.Contains(msg, "clearing cache"):
		t.snap.Step = "清除缓存"
	case strings.Contains(msg, "stopping background"):
		t.snap.Step = "停止后台任务"
	default:
		t.snap.Step = "扫描工作区"
	}
	t.snap.Pct = 2
	t.snap.Message = msg
	t.emitLocked()
}

func (t *progressTracker) setScanDone(javaTotal int, msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.snap.Phase = "scan"
	t.snap.Step = "扫描完成"
	t.snap.Pct = 5
	t.snap.Message = msg
	t.snap.Java.Total = javaTotal
	t.snap.Java.State = "pending"
	t.emitLocked()
}

func (t *progressTracker) setHashing(done, total int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.snap.Phase = "hash"
	t.snap.Step = "读取文件/缓存"
	t.snap.Java.Total = total
	hp := 0
	if total > 0 {
		hp = done * 100 / total
		if hp > 100 {
			hp = 100
		}
	} else {
		hp = 100
	}
	// hash occupies 5% → 15%
	t.snap.Pct = 5 + hp*10/100
	t.snap.Message = fmt.Sprintf("hash %d/%d", done, total)
	t.emitLocked()
}

func (t *progressTracker) setJavaRunning(done, total int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.snap.Phase = "java"
	t.snap.Step = "解析 Java"
	t.snap.Java.State = "running"
	t.snap.Java.Done = done
	t.snap.Java.Total = total
	jp := 100
	if total > 0 {
		jp = done * 100 / total
		if jp > 100 {
			jp = 100
		}
	}
	t.snap.Java.Pct = jp
	// Java parse occupies 15% → 82%
	t.snap.Pct = 15 + jp*67/100
	t.snap.Message = fmt.Sprintf("parsing %d/%d", done, total)
	t.snap.Step = fmt.Sprintf("解析 Java %d/%d", done, total)
	t.emitLocked()
}

func (t *progressTracker) setJavaReady(total int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.snap.Phase = "java"
	t.snap.Java = PhaseProg{Pct: 100, Done: total, Total: total, State: "ready"}
	t.snap.Pct = 82
	t.snap.NavReady = true
	t.snap.Message = "java ready"
	t.snap.Step = "Java 就绪"
	t.emitLocked()
}

func (t *progressTracker) markPlugins(active map[string]bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.snap.Phase = "plugins"
	t.snap.Step = "检测插件"
	for _, id := range knownPluginIDs {
		if active[id] {
			t.snap.Plugins[id] = PluginProg{State: "pending", Pct: 0}
		} else {
			t.snap.Plugins[id] = PluginProg{State: "skipped", Pct: 100}
		}
	}
	t.snap.Message = "plugins detecting"
	t.recalcPluginsPctLocked()
	t.emitLocked()
}

func (t *progressTracker) setPlugin(id, state string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	pp := t.snap.Plugins[id]
	pp.State = state
	switch state {
	case "indexing":
		pp.Pct = 35
	case "ready":
		pp.Pct = 100
	case "error":
		pp.Pct = 100
	case "skipped":
		pp.Pct = 100
	}
	t.snap.Plugins[id] = pp
	t.snap.Phase = "plugins"
	label := pluginStepLabel[id]
	if label == "" {
		label = id
	}
	switch state {
	case "indexing":
		t.snap.Step = "索引 " + label
	case "ready":
		t.snap.Step = label + " 完成"
	case "error":
		t.snap.Step = label + " 失败"
	default:
		t.snap.Step = label
	}
	t.snap.Message = id + ": " + state
	t.recalcPluginsPctLocked()
	t.emitLocked()
}

func (t *progressTracker) recalcPluginsPctLocked() {
	n, sum := 0, 0
	allDone := true
	for _, id := range knownPluginIDs {
		pp := t.snap.Plugins[id]
		if pp.State == "skipped" {
			continue
		}
		n++
		sum += pp.Pct
		if pp.State != "ready" && pp.State != "error" {
			allDone = false
		}
	}
	pluginPct := 100
	if n > 0 {
		pluginPct = sum / n
	}
	// plugins occupy 82% → 99% while any active plugin is still running.
	// When every active plugin is finished, jump to 100% so the UI cannot
	// sit on "Spring 完成" at 99% waiting for a later setDone (e.g. slow deps).
	if n == 0 || allDone {
		t.snap.Pct = 100
		return
	}
	t.snap.Pct = 82 + pluginPct*17/100
	if t.snap.Pct >= 99 {
		t.snap.Pct = 99
	}
}

func (t *progressTracker) setDone() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.snap.Phase = "done"
	t.snap.Pct = 100
	t.snap.NavReady = true
	t.snap.Message = "ready"
	t.snap.Step = "就绪"
	if t.snap.Java.State != "ready" {
		t.snap.Java.State = "ready"
		t.snap.Java.Pct = 100
	}
	for id, pp := range t.snap.Plugins {
		if pp.State == "pending" || pp.State == "indexing" {
			pp.State = "ready"
			pp.Pct = 100
			t.snap.Plugins[id] = pp
		}
	}
	t.emitLocked()
}
