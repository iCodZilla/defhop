"use strict";

/**
 * Defhop — VSCode extension.
 *
 * Instant navigation:
 * - Buffer changes are flushed to the backend IMMEDIATELY (no debounce).
 * - Backend keeps an overlay; nav sync-publishes all stale overlays (cross-file edits).
 * - Before DEF/REFS, open buffers are awaited so the index sees the latest text.
 * - Broken / incomplete Java still updates the index (tolerant parse).
 * - Navigation unlocks as soon as Java core index is ready (navReady), before plugins finish.
 * - Status bar shows overall %; stops spinning at 100%.
 */

const vscode = require("vscode");
const childProcess = require("child_process");
const crypto = require("crypto");
const fs = require("fs");
const path = require("path");
const readline = require("readline");

let nextRequestId = 1;

function b64(value) {
  return Buffer.from(String(value), "utf8").toString("base64");
}

/** Short content fingerprint to skip identical UPDATE payloads (undo/redo). */
function contentFingerprint(text) {
  return crypto.createHash("sha256").update(text, "utf8").digest("hex").slice(0, 32);
}

function workspaceRootFor(document) {
  if (document && document.uri && document.uri.scheme === "file") {
    const folder = vscode.workspace.getWorkspaceFolder(document.uri);
    if (folder) return folder.uri.fsPath;
  }
  return vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
}

/** All workspace folders that look like Java projects (multi-root). */
function javaWorkspaceRoots() {
  const folders = vscode.workspace.workspaceFolders || [];
  const marked = [];
  for (const folder of folders) {
    if (isJavaProjectRoot(folder.uri.fsPath)) marked.push(folder.uri.fsPath);
  }
  if (marked.length > 0) return marked;
  return folders.map((f) => f.uri.fsPath);
}

function fileExists(candidate) {
  try {
    return Boolean(candidate) && fs.statSync(candidate).isFile();
  } catch {
    return false;
  }
}

function isIndexedFile(uri) {
  if (!uri || uri.scheme !== "file") return false;
  const name = uri.fsPath.toLowerCase();
  return (
    name.endsWith(".java") ||
    name.endsWith(".xml") ||
    name.endsWith(".yml") ||
    name.endsWith(".yaml") ||
    name.endsWith(".properties")
  );
}

function isConfigLike(document) {
  if (!document || document.uri.scheme !== "file") return false;
  const lang = document.languageId;
  if (lang === "java" || lang === "xml" || lang === "yaml" || lang === "yml") return true;
  if (lang === "properties" || lang === "java-properties") return true;
  const name = document.uri.fsPath.toLowerCase();
  return (
    name.endsWith(".yml") ||
    name.endsWith(".yaml") ||
    name.endsWith(".properties")
  );
}

const JAVA_PROJECT_MARKERS = [
  "pom.xml",
  "build.gradle",
  "build.gradle.kts",
  "settings.gradle",
  "settings.gradle.kts",
  ".classpath",
];

function pathExists(candidate) {
  try {
    return Boolean(candidate) && fs.existsSync(candidate);
  } catch {
    return false;
  }
}

/** True when the folder looks like a Java / JVM project. */
function isJavaProjectRoot(root) {
  if (!root) return false;
  for (const marker of JAVA_PROJECT_MARKERS) {
    if (pathExists(path.join(root, marker))) return true;
  }
  // Multi-module Maven/Gradle: marker often lives in a child module.
  try {
    const entries = fs.readdirSync(root, { withFileTypes: true });
    for (const ent of entries) {
      if (!ent.isDirectory()) continue;
      if (ent.name.startsWith(".") || ent.name === "node_modules" || ent.name === "target") continue;
      for (const marker of JAVA_PROJECT_MARKERS) {
        if (pathExists(path.join(root, ent.name, marker))) return true;
      }
    }
  } catch {
    /* ignore */
  }
  return false;
}

async function isJavaWorkspace() {
  const folders = vscode.workspace.workspaceFolders;
  if (!folders || folders.length === 0) return false;
  for (const folder of folders) {
    if (isJavaProjectRoot(folder.uri.fsPath)) return true;
  }
  // Fallback: any .java under the workspace (skip heavy trees via exclude).
  const hits = await vscode.workspace.findFiles(
    "**/*.java",
    "**/{node_modules,target,.git,build,out,dist}/**",
    1,
  );
  return hits.length > 0;
}

const PLUGIN_LABELS = [
  { id: "spring", label: "Spring" },
  { id: "feign", label: "OpenFeign" },
  { id: "mybatis", label: "MyBatis" },
  { id: "mybatis", label: "MyBatis-Plus", alias: true },
  { id: "lombok", label: "Lombok" },
  { id: "mapstruct", label: "MapStruct" },
];

function emptyProgress() {
  return {
    pct: 0,
    phase: "idle",
    step: "",
    navReady: false,
    message: "",
    java: { pct: 0, done: 0, total: 0, state: "pending" },
    plugins: {
      spring: { state: "pending", pct: 0 },
      feign: { state: "pending", pct: 0 },
      mybatis: { state: "pending", pct: 0 },
      lombok: { state: "pending", pct: 0 },
      mapstruct: { state: "pending", pct: 0 },
    },
  };
}

function stateIcon(state) {
  switch (state) {
    case "ready":
      return "$(check)";
    case "indexing":
    case "running":
      return "$(sync~spin)";
    case "skipped":
    case "inactive":
      return "$(circle-slash)";
    case "error":
      return "$(error)";
    default:
      return "$(clock)";
  }
}

function stateText(state) {
  switch (state) {
    case "ready":
      return "完成";
    case "indexing":
      return "索引中";
    case "running":
      return "解析中";
    case "skipped":
      return "未启用";
    case "error":
      return "失败";
    case "pending":
      return "等待";
    default:
      return state || "—";
  }
}

class DefhopClient {
  constructor(context) {
    this.context = context;
    this.output = vscode.window.createOutputChannel("Defhop");
    // Left: current step; Right: overall %
    this.stepStatus = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 100);
    this.stepStatus.command = undefined;
    this.stepStatus.tooltip = undefined;
    this.stepStatus.hide();
    this.status = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Right, 90);
    this.status.command = "defhop.status";
    this.status.tooltip = "Defhop";
    this.status.text = "$(symbol-method) Defhop";
    this.status.show();
    this.jdkStatus = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Right, 89);
    this.jdkStatus.command = "defhop.configureJdk";
    this.jdkStatus.hide();
    this.child = undefined;
    this.pending = new Map();
    this._ready = false;
    this._starting = false;
    this._startPromise = undefined;
    this.root = undefined;
    this._cacheDir = path.join(context.globalStorageUri.fsPath, "index-cache");
    this._stderrBuffer = "";
    this._syncedVersion = new Map(); // fsPath -> document.version last sent to overlay
    this._syncedHash = new Map(); // fsPath -> content fingerprint last sent
    this._progress = emptyProgress();
    this._initSummary = "";
    this._lastLoggedPct = -1;
    this._displayPct = 0;
    this._targetPct = 0;
    this._animTimer = undefined;
    this._lastStepText = "";
  }

  async ensureReady() {
    if (this._ready) return;
    if (this._starting) {
      await this._startPromise;
      return;
    }
    this._starting = true;
    this._startPromise = this._doStart();
    try {
      await this._startPromise;
    } finally {
      this._starting = false;
    }
  }

  async _doStart() {
    const allRoots = javaWorkspaceRoots();
    const preferred =
      workspaceRootFor(vscode.window.activeTextEditor?.document) || allRoots[0];
    if (!preferred) {
      this.output.appendLine("[startup] No workspace folder found.");
      return;
    }
    // Preferred root first, then the rest (multi-root union index).
    this.root = preferred;
    this.roots = [
      preferred,
      ...allRoots.filter((r) => path.resolve(r) !== path.resolve(preferred)),
    ];

    const backendPaths = [
      path.join(this.context.extensionPath, "backend", "defhop.exe"),
      path.join(this.context.extensionPath, "backend", "defhop"),
    ];
    const backendPath = backendPaths.find((p) => fileExists(p));
    if (!backendPath) {
      throw new Error("Defhop backend not found. Run build.ps1 first.");
    }

    this._progress = emptyProgress();
    this._displayPct = 0;
    this._targetPct = 0;
    this._applyProgress({ pct: 1, phase: "scan", step: "启动中", message: "starting" });

    const jdkHome = await this._ensureJdkHome();
    const env = { ...process.env };
    if (jdkHome) {
      env.DEFHOP_JDK_HOME = jdkHome;
      try {
        if (fs.statSync(jdkHome).isDirectory()) {
          env.JAVA_HOME = jdkHome;
        }
      } catch {
        /* keep process JAVA_HOME */
      }
    }
    const child = childProcess.spawn(backendPath, [], {
      cwd: preferred,
      env,
      windowsHide: true,
      stdio: ["pipe", "pipe", "pipe"],
    });
    this.child = child;

    const lines = readline.createInterface({ input: child.stdout });
    lines.on("line", (line) => this._handleLine(line));
    child.stderr.on("data", (chunk) => this._handleStderr(chunk));
    child.on("error", (error) => {
      this.output.appendLine(`[backend:error] ${error.stack || error}`);
      this._rejectAll(error);
    });
    child.on("exit", (code, signal) => {
      this._ready = false;
      this.output.appendLine(`[backend] exited code=${code} signal=${signal}`);
      this._stopProgressAnim();
      this.stepStatus.hide();
      this.status.text = "$(error) Defhop";
      this._rejectAll(new Error(`Backend exited (${code ?? signal})`));
      if (this.child === child) this.child = undefined;
    });

    let init;
    try {
      init = await this._doRequest("INIT", this.reindexArguments(), 300000);
    } catch (error) {
      if (this.child === child) this.child = undefined;
      child.kill();
      throw error;
    }

    this._ready = true;
    const plugins = (init.plugins || []).join(",") || "none";
    const summary = `${init.files || 0} files, ${init.symbols || 0} symbols`;
    this._initSummary = summary;
    this.output.appendLine(
      `[ready] ${init.backendVersion} ${summary}, roots=${init.roots || this.roots.length}, plugins=[${plugins}], ${init.milliseconds || 0} ms`,
    );
    this._applyProgress({
      ...(this._progress || emptyProgress()),
      pct: 100,
      phase: "done",
      step: "就绪",
      navReady: true,
      message: "ready",
    });

    for (const doc of vscode.workspace.textDocuments) {
      this.pushBuffer(doc);
    }
  }

  _markNavReady() {
    if (this._ready) return;
    this._ready = true;
    this.output.appendLine("[ready] java core index — navigation unlocked");
    for (const doc of vscode.workspace.textDocuments) {
      this.pushBuffer(doc);
    }
  }

  _applyProgress(snap) {
    if (!snap || typeof snap !== "object") return;
    this._progress = {
      ...emptyProgress(),
      ...this._progress,
      ...snap,
      java: { ...emptyProgress().java, ...(this._progress && this._progress.java), ...snap.java },
      plugins: {
        ...emptyProgress().plugins,
        ...(this._progress && this._progress.plugins),
        ...snap.plugins,
      },
    };
    const pct = Math.max(0, Math.min(100, Number(this._progress.pct) || 0));
    this._progress.pct = pct;
    this._targetPct = Math.max(this._targetPct, pct);

    if (this._progress.navReady) {
      this._markNavReady();
    }

    this._updateStepStatus();
    this._ensureProgressAnim();

    if (pct >= 100 || this._progress.phase === "done") {
      this._targetPct = 100;
      // Snap the bar immediately — avoid sitting on 99% while the ease finishes.
      this._displayPct = 100;
      this.status.text = "$(check) Defhop";
      this.stepStatus.hide();
      this._lastStepText = "";
      this._stopProgressAnim();
    }
  }

  _updateStepStatus() {
    const p = this._progress || emptyProgress();
    if (p.phase === "done" || (p.pct || 0) >= 100) {
      this.stepStatus.hide();
      this._lastStepText = "";
      return;
    }
    const step = (p.step || p.message || "索引中…").trim();
    const text = `$(sync~spin) Defhop: ${step}`;
    if (text === this._lastStepText) return;
    this._lastStepText = text;
    this.stepStatus.text = text;
    this.stepStatus.show();
  }

  _ensureProgressAnim() {
    if (this._animTimer) return;
    this._animTimer = setInterval(() => this._tickProgressAnim(), 40);
  }

  _stopProgressAnim() {
    if (this._animTimer) {
      clearInterval(this._animTimer);
      this._animTimer = undefined;
    }
  }

  _tickProgressAnim() {
    const target = Math.max(0, Math.min(100, this._targetPct || 0));
    let cur = this._displayPct || 0;
    if (cur >= target && target < 100) {
      // Idle between backend updates: creep slowly so the bar never looks frozen.
      const creepCap = Math.min(99, target + 1.2);
      if (cur < creepCap && this._progress && this._progress.phase !== "done") {
        cur = Math.min(creepCap, cur + 0.15);
      }
    } else if (cur < target) {
      const gap = target - cur;
      // Ease toward target — large jumps animate over ~0.4–0.8s.
      const step = Math.max(0.35, gap * 0.18);
      cur = Math.min(target, cur + step);
    }
    this._displayPct = cur;
    const shown = Math.max(1, Math.min(100, Math.floor(cur)));
    if (shown >= 100 && target >= 100) {
      this.status.text = "$(check) Defhop";
      this.stepStatus.hide();
      this._stopProgressAnim();
      return;
    }
    this.status.text = `Defhop ${shown}%`;
  }

  /**
   * Push buffer to overlay (fire-and-forget). Backend stores overlay then parses async;
   * Navigate also sync-publishes stale overlays for cross-file freshness.
   */
  pushBuffer(document) {
    const p = this.pushBufferAwait(document);
    if (p) {
      p.catch((error) => {
        this.output.appendLine(`[update] ${error.message}`);
      });
    }
  }

  /** Await overlay store for one document. Returns null when skipped. */
  pushBufferAwait(document) {
    if (!this._ready || !document || document.uri.scheme !== "file") return null;
    if (!isConfigLike(document)) return null;
    const file = document.uri.fsPath;
    const ver = document.version;
    if (this._syncedVersion.get(file) === ver) return Promise.resolve();
    const text = document.getText();
    const hash = contentFingerprint(text);
    // Same bytes as last push (common on undo) — skip base64 IPC entirely.
    if (this._syncedHash.get(file) === hash) {
      this._syncedVersion.set(file, ver);
      return Promise.resolve();
    }
    this._syncedVersion.set(file, ver);
    this._syncedHash.set(file, hash);
    return this._doRequest("UPDATE", [file, text], 15000).catch((error) => {
      this._syncedVersion.delete(file);
      this._syncedHash.delete(file);
      throw error;
    });
  }

  /**
   * Push open buffers before navigation.
   * Always await the preferred document; other tabs get a short budget so F12 stays snappy.
   */
  async syncOpenBuffers(preferDocument, otherBudgetMs = 120) {
    if (!this._ready) return;
    if (preferDocument) {
      const prefer = this.pushBufferAwait(preferDocument);
      if (prefer) await prefer.catch(() => {});
    }
    const pending = [];
    const preferPath = preferDocument?.uri?.fsPath;
    for (const doc of vscode.workspace.textDocuments) {
      if (preferPath && doc.uri.fsPath === preferPath) continue;
      const p = this.pushBufferAwait(doc);
      if (p) pending.push(p);
    }
    if (pending.length === 0) return;
    await Promise.race([
      Promise.allSettled(pending),
      new Promise((resolve) => setTimeout(resolve, otherBudgetMs)),
    ]);
  }

  _doRequest(command, args, timeoutMs = 60000) {
    if (!this.child || !this.child.stdin.writable) {
      return Promise.reject(new Error("Backend not running"));
    }
    const id = String(nextRequestId++);
    const line = [id, command, ...args.map(b64)].join("\t") + "\n";
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`Defhop request timed out: ${command}`));
      }, timeoutMs);
      this.pending.set(id, {
        resolve: (value) => {
          clearTimeout(timer);
          resolve(value);
        },
        reject: (error) => {
          clearTimeout(timer);
          reject(error);
        },
      });
      this.child.stdin.write(line);
    });
  }

  _handleStderr(chunk) {
    this._stderrBuffer += chunk.toString();
    const parts = this._stderrBuffer.split("\n");
    this._stderrBuffer = parts.pop() || "";
    for (const line of parts) {
      const trimmed = line.trimEnd();
      if (!trimmed) continue;
      // Structured: [progress]{json}  (no space) or legacy [progress] text
      const jsonProg = trimmed.match(/^\[progress\](\{.+\})\s*$/);
      if (jsonProg) {
        try {
          const snap = JSON.parse(jsonProg[1]);
          this._applyProgress(snap);
          const pct = Number(snap.pct) || 0;
          // Avoid flooding the output channel (causes UI jank).
          if (
            pct >= 100 ||
            snap.phase === "done" ||
            snap.navReady && this._lastLoggedPct < 70 ||
            pct - this._lastLoggedPct >= 10
          ) {
            this._lastLoggedPct = pct;
            this.output.appendLine(
              `[progress] ${pct}% ${snap.phase || ""} ${snap.message || ""}`,
            );
          }
        } catch (e) {
          this.output.appendLine(trimmed);
        }
        continue;
      }
      const m = trimmed.match(/^\[progress\]\s+(.+)/);
      if (m) {
        this.output.appendLine(`[progress] ${m[1]}`);
        continue;
      }
      this.output.appendLine(trimmed);
    }
  }

  _handleLine(line) {
    let message;
    try {
      message = JSON.parse(line);
    } catch {
      this.output.appendLine(`[backend] ${line}`);
      return;
    }
    const pending = this.pending.get(String(message.id));
    if (!pending) return;
    this.pending.delete(String(message.id));
    if (message.ok === false) {
      pending.reject(new Error(message.error || "Defhop request failed"));
    } else {
      pending.resolve(message);
    }
  }

  _rejectAll(error) {
    for (const p of this.pending.values()) p.reject(error);
    this.pending.clear();
  }

  _excludeDirectories() {
    const configured = vscode.workspace
      .getConfiguration("defhop")
      .get("excludeDirectories", []);
    return Array.isArray(configured) ? configured.map(String) : [];
  }

  /**
   * JDK path priority:
   * 1) project (workspace / folder) setting
   * 2) user (plugin) setting
   * 3) JAVA_HOME / JDK_HOME / DEFHOP_JDK_HOME
   * 4) auto-detect on disk
   * Env/auto hits are written into project-level defhop.jdkHome.
   */
  _jdkHome() {
    if (this._resolvedJdkHome) return this._resolvedJdkHome;
    const raw = vscode.workspace.getConfiguration("defhop").get("jdkHome", "");
    return typeof raw === "string" ? raw.trim() : "";
  }

  _jdkInspectLayers() {
    const inspected = vscode.workspace.getConfiguration("defhop").inspect("jdkHome") || {};
    const project = String(
      inspected.workspaceFolderValue ?? inspected.workspaceValue ?? "",
    ).trim();
    const user = String(inspected.globalValue ?? "").trim();
    return { project, user };
  }

  _jdkSrcZipFor(home) {
    if (!home) return "";
    try {
      const st = fs.statSync(home);
      if (st.isFile() && path.basename(home).toLowerCase() === "src.zip" && st.size > 1024) {
        return home;
      }
      if (!st.isDirectory()) return "";
    } catch {
      return "";
    }
    for (const rel of ["lib/src.zip", "src.zip"]) {
      const p = path.join(home, rel);
      try {
        const st = fs.statSync(p);
        if (st.isFile() && st.size > 1024) return p;
      } catch {
        /* try next */
      }
    }
    return "";
  }

  _jdkLooksValid(home) {
    return Boolean(this._jdkSrcZipFor(home));
  }

  _jdkHomeFromSrcZip(zipPath) {
    const base = path.basename(zipPath);
    if (base.toLowerCase() !== "src.zip") return zipPath;
    const dir = path.dirname(zipPath);
    if (path.basename(dir).toLowerCase() === "lib") return path.dirname(dir);
    return dir;
  }

  _detectJdkHome() {
    const candidates = [];
    const add = (p) => {
      if (p) candidates.push(p);
    };
    for (const key of ["JAVA_HOME", "JDK_HOME", "DEFHOP_JDK_HOME"]) {
      const v = process.env[key];
      if (v) {
        add(path.join(v, "lib", "src.zip"));
        add(path.join(v, "src.zip"));
      }
    }
    const home = require("os").homedir();
    const roots = [
      "C:\\Program Files\\Java",
      "C:\\Program Files\\Eclipse Adoptium",
      "C:\\Program Files\\Microsoft",
      "C:\\Program Files\\Amazon Corretto",
      "D:\\dev\\sdk\\java",
      path.join(home, "sdk", "java"),
      "/usr/lib/jvm",
      "/Library/Java/JavaVirtualMachines",
    ];
    const walk = (root, depth) => {
      if (depth > 3) return;
      let entries;
      try {
        entries = fs.readdirSync(root, { withFileTypes: true });
      } catch {
        return;
      }
      for (const ent of entries) {
        const p = path.join(root, ent.name);
        if (ent.isFile() && ent.name.toLowerCase() === "src.zip") {
          candidates.push(p);
        } else if (ent.isDirectory()) {
          walk(p, depth + 1);
        }
      }
    };
    for (const root of roots) walk(root, 0);

    let best = "";
    let bestSize = 0;
    for (const p of candidates) {
      try {
        const st = fs.statSync(p);
        if (!st.isFile() || st.size < 100 * 1024) continue;
        if (st.size > bestSize) {
          bestSize = st.size;
          best = p;
        }
      } catch {
        /* skip */
      }
    }
    return best ? this._jdkHomeFromSrcZip(best) : "";
  }

  async _persistProjectJdk(home, source) {
    this._resolvedJdkHome = home;
    this._jdkSource = source;
    this._writingJdkHome = true;
    try {
      const folder = vscode.workspace.workspaceFolders?.[0];
      const cfg = folder
        ? vscode.workspace.getConfiguration("defhop", folder.uri)
        : vscode.workspace.getConfiguration("defhop");
      const target = folder
        ? vscode.ConfigurationTarget.WorkspaceFolder
        : vscode.ConfigurationTarget.Workspace;
      await cfg.update("jdkHome", home, target);
      this.output.appendLine(`[config] defhop.jdkHome ← ${home} (${source}, project)`);
    } catch (error) {
      this.output.appendLine(
        `[config] failed to write project jdkHome: ${error.message || error}`,
      );
    } finally {
      this._writingJdkHome = false;
    }
  }

  _updateJdkStatus() {
    if (!this.jdkStatus) return;
    const home = this._resolvedJdkHome || "";
    const src = this._jdkSource || "";
    if (!home) {
      this.jdkStatus.text = "$(warning)";
      this.jdkStatus.tooltip = "Defhop JDK 未配置 — 点击选择 JDK / src.zip";
      this.jdkStatus.backgroundColor = new vscode.ThemeColor(
        "statusBarItem.warningBackground",
      );
      this.jdkStatus.show();
      return;
    }
    const label =
      src === "workspace"
        ? "project"
        : src === "user"
          ? "user"
          : src.startsWith("env:")
            ? "env"
            : src || "jdk";
    this.jdkStatus.text = "$(coffee)";
    this.jdkStatus.tooltip = `Defhop JDK (${label})\n${home}\n点击可重选 / 清除项目配置`;
    this.jdkStatus.backgroundColor = undefined;
    this.jdkStatus.show();
  }

  async _pickAndSetProjectJdk() {
    const uris = await vscode.window.showOpenDialog({
      canSelectFiles: true,
      canSelectFolders: true,
      canSelectMany: false,
      openLabel: "选择 JDK 目录或 src.zip",
      filters: { "JDK sources": ["zip"], All: ["*"] },
    });
    if (!uris || !uris.length) return "";
    const picked = uris[0].fsPath;
    if (!this._jdkLooksValid(picked)) {
      vscode.window.showWarningMessage(
        `不是有效的 JDK / src.zip：${picked}`,
      );
      return "";
    }
    const home = path.basename(picked).toLowerCase() === "src.zip"
      ? this._jdkHomeFromSrcZip(picked)
      : picked;
    await this._persistProjectJdk(home, "workspace");
    this._updateJdkStatus();
    return home;
  }

  async _clearProjectJdk() {
    this._writingJdkHome = true;
    try {
      const folder = vscode.workspace.workspaceFolders?.[0];
      const cfg = folder
        ? vscode.workspace.getConfiguration("defhop", folder.uri)
        : vscode.workspace.getConfiguration("defhop");
      const target = folder
        ? vscode.ConfigurationTarget.WorkspaceFolder
        : vscode.ConfigurationTarget.Workspace;
      await cfg.update("jdkHome", undefined, target);
      this._resolvedJdkHome = undefined;
      this._jdkSource = "";
      const next = await this._ensureJdkHome();
      this.output.appendLine(
        `[config] cleared project jdkHome → now ${next || "(unset)"} (${this._jdkSource || "none"})`,
      );
      return next;
    } finally {
      this._writingJdkHome = false;
    }
  }

  async _ensureJdkHome() {
    const { project, user } = this._jdkInspectLayers();
    if (project) {
      if (this._jdkLooksValid(project)) {
        this._resolvedJdkHome = project;
        this._jdkSource = "workspace";
        this.output.appendLine(`[config] jdkHome workspace → ${project}`);
        this._updateJdkStatus();
        return project;
      }
      this.output.appendLine(`[config] jdkHome workspace invalid, falling back: ${project}`);
    }
    if (user) {
      if (this._jdkLooksValid(user)) {
        this._resolvedJdkHome = user;
        this._jdkSource = "user";
        this.output.appendLine(`[config] jdkHome user → ${user}`);
        this._updateJdkStatus();
        return user;
      }
      this.output.appendLine(`[config] jdkHome user invalid, falling back: ${user}`);
    }
    for (const key of ["DEFHOP_JDK_HOME", "JAVA_HOME", "JDK_HOME"]) {
      const v = typeof process.env[key] === "string" ? process.env[key].trim() : "";
      if (v && this._jdkLooksValid(v)) {
        await this._persistProjectJdk(v, `env:${key}`);
        this._updateJdkStatus();
        return v;
      }
    }
    const detected = this._detectJdkHome();
    if (detected && this._jdkLooksValid(detected)) {
      await this._persistProjectJdk(detected, "auto");
      this._updateJdkStatus();
      return detected;
    }
    this._resolvedJdkHome = "";
    this._jdkSource = "";
    this.output.appendLine("[config] jdkHome unset (no project/user/env/auto)");
    this._updateJdkStatus();
    return "";
  }

  reindexArguments() {
    const extras = (this.roots || [])
      .filter((r) => path.resolve(r) !== path.resolve(this.root))
      .join("\n");
    return [
      this.root,
      "",
      "21",
      this._excludeDirectories().join(","),
      this._cacheDir,
      extras,
      this._jdkHome(),
    ];
  }

  _toLocations(raw) {
    return (raw || [])
      .filter((item) => item && item.path)
      .filter((item) => {
        try {
          return fs.statSync(item.path).isFile();
        } catch {
          // Missing path: still return so VS Code can show a normal missing-file error.
          return !item.path.endsWith(path.sep) && /\.[a-zA-Z0-9]+$/.test(item.path);
        }
      })
      .map(
        (item) =>
          new vscode.Location(
            vscode.Uri.file(item.path),
            new vscode.Range(
              item.line || 0,
              item.character || 0,
              item.endLine ?? item.line ?? 0,
              item.endCharacter ?? item.character ?? 0,
            ),
          ),
      );
  }

  async navQuery(command, document, position, timeoutMs = 3000, includeDeclaration = false) {
    if (!this._ready) return { locations: [] };
    // Prefer current doc; other open buffers get a short sync budget.
    try {
      await this.syncOpenBuffers(document);
    } catch (error) {
      this.output.appendLine(`[nav] sync ${error.message}`);
    }
    const args = [document.uri.fsPath, "", position.line, position.character];
    if (command === "REFS") args.push(includeDeclaration ? "true" : "false");
    try {
      // Slightly longer budget: may sync-parse a few stale overlays first.
      const message = await this._doRequest(command, args, Math.max(timeoutMs, 5000));
      const locations = this._toLocations(message.locations);
      if (command === "DEF" && locations.length === 0 && message.navMenu === "configure-jdk") {
        this._promptConfigureJdk();
      }
      return {
        locations,
        raw: message.locations || [],
        navMenu: message.navMenu || "",
      };
    } catch (error) {
      this.output.appendLine(`[nav] ${command} ${error.message}`);
      return { locations: [] };
    }
  }

  /** Prompt once per cooldown when JDK sources are missing for java.* DEF. */
  _promptConfigureJdk() {
    const now = Date.now();
    if (this._jdkPromptAt && now - this._jdkPromptAt < 60_000) return;
    this._jdkPromptAt = now;
    const open = "打开设置";
    vscode.window
      .showWarningMessage(
        "无法跳转到 JDK 类型：未找到 JDK 源码。请在设置中配置 Defhop: Jdk Home（JDK 目录或 src.zip）。",
        open,
      )
      .then((choice) => {
        if (choice === open) {
          vscode.commands.executeCommand(
            "workbench.action.openSettings",
            "defhop.jdkHome",
          );
        }
      });
  }

  async locations(command, document, position, timeoutMs = 3000, includeDeclaration = false) {
    const result = await this.navQuery(command, document, position, timeoutMs, includeDeclaration);
    return result.locations.length ? result.locations : undefined;
  }

  _symbolKind(kind) {
    switch (kind) {
      case "type":
        return vscode.SymbolKind.Class;
      case "method":
        return vscode.SymbolKind.Method;
      case "constructor":
        return vscode.SymbolKind.Constructor;
      case "field":
        return vscode.SymbolKind.Field;
      default:
        return vscode.SymbolKind.Variable;
    }
  }

  _toDocumentSymbol(item) {
    const range = new vscode.Range(
      item.line || 0,
      item.character || 0,
      item.endLine ?? item.line ?? 0,
      item.endCharacter ?? item.character ?? 0,
    );
    const sym = new vscode.DocumentSymbol(
      item.name || "?",
      item.detail || "",
      this._symbolKind(item.kind),
      range,
      range,
    );
    if (Array.isArray(item.children)) {
      sym.children = item.children.map((c) => this._toDocumentSymbol(c));
    }
    return sym;
  }

  _toTypeHierarchyItem(item) {
    if (!item || !item.path) return undefined;
    const range = new vscode.Range(
      item.line || 0,
      item.character || 0,
      item.endLine ?? item.line ?? 0,
      item.endCharacter ?? item.character ?? 0,
    );
    const th = new vscode.TypeHierarchyItem(
      this._symbolKind(item.kind || "type"),
      item.name || "?",
      item.detail || "",
      vscode.Uri.file(item.path),
      range,
      range,
    );
    th._defhopKey = item.key || "";
    return th;
  }

  async outline(document) {
    if (!this._ready || !document) return [];
    try {
      const message = await this._doRequest("OUTLINE", [document.uri.fsPath], 5000);
      return (message.symbols || []).map((s) => this._toDocumentSymbol(s));
    } catch (error) {
      this.output.appendLine(`[outline] ${error.message}`);
      return [];
    }
  }

  async workspaceSymbols(query) {
    if (!this._ready) return [];
    try {
      const message = await this._doRequest("SYMBOLS", [query || "", "80"], 5000);
      return (message.symbols || [])
        .filter((s) => s && s.path)
        .map((s) => {
          const range = new vscode.Range(
            s.line || 0,
            s.character || 0,
            s.endLine ?? s.line ?? 0,
            s.endCharacter ?? s.character ?? 0,
          );
          return new vscode.SymbolInformation(
            s.name || "?",
            this._symbolKind(s.kind),
            s.detail || "",
            new vscode.Location(vscode.Uri.file(s.path), range),
          );
        });
    } catch (error) {
      this.output.appendLine(`[symbols] ${error.message}`);
      return [];
    }
  }

  async hierarchyAt(document, position) {
    if (!this._ready || !document) return undefined;
    try {
      const message = await this._doRequest(
        "HIERARCHY",
        [document.uri.fsPath, position.line, position.character],
        5000,
      );
      if (!message.item) return undefined;
      return {
        item: this._toTypeHierarchyItem(message.item),
        supers: (message.supers || []).map((s) => this._toTypeHierarchyItem(s)).filter(Boolean),
        subtypes: (message.subtypes || []).map((s) => this._toTypeHierarchyItem(s)).filter(Boolean),
      };
    } catch (error) {
      this.output.appendLine(`[hierarchy] ${error.message}`);
      return undefined;
    }
  }

  _toCallHierarchyItem(item) {
    if (!item || !item.path) return undefined;
    const range = new vscode.Range(
      item.line || 0,
      item.character || 0,
      item.endLine ?? item.line ?? 0,
      item.endCharacter ?? item.character ?? 0,
    );
    return new vscode.CallHierarchyItem(
      this._symbolKind(item.kind === "reference" ? "method" : item.kind || "method"),
      item.name || "?",
      item.detail || "",
      vscode.Uri.file(item.path),
      range,
      range,
    );
  }

  async hoverAt(document, position) {
    if (!this._ready || !document) return undefined;
    try {
      const message = await this._doRequest(
        "HOVER",
        [document.uri.fsPath, position.line, position.character],
        3000,
      );
      if (!message.hover || !message.hover.markdown) return undefined;
      return new vscode.Hover(
        new vscode.MarkdownString(message.hover.markdown, true),
      );
    } catch (error) {
      this.output.appendLine(`[hover] ${error.message}`);
      return undefined;
    }
  }

  async callsAt(document, position) {
    if (!this._ready || !document) return undefined;
    try {
      const message = await this._doRequest(
        "CALLS",
        [document.uri.fsPath, position.line, position.character],
        8000,
      );
      if (!message.item) return undefined;
      return {
        item: this._toCallHierarchyItem(message.item),
        incoming: (message.incoming || []).map((s) => this._toCallHierarchyItem(s)).filter(Boolean),
        outgoing: (message.outgoing || []).map((s) => this._toCallHierarchyItem(s)).filter(Boolean),
      };
    } catch (error) {
      this.output.appendLine(`[calls] ${error.message}`);
      return undefined;
    }
  }

  async openLocations(locations, emptyMsg) {
    if (!locations || locations.length === 0) {
      vscode.window.showInformationMessage(emptyMsg || "Defhop: no locations");
      return;
    }
    if (locations.length === 1) {
      await vscode.window.showTextDocument(locations[0].uri, {
        selection: locations[0].range,
      });
      return;
    }
    const editor = vscode.window.activeTextEditor;
    if (!editor) return;
    await vscode.commands.executeCommand(
      "editor.action.goToLocations",
      editor.document.uri,
      editor.selection.active,
      locations,
      "goto",
      emptyMsg || "No locations",
    );
  }

  /** Instant definition using only our backend (bypasses slow Java LS merge). */
  async goDefinition() {
    const editor = vscode.window.activeTextEditor;
    if (!editor) return;
    await this.ensureReady();
    const result = await this.navQuery(
      "DEF",
      editor.document,
      editor.selection.active,
      2000,
    );
    await this.openLocations(result.locations, "Defhop: no definition found");
  }

  /** Ask backend to flush cache, then kill the child. */
  async shutdown() {
    if (this.child && this.child.stdin?.writable) {
      try {
        await this._doRequest("SHUTDOWN", [], 8000);
      } catch {
        /* process may already be exiting */
      }
    }
    this.dispose();
  }

  dispose() {
    this._rejectAll(new Error("Defhop is shutting down."));
    this._stopProgressAnim();
    this.child?.kill();
    this.child = undefined;
    this._ready = false;
    this.output.dispose();
    this.stepStatus.dispose();
    this.status.dispose();
    if (this.jdkStatus) this.jdkStatus.dispose();
  }
}

/** Module-level client for deactivate() SHUTDOWN. */
let activeClient;

async function activate(context) {
  const client = new DefhopClient(context);
  activeClient = client;
  const selector = [
    { language: "java", scheme: "file" },
    { language: "xml", scheme: "file" },
    { language: "yaml", scheme: "file" },
    { language: "yml", scheme: "file" },
    { language: "properties", scheme: "file" },
  ];

  const startIndex = (reason) => {
    client.output.appendLine(`[startup] indexing (${reason})`);
    client.ensureReady().catch((error) => {
      console.error(`[defhop] ${error.stack || error}`);
      client.output.appendLine(`[startup] failed: ${error.message || error}`);
    });
  };

  isJavaWorkspace()
    .then((yes) => {
      if (yes) startIndex("java project detected");
      else client.output.appendLine("[startup] not a Java project; index deferred");
    })
    .catch((error) => {
      client.output.appendLine(`[startup] detect failed: ${error.message || error}`);
      startIndex("detect failed — starting anyway");
    });

  context.subscriptions.push(
    client,
    vscode.languages.registerDefinitionProvider(selector, {
      async provideDefinition(document, position, token) {
        await client.ensureReady();
        if (token.isCancellationRequested) return undefined;
        const result = await client.navQuery("DEF", document, position, 2000);
        if (token.isCancellationRequested) return undefined;
        // Return all candidates (matches goDefinition / Peek Definition).
        return result.locations && result.locations.length
          ? result.locations
          : undefined;
      },
    }),
    vscode.commands.registerCommand("defhop.goDefinition", () => client.goDefinition()),
    vscode.languages.registerReferenceProvider(selector, {
      async provideReferences(document, position, referenceContext) {
        await client.ensureReady();
        return (
          (await client.locations(
            "REFS",
            document,
            position,
            10000,
            referenceContext.includeDeclaration,
          )) || []
        );
      },
    }),
    vscode.languages.registerImplementationProvider(selector, {
      async provideImplementation(document, position) {
        await client.ensureReady();
        return client.locations("IMPL", document, position);
      },
    }),
    vscode.languages.registerDocumentSymbolProvider(
      [{ language: "java", scheme: "file" }],
      {
        async provideDocumentSymbols(document) {
          await client.ensureReady();
          return client.outline(document);
        },
      },
    ),
    vscode.languages.registerWorkspaceSymbolProvider({
      async provideWorkspaceSymbols(query) {
        await client.ensureReady();
        return client.workspaceSymbols(query);
      },
    }),
    vscode.languages.registerTypeHierarchyProvider(
      [{ language: "java", scheme: "file" }],
      {
        async prepareTypeHierarchy(document, position) {
          await client.ensureReady();
          const result = await client.hierarchyAt(document, position);
          if (!result || !result.item) return undefined;
          result.item._defhopSupers = result.supers;
          result.item._defhopSubtypes = result.subtypes;
          return result.item;
        },
        async provideTypeHierarchySupertypes(item) {
          if (item._defhopSupers) return item._defhopSupers;
          try {
            const doc = await vscode.workspace.openTextDocument(item.uri);
            const result = await client.hierarchyAt(doc, item.selectionRange.start);
            return result?.supers || [];
          } catch {
            return [];
          }
        },
        async provideTypeHierarchySubtypes(item) {
          if (item._defhopSubtypes) return item._defhopSubtypes;
          try {
            const doc = await vscode.workspace.openTextDocument(item.uri);
            const result = await client.hierarchyAt(doc, item.selectionRange.start);
            return result?.subtypes || [];
          } catch {
            return [];
          }
        },
      },
    ),
    vscode.languages.registerHoverProvider(
      [{ language: "java", scheme: "file" }],
      {
        async provideHover(document, position) {
          await client.ensureReady();
          return client.hoverAt(document, position);
        },
      },
    ),
    vscode.languages.registerCallHierarchyProvider(
      [{ language: "java", scheme: "file" }],
      {
        async prepareCallHierarchy(document, position) {
          await client.ensureReady();
          const result = await client.callsAt(document, position);
          if (!result || !result.item) return undefined;
          result.item._defhopIncoming = result.incoming;
          result.item._defhopOutgoing = result.outgoing;
          return result.item;
        },
        async provideCallHierarchyIncomingCalls(item) {
          if (item._defhopIncoming) {
            return item._defhopIncoming.map(
              (from) => new vscode.CallHierarchyIncomingCall(from, [from.selectionRange]),
            );
          }
          return [];
        },
        async provideCallHierarchyOutgoingCalls(item) {
          if (item._defhopOutgoing) {
            return item._defhopOutgoing.map(
              (to) => new vscode.CallHierarchyOutgoingCall(to, [to.selectionRange]),
            );
          }
          return [];
        },
      },
    ),
    vscode.commands.registerCommand("defhop.refreshIndex", async () => {
      await client.ensureReady();
      client._displayPct = 0;
      client._targetPct = 0;
      client._applyProgress({
        pct: 1,
        phase: "scan",
        step: "刷新索引",
        navReady: true,
        message: "refresh",
        java: { pct: 0, done: 0, total: 0, state: "pending" },
      });
      const result = await client._doRequest("REINDEX", client.reindexArguments(), 300000);
      await client.syncOpenBuffers();
      client._applyProgress({
        ...(client._progress || emptyProgress()),
        pct: 100,
        phase: "done",
        step: "就绪",
        navReady: true,
        message: "ready",
      });
      vscode.window.showInformationMessage(
        `Defhop refreshed ${result.files || 0} files in ${result.milliseconds || 0} ms` +
          ` (cache kept)` +
          ` [plugins: ${(result.plugins || []).join(", ") || "none"}]`,
      );
    }),
    vscode.commands.registerCommand("defhop.rebuildIndex", async () => {
      await client.ensureReady();
      const confirm = await vscode.window.showWarningMessage(
        "Clear Defhop disk cache and rebuild the index from scratch?",
        { modal: true },
        "Rebuild",
      );
      if (confirm !== "Rebuild") return;
      client._displayPct = 0;
      client._targetPct = 0;
      client._lastLoggedPct = 0;
      client._applyProgress({
        pct: 1,
        phase: "scan",
        step: "清除缓存…",
        navReady: false,
        message: "rebuild",
        java: { pct: 0, done: 0, total: 0, state: "pending" },
      });
      const result = await client._doRequest("REBUILD", client.reindexArguments(), 600000);
      await client.syncOpenBuffers();
      client._applyProgress({
        ...(client._progress || emptyProgress()),
        pct: 100,
        phase: "done",
        step: "就绪",
        navReady: true,
        message: "ready",
      });
      vscode.window.showInformationMessage(
        `Defhop rebuilt ${result.files || 0} files in ${result.milliseconds || 0} ms` +
          ` (cache cleared)` +
          ` [plugins: ${(result.plugins || []).join(", ") || "none"}]`,
      );
    }),
    vscode.commands.registerCommand("defhop.status", () => {
      client.output.show(true);
    }),
    vscode.commands.registerCommand("defhop.configureJdk", async () => {
      const current = client._jdkHome() || "(未设置)";
      const src = client._jdkSource || "none";
      const pick = "选择 JDK / src.zip";
      const clear = "清除项目配置";
      const openSettings = "打开设置";
      const choice = await vscode.window.showQuickPick(
        [
          {
            label: pick,
            description: `当前: ${current}`,
            detail: `来源: ${src}`,
          },
          {
            label: clear,
            description: "删除 workspace defhop.jdkHome，再按优先级重探",
          },
          {
            label: openSettings,
            description: "defhop.jdkHome",
          },
        ],
        { title: "Defhop JDK" },
      );
      if (!choice) return;
      if (choice.label === openSettings) {
        await vscode.commands.executeCommand(
          "workbench.action.openSettings",
          "defhop.jdkHome",
        );
        return;
      }
      if (choice.label === clear) {
        await client._clearProjectJdk();
      } else {
        await client._pickAndSetProjectJdk();
      }
      if (client._ready) {
        try {
          await client._doRequest("REINDEX", client.reindexArguments(), 300000);
          await client.syncOpenBuffers();
        } catch (error) {
          client.output.appendLine(`[config] jdk apply failed: ${error.message || error}`);
        }
      }
      const jdk = client._jdkHome();
      if (jdk) {
        vscode.window.showInformationMessage(`Defhop JDK: ${jdk} (${client._jdkSource || "?"})`);
      } else {
        vscode.window.showWarningMessage("Defhop: 仍未找到可用 JDK 源码");
      }
    }),
    vscode.commands.registerCommand("defhop.clearJdkHome", async () => {
      await client._clearProjectJdk();
      if (client._ready) {
        try {
          await client._doRequest("REINDEX", client.reindexArguments(), 300000);
          await client.syncOpenBuffers();
        } catch (error) {
          client.output.appendLine(`[config] jdk clear failed: ${error.message || error}`);
        }
      }
      vscode.window.showInformationMessage(
        `Defhop JDK: ${client._jdkHome() || "(unset)"} (${client._jdkSource || "none"})`,
      );
    }),
    vscode.commands.registerCommand("defhop.goMyBatisSql", async () => {
      const editor = vscode.window.activeTextEditor;
      if (!editor || editor.document.languageId !== "java") return;
      const locations = await client.locations("ROUTE", editor.document, editor.selection.active);
      if (!locations || locations.length === 0) {
        vscode.window.showInformationMessage("No MyBatis XML statement found.");
        return;
      }
      await vscode.window.showTextDocument(locations[0].uri, { selection: locations[0].range });
    }),
    vscode.commands.registerCommand("defhop.goJavaDeclaration", async () => {
      const editor = vscode.window.activeTextEditor;
      if (!editor || editor.document.languageId !== "xml") return;
      const locations = await client.locations("ROUTE", editor.document, editor.selection.active);
      if (!locations || locations.length === 0) {
        vscode.window.showInformationMessage("No Java declaration found.");
        return;
      }
      await vscode.window.showTextDocument(locations[0].uri, { selection: locations[0].range });
    }),
    // Instant overlay — every keystroke / save / open
    vscode.workspace.onDidChangeTextDocument((event) => client.pushBuffer(event.document)),
    vscode.workspace.onDidSaveTextDocument((document) => client.pushBuffer(document)),
    vscode.workspace.onDidOpenTextDocument((document) => {
      if (isIndexedFile(document.uri) && !client._ready && !client._starting) {
        startIndex(`opened ${path.basename(document.uri.fsPath)}`);
      }
      client.pushBuffer(document);
    }),
    vscode.workspace.onDidCloseTextDocument((document) => {
      if (!client._ready || !document || document.uri.scheme !== "file") return;
      if (!isConfigLike(document)) return;
      const file = document.uri.fsPath;
      client._syncedVersion.delete(file);
      client._syncedHash.delete(file);
      client._doRequest("DROPOVERLAY", [file], 5000).catch(() => {});
    }),
    // Incremental: index/remove single paths instead of full REINDEX
    vscode.workspace.onDidCreateFiles((event) => {
      const paths = event.files.filter(isIndexedFile).map((u) => u.fsPath);
      if (paths.length === 0 || !client._ready) return;
      client
        ._doRequest("INDEX", paths, 60000)
        .catch((error) => client.output.appendLine(`[index] ${error.message}`));
    }),
    vscode.workspace.onDidDeleteFiles((event) => {
      const paths = event.files.filter(isIndexedFile).map((u) => u.fsPath);
      if (paths.length === 0 || !client._ready) return;
      for (const p of paths) {
        client._syncedVersion.delete(p);
        client._syncedHash.delete(p);
      }
      client
        ._doRequest("REMOVE", paths, 30000)
        .catch((error) => client.output.appendLine(`[remove] ${error.message}`));
    }),
    vscode.workspace.onDidChangeConfiguration(async (event) => {
      if (!event.affectsConfiguration("defhop.jdkHome")) return;
      if (client._writingJdkHome) return;
      client._resolvedJdkHome = undefined;
      const jdk = await client._ensureJdkHome();
      client.output.appendLine(
        `[config] defhop.jdkHome → ${jdk || "(unset)"} (${client._jdkSource || "none"})`,
      );
      if (!client._ready) return;
      try {
        await client._doRequest("REINDEX", client.reindexArguments(), 300000);
        await client.syncOpenBuffers();
        if (jdk) {
          vscode.window.showInformationMessage(`Defhop JDK updated: ${jdk}`);
        }
      } catch (error) {
        client.output.appendLine(`[config] jdkHome apply failed: ${error.message || error}`);
      }
    }),
  );

  return { client };
}

async function deactivate() {
  const client = activeClient;
  activeClient = undefined;
  if (client) await client.shutdown();
}

module.exports = { activate, deactivate };
