# GitHost cross-compilation matrix (Windows host).
# Usage: powershell -ExecutionPolicy Bypass -File scripts\cross-compile.ps1
$ErrorActionPreference = "Stop"
Set-Location (Split-Path $PSScriptRoot -Parent)
New-Item -ItemType Directory -Force -Path dist | Out-Null

$commit = (cmd /c "git rev-parse --short HEAD 2>nul") | Select-Object -First 1
if (-not $commit -or $LASTEXITCODE -ne 0) { $commit = "none" }
$version = (cmd /c "git describe --tags --always --dirty 2>nul") | Select-Object -First 1
if (-not $version -or $LASTEXITCODE -ne 0) { $version = "0.1.0-dev" }

$targets = @(
    "linux/amd64",   "linux/arm64",
    "windows/amd64", "windows/arm64",
    "darwin/amd64",  "darwin/arm64",
    "freebsd/amd64", "freebsd/arm64",
    "openbsd/amd64", "openbsd/arm64",
    "netbsd/amd64",  "netbsd/arm64"
)

# Targets known to be unsupported by the pure-Go SQLite dependency:
# modernc.org/libc v1.77.1 has no netbsd/arm64 port. Revisit when the
# driver adds it or when a CGO build on a native toolchain is produced.
# See docs/compatibility/platform-matrix.md.
$unsupported = @{
    "netbsd/arm64" = "modernc.org/libc has no netbsd/arm64 port (see docs/compatibility/platform-matrix.md)"
}

foreach ($t in $targets) {
    if ($unsupported.ContainsKey($t)) {
        Write-Host "==> SKIPPED $t (UNSUPPORTED: $($unsupported[$t]))" -ForegroundColor Yellow
        continue
    }
    $parts = $t.Split("/")
    $goos = $parts[0]
    $goarch = $parts[1]
    $ext = ""
    if ($goos -eq "windows") { $ext = ".exe" }
    Write-Host "==> building $goos/$goarch" -ForegroundColor Cyan
    $env:GOOS = $goos
    $env:GOARCH = $goarch
    $env:CGO_ENABLED = "0"
    go build -trimpath `
        -ldflags "-X githost.dev/githost/internal/buildinfo.Version=$version -X githost.dev/githost/internal/buildinfo.Commit=$commit" `
        -o "dist/githost_${goos}_${goarch}${ext}" ./cmd/githost
    if ($LASTEXITCODE -ne 0) { throw "cross build failed for $t" }
}

Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
Write-Host "Cross build complete." -ForegroundColor Green
