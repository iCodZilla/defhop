# Changelog

## [1.0.7] - Unreleased

Performance, memory, and navigation UX (dev iterations consolidated into one release).

### Improved
- **Index commands** — `Refresh Index (keep cache)` replaces ambiguous Reindex; new `Clear Cache and Rebuild` wipes disk cache then cold-builds (`REBUILD`).
- **Progress bar** — no longer sticks at 99% on the last plugin step; all plugins ready (or INIT done) snaps to 100% / checkmark.
- **Clear Cache and Rebuild** — cancels background deps (with timeout) and emits progress immediately; no longer freezes on the placeholder 2% step.
- **Java records** — component fields + accessors (`p.x()`) and record body methods index correctly; `permits` on sealed types no longer drops the class body.
- **Deps async** — dependency sources index after status 100% / INIT return (SHUTDOWN still waits).
- **Maven DM versions** — `dependencyManagement` + parent `relativePath` fill versions for unversioned `<dependency>` entries.
- **Dependency sources** — indexes JDK `src.zip` (common `java.*` packages) and Maven/Gradle `*-sources.jar` (plus workspace `.defhop-deps/` / `lib/`), capped; F12 into library APIs without a JVM language server.
- **Binary jar fallback** — when `*-sources.jar` is missing, indexes public types from the binary `.jar` via `.class` stubs (e.g. Apache `RequestConfig`).
- **On-demand library DEF** — F12 on an imported type (e.g. `RequestConfig`) locates the jar in `~/.m2` and materializes a stub even when the type was never batch-indexed (transitive deps).
- **Overload DEF by arity** — `parseDate(x, y)` jumps to the 2-arg overload, not the first same-named method (e.g. the enclosing 1-arg wrapper).
- **Chained member DEF** — walks receiver types for `a.b.c`, `a.getB().getC().run()`, and `new B().foo()` (locals keep declared `TypeKey`).
- **Generic chain DEF** — `list.get(0).foo()` / `Optional.of(x).get().foo()` use `List<T>` / factory arg element types.
- **Overload by arg type** — after arity match, prefer overloads whose params fit simple arg types (literals / locals).
- **JDK status** — status-bar chip shows JDK basename + source; commands `Configure JDK Home` / `Clear Project JDK Home` to re-pick or clear.
- **JDK on-demand DEF** — `SimpleDateFormat` / other `java.*` types resolve from `src.zip` on demand.
- **JDK path priority** — workspace `defhop.jdkHome` → user setting → `JAVA_HOME`/`JDK_HOME` → auto-detect; env/auto results are written into the **workspace** setting so manual setup is usually unnecessary.
- **JDK path prompt** — F12 on `java.*` only when every layer fails; ignores stale index-cache hits when no JDK is resolvable.
- **JDK extract paths** — unify cache dirs (`jdk-<hash>/java/...`), strip module prefixes, repair dir-vs-file clashes so F12 never opens a directory.
- **Multi-root workspaces** — all Java workspace folders are scanned into one index (`INIT` extra roots).
- **Call Hierarchy callers** — incoming calls prefer the enclosing method, not bare file:line.
- **Lombok synthesis** — `@Data`/`@Getter`/`@Setter`/`@Builder`/`@Slf4j` inject accessors, `builder()`/`build()`, and `log` into the index.
- **Workspace Symbol** — Ctrl+T search over types/methods/fields (`SYMBOLS`).
- **Snapshot COW** — delta publish copy-on-writes only touched maps; `BumpGen` forks Files only.
- **Incremental SubtypeIdx** — type SuperKeys add/remove edges without scanning the whole SymTab.
- **Overlay AllMeta** — stale flush no longer copies every open buffer's text up front.
- **Spring config** — no longer retains full YAML/properties text in the plugin; nav falls back to disk/overlay.
- **Incremental snapshot** — `PutFile` / `RemoveFile` patch SymTab / Refs / indexes instead of full rebuild on every buffer publish.
- **Incremental create/delete** — `INDEX` / `REMOVE` instead of full workspace REINDEX.
- **Skip reparse / UPDATE IPC** — same content hash only bumps `Gen`; extension skips identical buffer pushes (undo/redo).
- **Faster F12 sync** — flush current document first; other open tabs share a short budget; prefer nav target in stale-overlay flush.
- **Single-pass Java lexer** — no extra probe tokenize pass.
- **Field index** — `FindField` via per-owner `FieldIdx`.
- **String interning** — PathKey / symbol keys / ref targets share canonical strings.
- **Cold-start memory** — release fingerprint bytes after parse; PathKey-only hash maps.
- **MyBatis Detect** — probe resource/mapper XMLs only (capped); parallel mapper XML index.
- **Outline / Type Hierarchy / Hover / Call Hierarchy** — from the Go index.
- **Definition provider** — returns all candidate locations.
- **Overlay lifecycle** — close drops overlay; deactivate `SHUTDOWN` + cache flush.

## [1.0.6] - 2026-10-27

### Fixed
- **`Result.success` go-to-definition** — type-qualified members (`return Result.success(...)`) now set a proper type QualifierKey; when several classes share the name `Result`, Defhop picks the one that actually declares `success` (instead of failing the jump).

## [1.0.5] - 2026-10-27

### Fixed
- **Realtime cross-file navigation after edits** — adding a symbol (e.g. `Result.success`) no longer requires reindex/restart; navigation sync-publishes stale editor overlays before DEF/REFS/IMPL.
- **Open-buffer sync before jump** — all open Java/XML/YAML/properties buffers are flushed and awaited before a navigation query so defining files in other tabs are visible.
- **Reindex preserves unsaved edits** — after full REINDEX (manual, create, or delete), open buffers are re-pushed onto the overlay.

### Improved
- **Tolerant incremental indexing** — incomplete or broken Java syntax no longer stalls overlay/index generations; partial symbols from the tolerant tokenizer are published so the index keeps updating as you type.
