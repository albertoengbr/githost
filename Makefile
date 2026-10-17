# GitHost build orchestration (POSIX). On Windows prefer scripts\verify.ps1.
BINARY  := githost
PKG     := githost.dev/githost
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X $(PKG)/internal/buildinfo.Version=$(VERSION) \
           -X $(PKG)/internal/buildinfo.Commit=$(COMMIT) \
           -X $(PKG)/internal/buildinfo.Date=$(DATE)

.PHONY: build test race vet fmt verify cross clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/githost

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

verify: vet test race

cross:
	@mkdir -p dist
	@for goos in linux windows darwin freebsd openbsd netbsd; do \
		for goarch in amd64 arm64; do \
			if [ "$$goos/$$goarch" = "netbsd/arm64" ]; then \
				echo "==> SKIPPED netbsd/arm64 (UNSUPPORTED: modernc.org/libc has no netbsd/arm64 port)"; \
				continue; \
			fi; \
			ext=""; \
			if [ "$$goos" = "windows" ]; then ext=".exe"; fi; \
			echo "==> building $$goos/$$goarch"; \
			GOOS=$$goos GOARCH=$$goarch CGO_ENABLED=0 \
				go build -trimpath -ldflags "$(LDFLAGS)" \
				-o dist/$(BINARY)_$${goos}_$${goarch}$${ext} ./cmd/githost || exit 1; \
		done; \
	done

clean:
	rm -rf bin dist
