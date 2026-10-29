# Changelog

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
