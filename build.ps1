$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
$GoDir = Join-Path $Root "backend\go"
$OutDir = Join-Path $Root "backend"
$Exe = Join-Path $OutDir "defhop.exe"

Write-Host "==> Building Go backend"
Push-Location $GoDir
try {
  New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
  go build -o $Exe ./cmd/defhop
  if ($LASTEXITCODE -ne 0) { throw "go build failed" }
  Write-Host "    -> $Exe"
} finally {
  Pop-Location
}

Write-Host "==> Smoke test"
$Smoke = Join-Path $Root "scripts\smoke-test.js"
if (Test-Path $Smoke) {
  node $Smoke
  if ($LASTEXITCODE -ne 0) { throw "smoke test failed" }
} else {
  Write-Host "    (no smoke-test.js yet — skipped)"
}

Write-Host "==> Done. Backend: $Exe"
Write-Host "    Dev docs: BUILD.md"
Write-Host "    Install: code --install-extension .  (or see BUILD.md to package vsix)"
