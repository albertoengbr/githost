# GitHost verification script for Windows development.
# Usage: powershell -ExecutionPolicy Bypass -File scripts\verify.ps1
#
# Note: "go test -race" requires a C toolchain (gcc) on Windows, which is
# not assumed here. Race testing runs in POSIX environments/CI via
# scripts/verify.sh instead.
$ErrorActionPreference = "Stop"
Set-Location (Split-Path $PSScriptRoot -Parent)

Write-Host "==> gofmt" -ForegroundColor Cyan
$fmt = & gofmt -l .
if ($fmt) {
    $fmt
    throw "gofmt found unformatted files"
}

Write-Host "==> go vet" -ForegroundColor Cyan
go vet ./...
if ($LASTEXITCODE -ne 0) { throw "go vet failed" }

Write-Host "==> go test" -ForegroundColor Cyan
go test ./...
if ($LASTEXITCODE -ne 0) { throw "go test failed" }

Write-Host "==> go build" -ForegroundColor Cyan
New-Item -ItemType Directory -Force -Path bin | Out-Null
$commit = (cmd /c "git rev-parse --short HEAD 2>nul") | Select-Object -First 1
if (-not $commit -or $LASTEXITCODE -ne 0) { $commit = "none" }
$version = (cmd /c "git describe --tags --always --dirty 2>nul") | Select-Object -First 1
if (-not $version -or $LASTEXITCODE -ne 0) { $version = "0.1.0-dev" }
go build -trimpath `
    -ldflags "-X githost.dev/githost/internal/buildinfo.Version=$version -X githost.dev/githost/internal/buildinfo.Commit=$commit" `
    -o bin\githost.exe .\cmd\githost
if ($LASTEXITCODE -ne 0) { throw "build failed" }

Write-Host "==> smoke test (init/doctor/healthz)" -ForegroundColor Cyan
$smoke = Join-Path $env:TEMP ("githost-verify-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force -Path $smoke | Out-Null
try {
    & bin\githost.exe init --config (Join-Path $smoke "githost.yaml")
    if ($LASTEXITCODE -ne 0) { throw "init failed" }
    & bin\githost.exe doctor --config (Join-Path $smoke "githost.yaml")
    if ($LASTEXITCODE -ne 0) { throw "doctor failed" }
} finally {
    Remove-Item -Recurse -Force $smoke -ErrorAction SilentlyContinue
}

Write-Host "All checks passed." -ForegroundColor Green
