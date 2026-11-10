"use strict";

/**
 * Defhop — VS Code extension entry.
 * Client + helpers live in lib/; this file wires activate/deactivate.
 */

const path = require("path");
const vscode = require("vscode");
const { DefhopClient } = require("./lib/client");
const {
  isJavaWorkspace,
  isIndexedFile,
  watchIndexedDiskFiles,
  isConfigLike,
  emptyProgress,
} = require("./lib/util");

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
    vscode.languages.registerDocumentHighlightProvider(selector, {
      async provideDocumentHighlights(document, position) {
        await client.ensureReady();
        const locs = await client.documentHighlights(document, position);
        if (!locs || !locs.length) return undefined;
        return locs.map(
          (loc) =>
            new vscode.DocumentHighlight(
              loc.range,
              vscode.DocumentHighlightKind.Read,
            ),
        );
      },
    }),
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
      const result = await client._runIndexCommand("REINDEX", 300000);
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
      const result = await client._runIndexCommand("REBUILD", 600000);
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
    vscode.commands.registerCommand("defhop.doctor", async () => {
      const lines = ["Defhop doctor"];
      lines.push(`extension: ${client.context.extension.packageJSON.version}`);
      lines.push(`vscode: ${vscode.version}  platform: ${process.platform}-${process.arch}`);
      lines.push(`roots: ${(client.roots || []).join("; ") || "(none)"}`);
      lines.push(`cacheDir: ${client._cacheDir || "(none)"}`);
      lines.push(`jdk: ${client._jdkHome() || "(unset)"} (${client._jdkSource || "none"})`);
      try {
        await client.ensureReady();
        const status = await client._doRequest("STATUS", [], 10000);
        lines.push(
          `backend: ${status.backendVersion || "?"} protocol=${status.protocolVersion ?? "?"}`,
        );
        lines.push(
          `index: ${status.files || 0} files, ${status.symbols || 0} symbols, ${status.references || 0} references`,
        );
        lines.push(`plugins: ${(status.plugins || []).join(", ") || "none"}`);
        lines.push(`indexing: ${status.indexing ? "yes" : "no"}`);
        const p = status.progress || {};
        lines.push(`progress: ${p.pct ?? "?"}% ${p.phase || ""} ${p.message || ""}`.trim());
      } catch (error) {
        lines.push(`backend: ERROR ${error.message || error}`);
      }
      const text = lines.join("\n");
      for (const line of lines) client.output.appendLine(`[doctor] ${line}`);
      client.output.show(true);
      await vscode.env.clipboard.writeText(text);
      vscode.window.showInformationMessage("Defhop doctor: details in Output and clipboard.");
    }),
    vscode.commands.registerCommand("defhop.configureJdk", async () => {
      const current = client._jdkHome() || "(未设置)";
      const src = client._jdkSource || "none";
      const pick = "选择 JDK / src.zip";
      const clear = "清除项目配置";
      const openWorkspace = "打开工作区设置";
      const choice = await vscode.window.showQuickPick(
        [
          {
            label: pick,
            description: `当前: ${current}`,
            detail: `来源: ${src}（仅项目配置，忽略用户全局）`,
          },
          {
            label: clear,
            description: "删除项目 defhop.jdkHome，再按 env/自动探测重探",
          },
          {
            label: openWorkspace,
            description: "编辑 .vscode/settings.json 中的 defhop.jdkHome",
          },
        ],
        { title: "Defhop JDK" },
      );
      if (!choice) return;
      if (choice.label === openWorkspace) {
        await vscode.commands.executeCommand(
          "workbench.action.openWorkspaceSettings",
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
          await client._runIndexCommand("REINDEX", 300000);
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
          await client._runIndexCommand("REINDEX", 300000);
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
    // Instant overlay — keystrokes async; AI/paste bulk → sync commit (IntelliJ-like)
    vscode.workspace.onDidChangeTextDocument((event) => client.onDocumentEdited(event)),
    vscode.workspace.onDidSaveTextDocument((document) =>
      client.pushBuffer(document, { sync: true }),
    ),
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
    // Incremental: VS Code create/delete + disk watcher (AI Write / external edits)
    vscode.workspace.onDidCreateFiles((event) => {
      client.scheduleDiskIndex(event.files.filter(isIndexedFile).map((u) => u.fsPath));
    }),
    vscode.workspace.onDidDeleteFiles((event) => {
      client.scheduleDiskRemove(event.files.filter(isIndexedFile).map((u) => u.fsPath));
    }),
    ...watchIndexedDiskFiles(client),
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
        await client._runIndexCommand("REINDEX", 300000);
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

