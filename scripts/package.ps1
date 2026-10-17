# GitHost packaging script (Windows host).
# Builds the full matrix, then per-target tar.gz archives + SHA256SUMS.
# Usage: powershell -ExecutionPolicy Bypass -File scripts\package.ps1 [-Version X.Y.Z]
param(
    [string]$Version = ""
)
$ErrorActionPreference = "Stop"
Set-Location (Split-Path $PSScriptRoot -Parent)

if (-not $Version) {
    $Version = (git describe --tags --always --dirty 2>$null)
    if (-not $Version) { $Version = "0.0.0-dev" }
}

# Build the matrix (reuses the same rules as cross-compile.ps1).
& (Join-Path $PSScriptRoot "cross-compile.ps1")
if ($LASTEXITCODE -ne 0) { throw "cross build failed" }

$commit = (git rev-parse --short HEAD 2>$null)
if (-not $commit) { $commit = "none" }

New-Item -ItemType Directory -Force -Path dist | Out-Null
$sums = @()

foreach ($os in "linux", "windows", "darwin", "freebsd", "openbsd", "netbsd") {
    foreach ($arch in "amd64", "arm64") {
        $name = "githost_${os}_${arch}"
        $bin = "dist/$name"
        if ($os -eq "windows") { $bin += ".exe" }
        if (-not (Test-Path $bin)) { continue } # documented skips (netbsd/arm64)

        $archive = "dist/githost_${Version}_${os}_${arch}.tar.gz"
        if ($os -eq "windows") {
            # bsdtar handles Windows paths; stage a clean folder so the
            # archive contains the binary named githost(.exe).
            $stage = New-Item -ItemType Directory -Force -Path "dist/stage-$name"
            Copy-Item $bin (Join-Path $stage.FullName "githost.exe") -Force
            Copy-Item LICENSE (Join-Path $stage.FullName "LICENSE") -Force
            Copy-Item README.md (Join-Path $stage.FullName "README.md") -Force
            tar -czf $archive -C $stage.FullName githost.exe LICENSE README.md
            Remove-Item -Recurse -Force $stage.FullName
        } else {
            $stage = New-Item -ItemType Directory -Force -Path "dist/stage-$name"
            Copy-Item $bin (Join-Path $stage.FullName "githost") -Force
            Copy-Item LICENSE (Join-Path $stage.FullName "LICENSE") -Force
            Copy-Item README.md (Join-Path $stage.FullName "README.md") -Force
            tar -czf $archive -C $stage.FullName githost LICENSE README.md
            Remove-Item -Recurse -Force $stage.FullName
        }
        $sha = (Get-FileHash $archive -Algorithm SHA256).Hash.ToLower()
        $sums += "$sha  $(Split-Path $archive -Leaf)"
        Write-Host "packaged $(Split-Path $archive -Leaf)" -ForegroundColor Green
    }
}

$sums | Set-Content "dist/SHA256SUMS.txt"
Write-Host "wrote dist/SHA256SUMS.txt ($($sums.Count) archives)" -ForegroundColor Green
