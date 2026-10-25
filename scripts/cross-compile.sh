#!/bin/sh
# GitHost cross-compilation matrix (POSIX host).
# Usage: ./scripts/cross-compile.sh
set -e
cd "$(dirname "$0")/.."
mkdir -p dist

VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')}
VERSION=${VERSION:-0.1.0-dev}
COMMIT=${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}
LDFLAGS="-X githost.dev/githost/internal/buildinfo.Version=$VERSION -X githost.dev/githost/internal/buildinfo.Commit=$COMMIT"

# netbsd/arm64 is skipped: modernc.org/libc v1.77.1 has no netbsd/arm64
# port. Revisit when the driver adds it or via a CGO build on a native
# toolchain. See docs/compatibility/platform-matrix.md.
for goos in linux windows darwin freebsd openbsd netbsd; do
    for goarch in amd64 arm64; do
        if [ "$goos/$goarch" = "netbsd/arm64" ]; then
            echo "==> SKIPPED netbsd/arm64 (UNSUPPORTED: modernc.org/libc has no netbsd/arm64 port)"
            continue
        fi
        ext=""
        if [ "$goos" = "windows" ]; then ext=".exe"; fi
        echo "==> building $goos/$goarch"
        GOOS=$goos GOARCH=$goarch CGO_ENABLED=0 \
            go build -trimpath -ldflags "$LDFLAGS" \
            -o "dist/githost_${goos}_${goarch}${ext}" ./cmd/githost
    done
done

echo "Cross build complete."
