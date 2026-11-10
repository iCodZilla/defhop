"use strict";

const vscode = require("vscode");
const crypto = require("crypto");
const fs = require("fs");
const path = require("path");

let nextRequestId = 1;

// Must match backend/go/cmd/defhop/main.go protocolVersion. A stale binary is
// rejected explicitly instead of failing later with misparsed replies.
const PROTOCOL_VERSION = 1;
// Mirrors maxIndexedFileBytes in the backend. Larger buffers are not shipped
// over the line protocol (the backend would reject the request line).
const MAX_OVERLAY_TEXT_BYTES = 8 * 1024 * 1024;

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

/** Platform backend names (Node arch: x64/arm64). Never prefer .exe on Linux/macOS/WSL. */
function backendBinaryCandidates(extensionPath) {
  const dir = path.join(extensionPath, "backend");
  const plat = process.platform;
  const arch = process.arch;
  const names = [];
  if (plat === "win32") {
    names.push(`defhop-win32-${arch}.exe`, "defhop.exe");
  } else if (plat === "linux") {
    names.push(`defhop-linux-${arch}`, "defhop");
  } else if (plat === "darwin") {
    names.push(`defhop-darwin-${arch}`, "defhop");
  } else {
    names.push("defhop");
  }
  return names.map((n) => path.join(dir, n));
}

function ensureUnixExecutable(filePath) {
  if (process.platform === "win32" || !filePath) return;
  try {
    fs.chmodSync(filePath, 0o755);
  } catch {
    /* ignore — spawn will surface EACCES if still not runnable */
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

function pathsEqual(a, b) {
  if (!a || !b) return false;
  const na = path.normalize(a);
  const nb = path.normalize(b);
  return process.platform === "win32" ? na.toLowerCase() === nb.toLowerCase() : na === nb;
}

function openDocumentForPath(fsPath) {
  for (const doc of vscode.workspace.textDocuments) {
    if (doc.uri.scheme === "file" && pathsEqual(doc.uri.fsPath, fsPath)) return doc;
  }
  return undefined;
}

/** Watch disk creates/edits/deletes (AI Write, git checkout) — not only VS Code createFiles. */
function watchIndexedDiskFiles(client) {
  const disposables = [];
  const pattern = "**/*.{java,xml,yml,yaml,properties}";
  const folders = vscode.workspace.workspaceFolders || [];
  const watchers =
    folders.length === 0
      ? [vscode.workspace.createFileSystemWatcher(pattern)]
      : folders.map((folder) =>
          vscode.workspace.createFileSystemWatcher(new vscode.RelativePattern(folder, pattern)),
        );
  for (const watcher of watchers) {
    disposables.push(watcher);
    disposables.push(
      watcher.onDidCreate((uri) => {
        if (isIndexedFile(uri)) client.scheduleDiskIndex([uri.fsPath]);
      }),
      watcher.onDidChange((uri) => {
        if (isIndexedFile(uri)) client.scheduleDiskIndex([uri.fsPath]);
      }),
      watcher.onDidDelete((uri) => {
        if (isIndexedFile(uri)) client.scheduleDiskRemove([uri.fsPath]);
      }),
    );
  }
  disposables.push(
    vscode.workspace.onDidRenameFiles((event) => {
      const removed = [];
      const added = [];
      for (const f of event.files) {
        if (isIndexedFile(f.oldUri)) removed.push(f.oldUri.fsPath);
        if (isIndexedFile(f.newUri)) added.push(f.newUri.fsPath);
      }
      if (removed.length) client.scheduleDiskRemove(removed);
      if (added.length) client.scheduleDiskIndex(added);
    }),
  );
  return disposables;
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


module.exports = {
  get nextRequestId() {
    return nextRequestId;
  },
  set nextRequestId(v) {
    nextRequestId = v;
  },
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
  watchIndexedDiskFiles,
  isConfigLike,
  JAVA_PROJECT_MARKERS,
  pathExists,
  isJavaProjectRoot,
  isJavaWorkspace,
  PLUGIN_LABELS,
  emptyProgress,
  stateIcon,
  stateText,
};
