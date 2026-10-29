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
const fs = require("fs");
const path = require("path");
const readline = require("readline");

let nextRequestId = 1;

function b64(value) {
  return Buffer.from(String(value), "utf8").toString("base64");
}

function workspaceRootFor(document) {
  if (document && document.uri && document.uri.scheme === "file") {
    const folder = vscode.workspace.getWorkspaceFolder(document.uri);
    if (folder) return folder.uri.fsPath;
  }
  return vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
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
    this.status.command = undefined;
    this.status.tooltip = undefined; // no hover panel
    this.status.text = "$(symbol-method) Defhop";
    this.status.show();
    this.child = undefined;
    this.pending = new Map();
    this._ready = false;
    this._starting = false;
    this._startPromise = undefined;
    this.root = undefined;
    this._cacheDir = path.join(context.globalStorageUri.fsPath, "index-cache");
    this._stderrBuffer = "";
    this._syncedVersion = new Map(); // fsPath -> document.version last sent to overlay
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
    const root =
      workspaceRootFor(vscode.window.activeTextEditor?.document) || workspaceRootFor();
    if (!root) {
      this.output.appendLine("[startup] No workspace folder found.");
      return;
    }
    this.root = root;

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

    const child = childProcess.spawn(backendPath, [], {
      cwd: root,
      env: { ...process.env },
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
      init = await this._doRequest(
        "INIT",
        [root, "", "21", this._excludeDirectories().join(","), this._cacheDir],
        300000,
      );
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
      `[ready] ${init.backendVersion} ${summary}, plugins=[${plugins}], ${init.milliseconds || 0} ms`,
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
    this._syncedVersion.set(file, ver);
    return this._doRequest("UPDATE", [file, document.getText()], 15000).catch((error) => {
      this._syncedVersion.delete(file);
      throw error;
    });
  }

  /** Push every open config-like buffer; await so DEF sees cross-file edits. */
  async syncOpenBuffers() {
    if (!this._ready) return;
    const pending = [];
    for (const doc of vscode.workspace.textDocuments) {
      const p = this.pushBufferAwait(doc);
      if (p) pending.push(p);
    }
    if (pending.length === 0) return;
    await Promise.allSettled(pending);
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

  reindexArguments() {
    return [
      this.root,
      "",
      "21",
      this._excludeDirectories().join(","),
      this._cacheDir,
    ];
  }

  _toLocations(raw) {
    return (raw || [])
      .filter((item) => item && item.path)
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
    // Ensure defining files open in other tabs are in the overlay before DEF/REFS.
    try {
      await this.syncOpenBuffers();
    } catch (error) {
      this.output.appendLine(`[nav] sync ${error.message}`);
    }
    const args = [document.uri.fsPath, "", position.line, position.character];
    if (command === "REFS") args.push(includeDeclaration ? "true" : "false");
    try {
      // Slightly longer budget: may sync-parse a few stale overlays first.
      const message = await this._doRequest(command, args, Math.max(timeoutMs, 5000));
      return {
        locations: this._toLocations(message.locations),
        raw: message.locations || [],
      };
    } catch (error) {
      this.output.appendLine(`[nav] ${command} ${error.message}`);
      return { locations: [] };
    }
  }

  async locations(command, document, position, timeoutMs = 3000, includeDeclaration = false) {
    const result = await this.navQuery(command, document, position, timeoutMs, includeDeclaration);
    return result.locations.length ? result.locations : undefined;
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

  dispose() {
    this._rejectAll(new Error("Defhop is shutting down."));
    this._stopProgressAnim();
    this.child?.kill();
    this.child = undefined;
    this._ready = false;
    this.output.dispose();
    this.stepStatus.dispose();
    this.status.dispose();
  }
}

async function activate(context) {
  const client = new DefhopClient(context);
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
        return result.locations && result.locations[0];
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
    vscode.commands.registerCommand("defhop.reindex", async () => {
      await client.ensureReady();
      client._displayPct = 0;
      client._targetPct = 0;
      client._applyProgress({
        pct: 1,
        phase: "scan",
        step: "重新索引",
        navReady: true,
        message: "reindex",
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
        `Defhop reindexed ${result.files || 0} files in ${result.milliseconds || 0} ms` +
          ` [plugins: ${(result.plugins || []).join(", ") || "none"}]`,
      );
    }),
    vscode.commands.registerCommand("defhop.status", () => {
      client.output.show(true);
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
    vscode.workspace.onDidCreateFiles((event) => {
      if (event.files.some(isIndexedFile)) {
        client
          ._doRequest("REINDEX", client.reindexArguments(), 300000)
          .then(() => client.syncOpenBuffers())
          .catch(() => {});
      }
    }),
    vscode.workspace.onDidDeleteFiles((event) => {
      if (event.files.some(isIndexedFile)) {
        client
          ._doRequest("REINDEX", client.reindexArguments(), 300000)
          .then(() => client.syncOpenBuffers())
          .catch(() => {});
      }
    }),
  );

  return { client };
}

function deactivate() {}

module.exports = { activate, deactivate };
