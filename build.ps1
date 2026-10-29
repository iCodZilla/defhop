$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
$GoDir = Join-Path $Root "backend\go"
$OutDir = Join-Path $Root "backend"

Write-Host "==> Building Go backend (multi-platform)"
Push-Location $GoDir
try {
  New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
  $targets = @(
    @{ GOOS = "windows"; GOARCH = "amd64"; Out = "defhop-win32-x64.exe"; Also = "defhop.exe" },
    @{ GOOS = "linux";   GOARCH = "amd64"; Out = "defhop-linux-x64" },
    @{ GOOS = "linux";   GOARCH = "arm64"; Out = "defhop-linux-arm64" },
    @{ GOOS = "darwin";  GOARCH = "amd64"; Out = "defhop-darwin-x64" },
    @{ GOOS = "darwin";  GOARCH = "arm64"; Out = "defhop-darwin-arm64" }
  )
  foreach ($t in $targets) {
    $env:GOOS = $t.GOOS
    $env:GOARCH = $t.GOARCH
    $env:CGO_ENABLED = "0"
    $outPath = Join-Path $OutDir $t.Out
    go build -o $outPath ./cmd/defhop
    if ($LASTEXITCODE -ne 0) { throw "go build failed for $($t.GOOS)/$($t.GOARCH)" }
    Write-Host "    -> $outPath"
    if ($t.Also) {
      Copy-Item -Force $outPath (Join-Path $OutDir $t.Also)
      Write-Host "    -> $(Join-Path $OutDir $t.Also) (alias)"
    }
  }
  Remove-Item Env:GOOS -ErrorAction SilentlyContinue
  Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
  Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue
} finally {
  Pop-Location
}

Write-Host "==> Smoke test (demo)"
$Smoke = Join-Path $Root "scripts\smoke-test.js"
if (Test-Path $Smoke) {
  node $Smoke
  if ($LASTEXITCODE -ne 0) { throw "smoke test failed" }
} else {
  Write-Host "    (no smoke-test.js — skipped)"
}

Write-Host "==> Smoke test (samples: lang/spring/cloud/mybatis/mp/lombok/mapstruct/lambda)"
$SmokeSamples = Join-Path $Root "scripts\smoke-test-samples.js"
if (Test-Path $SmokeSamples) {
  node $SmokeSamples
  if ($LASTEXITCODE -ne 0) { throw "samples smoke test failed" }
} else {
  Write-Host "    (no smoke-test-samples.js — skipped)"
}

Write-Host "==> Done. Backends in: $OutDir"
Write-Host "    Dev docs: BUILD.md"
Write-Host "    Install: code --install-extension .  (or see BUILD.md to package vsix)"
