"use strict";

const vscode = require("vscode");
const childProcess = require("child_process");
const fs = require("fs");
const path = require("path");
const readline = require("readline");
const util = require("./util");

const {
  PROTOCOL_VERSION,
  MAX_OVERLAY_TEXT_BYTES,
  b64,
  contentFingerprint,
  workspaceRootFor,
  javaWorkspaceRoots,
  fileExists,
  backendBinaryCandidates,
  ensureUnixExecutable,
  isIndexedFile,
  pathsEqual,
  openDocumentForPath,
  isConfigLike,
  isJavaProjectRoot,
  PLUGIN_LABELS,
  emptyProgress,
  stateIcon,
  stateText,
} = util;

class DefhopClient {
  constructor(context) {
    this.context = context;
    this.output = vscode.window.createOutputChannel("Defhop");
    // Right-corner: built-in spin while busy, check when ready. No left step text.
    this.status = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Right, 90);
    this.status.command = "defhop.status";
    this.status.tooltip = "Defhop";
    this.status.text = "$(sync~spin) Defhop";
    this.status.show();
    this._statusMode = "loading"; // loading | sync | ready | error
    this.jdkStatus = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Right, 89);
    this.jdkStatus.command = "defhop.configureJdk";
    this.jdkStatus.hide();
    this.child = undefined;
    this.pending = new Map();
    this._ready = false;
    this._starting = false;
    this._startPromise = undefined;
    this.root = undefined;
    // Set in _doStart to <workspace>/.defhop (project-local cache).
    this._cacheDir = undefined;
    this._stderrBuffer = "";
    this._syncedVersion = new Map(); // fsPath -> document.version last sent to overlay
    this._syncedHash = new Map(); // fsPath -> content fingerprint last sent
    this._pendingDiskIndex = new Set();
    this._pendingDiskRemove = new Set();
    this._diskIndexTimer = undefined;
    this._diskRemoveTimer = undefined;
    this._diskIndexInFlight = 0;
    this._diskIndexChain = Promise.resolve();
    this._updateInFlight = 0;
    this._progress = emptyProgress();
    this._initSummary = "";
    this._lastLoggedPct = -1;
    this._displayPct = 0;
    this._targetPct = 0;
    this._animTimer = undefined;
    this._liveSyncLabel = "";
    this._restartTimer = undefined;
    this._restartStableTimer = undefined;
    this._restartAttempts = 0;
    this._deactivating = false;
    this._oversizeLogged = new Set();
  }

  async ensureReady() {
    if (this._deactivating) throw new Error("Defhop is shutting down.");
    if (this._ready) return;
    if (this._starting) {
      // Unblock as soon as java core is navigable (navReady), not when INIT
      // fully finishes plugins/deps — that is what made F12 feel stuck.
      await this._waitNavReady();
      return;
    }
    this._starting = true;
    this._startPromise = this._doStart();
    try {
      await this._waitNavReady();
    } finally {
      // Keep _starting true until _doStart settles so concurrent ensureReady
      // callers share the same wait, then clear in the background.
      this._startPromise.finally(() => {
        this._starting = false;
      });
    }
  }

  /** Resolves when navigation is unlocked, or rejects if startup failed. */
  async _waitNavReady() {
    if (this._ready) return;
    const start = this._startPromise;
    if (!start) {
      throw new Error("Defhop is not starting.");
    }
    while (!this._ready) {
      if (this._deactivating) throw new Error("Defhop is shutting down.");
      const settled = await Promise.race([
        start.then(() => "done").catch((e) => {
          throw e;
        }),
        new Promise((resolve) => setTimeout(() => resolve("tick"), 40)),
      ]);
      if (this._ready) return;
      if (settled === "done") {
        // INIT finished without navReady (empty workspace / error path).
        if (!this._ready) {
          throw new Error("Defhop backend started but index is not ready.");
        }
        return;
      }
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
    // Index + deps cache lives in the project (not Cursor globalStorage on C:).
    this._cacheDir = path.join(preferred, ".defhop");
    try {
      fs.mkdirSync(this._cacheDir, { recursive: true });
    } catch (error) {
      this.output.appendLine(`[cache] mkdir ${this._cacheDir}: ${error.message || error}`);
    }
    this.output.appendLine(`[cache] ${this._cacheDir}`);

    const backendPaths = backendBinaryCandidates(this.context.extensionPath);
    const backendPath = backendPaths.find((p) => fileExists(p));
    if (!backendPath) {
      throw new Error(
        `Defhop backend not found for ${process.platform}-${process.arch}. ` +
          `Expected one of: ${backendPaths.map((p) => path.basename(p)).join(", ")}. Run build.ps1.`,
      );
    }
    ensureUnixExecutable(backendPath);
    this.output.appendLine(`[backend] ${backendPath}`);

    this._progress = emptyProgress();
    this._displayPct = 0;
    this._targetPct = 0;
    this._lastLoggedPct = -1;
    // A restarted backend starts with an empty overlay: forget what the old
    // process had synced so open buffers are pushed again after INIT.
    this._syncedVersion.clear();
    this._syncedHash.clear();
    this._oversizeLogged.clear();
    this._applyProgress({ pct: 1, phase: "scan", step: "启动中", message: "starting" });

    // Do not walk Program Files for JDK before spawn — that blocked INIT on
    // cold machines. Project setting / env is enough to start; auto-detect
    // continues after navigation unlocks.
    const jdkHome = await this._ensureJdkHome({ allowAuto: false });
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
      this._rejectAll(new Error(`Backend exited (${code ?? signal})`));
      if (this.child === child) this.child = undefined;
      if (this._restartStableTimer) {
        clearTimeout(this._restartStableTimer);
        this._restartStableTimer = undefined;
      }
      if (this._deactivating || child._defhopDeliberateKill) return;
      this._setStatusMode("error", "后端已退出，正在重启…");
      this._scheduleRestart();
    });

    // Refuse a backend built from different protocol assumptions, otherwise
    // missing fields surface as silent wrong navigation results.
    let pong;
    try {
      pong = await this._doRequest("PING", [], 5000);
    } catch (error) {
      child._defhopDeliberateKill = true;
      if (this.child === child) this.child = undefined;
      child.kill();
      this._setStatusMode("error", "后端版本不匹配");
      throw new Error(
        `Defhop backend is out of date (${error.message || error}). Rebuild or reinstall the extension.`,
      );
    }
    if (Number(pong.protocolVersion) !== PROTOCOL_VERSION) {
      child._defhopDeliberateKill = true;
      child.kill();
      if (this.child === child) this.child = undefined;
      this._setStatusMode("error", "后端版本不匹配");
      throw new Error(
        `Defhop protocol mismatch: extension=${PROTOCOL_VERSION} backend=${pong.protocolVersion}. ` +
          `Rebuild or reinstall the extension.`,
      );
    }
    this.output.appendLine(
      `[backend] version=${pong.backendVersion || "?"} protocol=${pong.protocolVersion}`,
    );

    let init;
    try {
      init = await this._doRequest("INIT", this.reindexArguments(), 300000);
    } catch (error) {
      child._defhopDeliberateKill = true;
      if (this.child === child) this.child = undefined;
      child.kill();
      this._setStatusMode("error", "后端启动失败，点击查看输出");
      this.output.appendLine(`[backend] init failed: ${error.message || error}`);
      throw error;
    }

    this._ready = true;
    // Count a crash loop from the same failure, but a backend that survives
    // 30s is considered healthy and gets a fresh retry budget.
    if (this._restartStableTimer) clearTimeout(this._restartStableTimer);
    this._restartStableTimer = setTimeout(() => {
      this._restartStableTimer = undefined;
      this._restartAttempts = 0;
    }, 30000);
    const plugins = (init.plugins || []).join(",") || "none";
    const summary = `${init.files || 0} files, ${init.symbols || 0} symbols`;
    this._initSummary = summary;
    this.output.appendLine(
      `[ready] ${init.backendVersion} ${summary}, roots=${init.roots || this.roots.length}, plugins=[${plugins}], ${init.milliseconds || 0} ms`,
    );
    this._applyProgress({
      ...(this._progress || emptyProgress()),
      pct: Math.max(Number(this._progress?.pct) || 0, 82),
      phase: this._progress?.phase === "done" ? "done" : "plugins",
      step: this._progress?.phase === "done" ? "就绪" : "插件索引中",
      navReady: true,
      message: "java-ready",
    });

    for (const doc of vscode.workspace.textDocuments) {
      this.pushBuffer(doc);
    }

    // Auto-detect JDK after nav is up; apply via background REINDEX if new.
    if (!jdkHome) {
      void this._ensureJdkHome({ allowAuto: true })
        .then(async (found) => {
          if (!found || this._deactivating || !this._ready) return;
          this.output.appendLine(`[config] auto JDK after start → ${found}`);
          try {
            await this._runIndexCommand("REINDEX", 300000);
            await this.syncOpenBuffers();
          } catch (error) {
            this.output.appendLine(`[config] auto JDK reindex failed: ${error.message || error}`);
          }
        })
        .catch(() => {});
    }
  }

  _markNavReady() {
    if (this._ready) return;
    this._ready = true;
    this.output.appendLine("[ready] java core index — navigation unlocked");
    this._setStatusMode("ready");
    for (const doc of vscode.workspace.textDocuments) {
      this.pushBuffer(doc);
    }
  }

  /** Restart a crashed backend with bounded exponential backoff. */
  _scheduleRestart() {
    if (this._deactivating || this._restartTimer) return;
    if (this._restartAttempts >= 3) {
      this.output.appendLine("[backend] giving up after 3 restart attempts — see output above");
      this._setStatusMode("error", "后端反复退出，请查看 Defhop 输出");
      return;
    }
    const delay = 1000 * 3 ** this._restartAttempts;
    this._restartAttempts += 1;
    this.output.appendLine(
      `[backend] restart in ${delay}ms (attempt ${this._restartAttempts}/3)`,
    );
    this._restartTimer = setTimeout(async () => {
      this._restartTimer = undefined;
      if (this._deactivating) return;
      try {
        await this.ensureReady();
      } catch (error) {
        this.output.appendLine(`[backend] restart failed: ${error.message || error}`);
        this._scheduleRestart();
      }
    }, delay);
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

    this._ensureProgressAnim();
    this._refreshStatusBar();

    if (pct >= 100 || this._progress.phase === "done") {
      this._targetPct = 100;
      this._displayPct = 100;
      this._stopProgressAnim();
      this._refreshStatusBar();
    }
  }

  _liveSyncPendingCount() {
    return this._pendingDiskIndex.size + this._diskIndexInFlight + this._updateInFlight;
  }

  /**
   * Right-corner Defhop badge:
   *   $(sync~spin) → indexing / catching up (VS Code animates)
   *   $(check)     → synced
   *   $(error)     → backend failed
   */
  _setStatusMode(mode, detail) {
    this._statusMode = mode;
    const tipExtra = detail ? `\n${detail}` : "";
    this.status.backgroundColor = undefined;
    if (mode === "error") {
      this.status.text = "$(error) Defhop";
      this.status.tooltip = `Defhop 出错${tipExtra}\n点击打开输出`;
      this.status.backgroundColor = new vscode.ThemeColor("statusBarItem.errorBackground");
      return;
    }
    if (mode === "loading" || mode === "sync") {
      this.status.text = "$(sync~spin) Defhop";
      if (mode === "loading") {
        const pct = Math.max(0, Math.min(100, Math.floor(this._displayPct || 0)));
        this.status.tooltip =
          `Defhop 正在建索引${pct ? ` ${pct}%` : ""}…${tipExtra}\n转圈结束后变为 ✓，即可跳转。\n点击打开输出`;
      } else {
        this.status.tooltip =
          `Defhop 正在同步改动…${tipExtra}\n转圈结束后变为 ✓，即可跳转。\n点击打开输出`;
      }
      return;
    }
    this.status.text = "$(check) Defhop";
    this.status.tooltip =
      `Defhop 已同步，可跳转。${tipExtra}\n点击打开输出`;
  }

  _refreshStatusBar() {
    if (this._statusMode === "error" && !this._ready && !this._starting) {
      return;
    }
    const p = this._progress || emptyProgress();
    const initDone = p.phase === "done" || (p.pct || 0) >= 100;
    const n = this._liveSyncPendingCount();
    if (!initDone) {
      // No left/step text (Spring / 缓存…); only right spinning badge.
      this._setStatusMode("loading");
      return;
    }
    if (n > 0) {
      this._setStatusMode("sync");
      return;
    }
    this._setStatusMode("ready");
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
      const creepCap = Math.min(99, target + 1.2);
      if (cur < creepCap && this._progress && this._progress.phase !== "done") {
        cur = Math.min(creepCap, cur + 0.15);
      }
    } else if (cur < target) {
      const gap = target - cur;
      const step = Math.max(0.35, gap * 0.18);
      cur = Math.min(target, cur + step);
    }
    this._displayPct = cur;
    if (Math.floor(cur) >= 100 && target >= 100) {
      this._stopProgressAnim();
    }
    this._refreshStatusBar();
  }

  /** Drop build/cache paths so watcher storms never enter the sync queue. */
  _filterLiveIndexPaths(paths) {
    const excludes = this._excludeDirectories();
    const out = [];
    for (const p of paths || []) {
      if (!p) continue;
      if (!isIndexedFile(vscode.Uri.file(p), excludes)) continue;
      out.push(p);
    }
    return out;
  }

  /**
   * Queue disk INDEX for AI/tool writes. Coalesce ~16ms bursts, then sync-index
   * so F12 can run as soon as the batch returns (backend INDEX is synchronous).
   */
  scheduleDiskIndex(paths) {
    if (!this._ready || !paths || paths.length === 0) return;
    const filtered = this._filterLiveIndexPaths(paths);
    if (filtered.length === 0) return;
    for (const p of filtered) {
      this._pendingDiskIndex.add(p);
    }
    this._refreshStatusBar();
    if (this._diskIndexTimer) clearTimeout(this._diskIndexTimer);
    // Larger pending sets (checkout / deps) debounce longer so we batch once.
    const delay = this._pendingDiskIndex.size > 40 ? 80 : 16;
    this._diskIndexTimer = setTimeout(() => {
      this._diskIndexTimer = undefined;
      void this._flushDiskIndexAwait();
    }, delay);
  }

  /** Debounced REMOVE when files disappear on disk. */
  scheduleDiskRemove(paths) {
    if (!this._ready || !paths || paths.length === 0) return;
    const filtered = this._filterLiveIndexPaths(paths);
    if (filtered.length === 0) return;
    for (const p of filtered) {
      this._pendingDiskIndex.delete(p);
      this._pendingDiskRemove.add(p);
      this._syncedVersion.delete(p);
      this._syncedHash.delete(p);
    }
    this._refreshStatusBar();
    if (this._diskRemoveTimer) clearTimeout(this._diskRemoveTimer);
    this._diskRemoveTimer = setTimeout(() => this._flushDiskRemove(), 16);
  }

  /** Await any queued disk INDEX before navigation (AI writes must be visible). */
  async flushPendingDiskIndex() {
    if (this._diskIndexTimer) {
      clearTimeout(this._diskIndexTimer);
      this._diskIndexTimer = undefined;
    }
    await this._flushDiskIndexAwait();
    await this._diskIndexChain;
  }

  _flushDiskIndexAwait() {
    const INDEX_BATCH = 64;
    const run = async () => {
      const pending = [...this._pendingDiskIndex];
      this._pendingDiskIndex.clear();
      if (!this._ready || pending.length === 0) {
        this._refreshStatusBar();
        return;
      }
      const fromDisk = [];
      const openPushes = [];
      for (const p of pending) {
        if (this._pendingDiskRemove.has(p)) continue;
        if (!isIndexedFile(vscode.Uri.file(p), this._excludeDirectories())) continue;
        const doc = openDocumentForPath(p);
        if (doc && isConfigLike(doc)) {
          const push = this.pushBufferAwait(doc);
          if (push) openPushes.push(push);
        } else {
          fromDisk.push(p);
        }
      }
      // Cap each INDEX so a flood cannot hold the sync badge for minutes.
      const batch = fromDisk.slice(0, INDEX_BATCH);
      const rest = fromDisk.slice(INDEX_BATCH);
      for (const p of rest) this._pendingDiskIndex.add(p);
      this._liveSyncLabel = batch[0] || pending[0] || "";
      this._diskIndexInFlight += 1;
      this._refreshStatusBar();
      try {
        if (openPushes.length) {
          await Promise.allSettled(openPushes);
        }
        if (batch.length) {
          await this._doRequest("INDEX", batch, 60000);
        }
      } catch (error) {
        this.output.appendLine(`[index] ${error.message}`);
      } finally {
        this._diskIndexInFlight = Math.max(0, this._diskIndexInFlight - 1);
        this._liveSyncLabel = "";
        this._refreshStatusBar();
        if (this._pendingDiskIndex.size > 0) {
          void this._flushDiskIndexAwait();
        }
      }
    };
    this._diskIndexChain = this._diskIndexChain.then(run, run);
    return this._diskIndexChain;
  }

  _flushDiskRemove() {
    this._diskRemoveTimer = undefined;
    const pending = [...this._pendingDiskRemove];
    this._pendingDiskRemove.clear();
    if (!this._ready || pending.length === 0) {
      this._refreshStatusBar();
      return;
    }
    this._doRequest("REMOVE", pending, 30000)
      .catch((error) => {
        this.output.appendLine(`[remove] ${error.message}`);
      })
      .finally(() => this._refreshStatusBar());
  }

  /**
   * Push buffer to overlay (fire-and-forget by default).
   * Bulk AI/paste passes { sync: true } so the index is committed before return.
   */
  pushBuffer(document, opts) {
    const p = this.pushBufferAwait(document, opts);
    if (p) {
      p.catch((error) => {
        this.output.appendLine(`[update] ${error.message}`);
      });
    }
  }

  /**
   * Await overlay store for one document. Returns null when skipped.
   * opts.sync → backend IndexBufferSync (IntelliJ commitDocument for large edits).
   */
  pushBufferAwait(document, opts = {}) {
    if (!this._ready || !document || document.uri.scheme !== "file") return null;
    if (!isConfigLike(document)) return null;
    const file = document.uri.fsPath;
    const ver = document.version;
    const sync = !!opts.sync;
    if (!sync && this._syncedVersion.get(file) === ver) return Promise.resolve();
    const text = document.getText();
    if (Buffer.byteLength(text, "utf8") > MAX_OVERLAY_TEXT_BYTES) {
      if (!this._oversizeLogged.has(file)) {
        this._oversizeLogged.add(file);
        this.output.appendLine(
          `[update] skipped oversized buffer (>${MAX_OVERLAY_TEXT_BYTES >> 20} MB): ${file}`,
        );
      }
      // Make sure a previously pushed (smaller) overlay is not left published.
      this._doRequest("DROPOVERLAY", [file], 5000).catch(() => {});
      return null;
    }
    const hash = contentFingerprint(text);
    // Same bytes as last push (common on undo) — skip base64 IPC entirely.
    if (!sync && this._syncedHash.get(file) === hash) {
      this._syncedVersion.set(file, ver);
      return Promise.resolve();
    }
    this._oversizeLogged.delete(file);
    this._syncedVersion.set(file, ver);
    this._syncedHash.set(file, hash);
    const args = sync ? [file, text, "sync"] : [file, text];
    this._updateInFlight += 1;
    this._liveSyncLabel = file;
    this._refreshStatusBar();
    return this._doRequest("UPDATE", args, sync ? 30000 : 15000)
      .catch((error) => {
        this._syncedVersion.delete(file);
        this._syncedHash.delete(file);
        throw error;
      })
      .finally(() => {
        this._updateInFlight = Math.max(0, this._updateInFlight - 1);
        if (this._updateInFlight === 0) this._liveSyncLabel = "";
        this._refreshStatusBar();
      });
  }

  /**
   * IntelliJ commitAllDocuments: await every open indexed buffer before nav.
   * (No short race timeout — missing a sibling AI edit is worse than ~ms wait.)
   */
  async syncOpenBuffers(preferDocument) {
    if (!this._ready) return;
    if (preferDocument) {
      const prefer = this.pushBufferAwait(preferDocument);
      if (prefer) await prefer.catch(() => {});
    }
    const pending = [];
    const preferPath = preferDocument?.uri?.fsPath;
    for (const doc of vscode.workspace.textDocuments) {
      if (preferPath && pathsEqual(doc.uri.fsPath, preferPath)) continue;
      const p = this.pushBufferAwait(doc);
      if (p) pending.push(p);
    }
    if (pending.length === 0) return;
    await Promise.allSettled(pending);
  }

  /** True when the edit looks like AI apply / paste (not a single keystroke). */
  isBulkEdit(event) {
    if (!event || !event.contentChanges || event.contentChanges.length === 0) return false;
    if (event.contentChanges.length >= 4) return true;
    let span = 0;
    for (const c of event.contentChanges) {
      span += (c.text || "").length + (c.rangeLength || 0);
      if ((c.text || "").includes("\n") && (c.text || "").length >= 40) return true;
    }
    return span >= 80;
  }

  onDocumentEdited(event) {
    if (!event || !event.document) return;
    if (this.isBulkEdit(event)) {
      this.pushBuffer(event.document, { sync: true });
    } else {
      this.pushBuffer(event.document);
    }
  }

  _doRequest(command, args, timeoutMs = 60000) {
    if (!this.child || !this.child.stdin.writable) {
      return Promise.reject(new Error("Backend not running"));
    }
    const id = String(util.nextRequestId++);
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
   * JDK path priority (local / Remote-SSH stay independent):
   * 1) project (workspace / folder) setting only — never User/global
   * 2) JAVA_HOME / JDK_HOME / DEFHOP_JDK_HOME
   * 3) auto-detect on disk
   * Env/auto hits are written into project-level defhop.jdkHome.
   * User-level defhop.jdkHome is ignored so Settings Sync / remote cannot overwrite local.
   */
  _jdkHome() {
    if (this._resolvedJdkHome) return this._resolvedJdkHome;
    return this._jdkInspectLayers().project;
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
    const label = src === "workspace"
      ? "project"
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

  async _ensureJdkHome(opts = {}) {
    const allowAuto = opts.allowAuto !== false;
    const { project, user } = this._jdkInspectLayers();
    if (user) {
      this.output.appendLine(
        `[config] ignoring user/global defhop.jdkHome (use project setting only): ${user}`,
      );
    }
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
    for (const key of ["DEFHOP_JDK_HOME", "JAVA_HOME", "JDK_HOME"]) {
      const v = typeof process.env[key] === "string" ? process.env[key].trim() : "";
      if (v && this._jdkLooksValid(v)) {
        await this._persistProjectJdk(v, `env:${key}`);
        this._updateJdkStatus();
        return v;
      }
    }
    if (!allowAuto) {
      this._resolvedJdkHome = "";
      this._jdkSource = "";
      this.output.appendLine("[config] jdkHome unset (defer auto-detect)");
      this._updateJdkStatus();
      return "";
    }
    const detected = this._detectJdkHome();
    if (detected && this._jdkLooksValid(detected)) {
      await this._persistProjectJdk(detected, "auto");
      this._updateJdkStatus();
      return detected;
    }
    this._resolvedJdkHome = "";
    this._jdkSource = "";
    this.output.appendLine("[config] jdkHome unset (no project/env/auto)");
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

  /**
   * Run a full index and wait for it.
   * The backend answers REINDEX/REBUILD immediately (started:true) so nav
   * keeps working from the previous snapshot; completion is observed through
   * STATUS (indexing flag + progress phase).
   */
  async _runIndexCommand(command, timeoutMs = 600000) {
    const startedAt = Date.now();
    const reply = await this._doRequest(command, this.reindexArguments(), 15000);
    if (!reply.started) return reply; // older synchronous reply
    while (Date.now() - startedAt < timeoutMs) {
      if (this._deactivating) throw new Error("Defhop is shutting down.");
      if (!this.child) throw new Error("Backend exited while indexing.");
      await new Promise((resolve) => setTimeout(resolve, 500));
      let ping;
      try {
        ping = await this._doRequest("PING", [], 5000);
      } catch (error) {
        this.output.appendLine(`[index] status poll: ${error.message}`);
        continue;
      }
      if (ping.indexing) continue;
      const status = await this._doRequest("STATUS", [], 5000);
      if (status.indexing) continue;
      // Java core returns before plugins/deps; indexing flips false while
      // phase is still "plugins". Wait for setDone instead of treating that as abort.
      const phase = status.progress && status.progress.phase;
      const pct = Number(status.progress && status.progress.pct) || 0;
      if (phase === "done" || pct >= 100) {
        return { ...status, milliseconds: Date.now() - startedAt };
      }
    }
    throw new Error(`${command} did not finish within ${Math.round(timeoutMs / 1000)}s`);
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
    // Drain AI/disk INDEX first, then open buffers — F12 must see the latest files.
    try {
      await this.flushPendingDiskIndex();
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
    const pick = "选择 JDK / src.zip";
    vscode.window
      .showWarningMessage(
        "无法跳转到 JDK 类型：未找到 JDK 源码。请为当前工作区配置 Defhop: Jdk Home（只写入项目，不写全局）。",
        pick,
      )
      .then((choice) => {
        if (choice === pick) {
          vscode.commands.executeCommand("defhop.configureJdk");
        }
      });
  }

  async locations(command, document, position, timeoutMs = 3000, includeDeclaration = false) {
    const result = await this.navQuery(command, document, position, timeoutMs, includeDeclaration);
    return result.locations.length ? result.locations : undefined;
  }

  async documentHighlights(document, position) {
    if (!this._ready) return [];
    try {
      await this.flushPendingDiskIndex();
      await this.syncOpenBuffers(document);
      const message = await this._doRequest(
        "HIGHLIGHT",
        [document.uri.fsPath, position.line, position.character],
        5000,
      );
      return this._toLocations(message.locations);
    } catch (error) {
      this.output.appendLine(`[nav] HIGHLIGHT ${error.message}`);
      return [];
    }
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
        await this._doRequest("SHUTDOWN", [], 15000);
      } catch {
        /* process may already be exiting */
      }
    }
    this.dispose();
  }

  dispose() {
    this._deactivating = true;
    if (this._restartTimer) {
      clearTimeout(this._restartTimer);
      this._restartTimer = undefined;
    }
    if (this._restartStableTimer) {
      clearTimeout(this._restartStableTimer);
      this._restartStableTimer = undefined;
    }
    this._rejectAll(new Error("Defhop is shutting down."));
    this._stopProgressAnim();
    this.child?.kill();
    this.child = undefined;
    this._ready = false;
    this.output.dispose();
    this.status.dispose();
    if (this.jdkStatus) this.jdkStatus.dispose();
  }
}

module.exports = { DefhopClient };
