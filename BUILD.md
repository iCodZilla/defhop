# Defhop — Build & Develop

Developer notes (not shown on the Marketplace). Product docs live in `README.md`.

## Requirements

- Go 1.22+
- Node.js (smoke test / `vsce`)

## Build backend

```powershell
.\build.ps1
```

Produces `backend\defhop.exe` and runs:

- `scripts\smoke-test.js` — classic `testdata/demo` fixtures  
- `scripts\smoke-test-samples.js` — `testdata/samples` (Java lang + lambda, Spring, Spring Cloud OpenFeign, MyBatis, MyBatis-Plus, Lombok, MapStruct, dependency sources.jar)

## Performance numbers (for README)

```powershell
node .\scripts\bench-init-only.js
```

INIT + DEF samples against local mid/large sample workspaces. **README must describe scale only** (file count / module count)—do not publish real project names.

Manual:

```powershell
cd backend\go
go build -o ..\defhop.exe ./cmd/defhop
```

## Package VSIX

```powershell
npx @vscode/vsce package --out dist/defhop-1.0.0.vsix --allow-missing-repository --baseContentUrl "https://example.invalid/defhop" --baseImagesUrl "https://example.invalid/defhop"
```

Then restore relative image paths in the VSIX `readme.md` from the repo `README.md` (Marketplace / sideload need `media/...` relatives). Or install from folder:

```powershell
code --install-extension .
```

## Install (dev)

- `Developer: Install Extension from Location…` → this repo root  
- or `code --install-extension .\dist\defhop-1.0.0.vsix`  
- then **Reload Window**

## Publish

Publisher id: `vg0x00` (see `package.json`).

```powershell
npx @vscode/vsce login vg0x00
npx @vscode/vsce publish
```

## Extending plugins

1. Add `backend/go/internal/plugins/<name>` implementing `plugin.Plugin`
2. Register in `cmd/defhop/main.go`
3. Detect keeps unused plugins off the heap

## Layout

| Path | Role |
|------|------|
| `extension.js` | VS Code host |
| `package.json` | Manifest / marketplace metadata |
| `README.md` | Marketplace product page |
| `backend/go` | Index + navigation engine |
| `backend/defhop.exe` | Bundled binary |
| `media/` | Icon, banner, donation QR |
| `scripts/smoke-test.js` | Navigation smoke checks |
