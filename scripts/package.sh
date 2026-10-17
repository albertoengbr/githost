#!/bin/sh
# GitHost packaging script (POSIX host).
# Builds the matrix, then per-target tar.gz archives + SHA256SUMS.
# Usage: VERSION=x.y.z ./scripts/package.sh
set -e
cd "$(dirname "$0")/.."

VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)}
COMMIT=${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}

./scripts/cross-compile.sh

mkdir -p dist
: > dist/SHA256SUMS.txt

for goos in linux windows darwin freebsd openbsd netbsd; do
    for goarch in amd64 arm64; do
        name="githost_${goos}_${goarch}"
        bin="dist/$name"
        ext=""
        if [ "$goos" = "windows" ]; then
            ext=".exe"
            bin="$bin.exe"
        fi
        [ -f "$bin" ] || continue # documented skips (netbsd/arm64)

        archive="dist/githost_${VERSION}_${goos}_${goarch}.tar.gz"
        stage="dist/stage-$name"
        rm -rf "$stage"
        mkdir -p "$stage"
        cp "$bin" "$stage/githost$ext"
        cp LICENSE README.md "$stage/"
        tar -czf "$archive" -C "$stage" "githost$ext" LICENSE README.md
        rm -rf "$stage"

        sha=$(sha256sum "$archive" | cut -d' ' -f1)
        echo "$sha  $(basename "$archive")" >> dist/SHA256SUMS.txt
        echo "packaged $(basename "$archive")"
    done
done

echo "wrote dist/SHA256SUMS.txt"
