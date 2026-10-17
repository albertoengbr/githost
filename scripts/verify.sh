#!/bin/sh
# GitHost verification script (POSIX: Linux, macOS, *BSD).
# Usage: ./scripts/verify.sh
set -e
cd "$(dirname "$0")/.."

echo "==> gofmt"
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
    echo "$unformatted"
    echo "gofmt found unformatted files" >&2
    exit 1
fi

echo "==> go vet"
go vet ./...

echo "==> go test"
go test ./...

echo "==> go test -race"
go test -race ./...

echo "All checks passed."
