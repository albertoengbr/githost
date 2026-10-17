# GitHost container image: one static binary plus git.
# Build:  docker build -f packaging/docker/Dockerfile -t githost:tag .
# Run:    docker run -d -p 3000:3000 -p 2222:2222 \
#           -v githost-data:/data githost:tag
#
# /data holds the whole instance (config, db, secret, repositories,
# backups). Persist it with a named volume or a bind mount.
#
# IMPORTANT: when publishing ports with -p, set `server.address:
# "0.0.0.0"` in githost.yaml — the default binds 127.0.0.1 inside the
# container and is unreachable through the published port.

FROM golang:1.26-alpine AS build
ARG VERSION=docker-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X githost.dev/githost/internal/buildinfo.Version=${VERSION}" \
    -o /out/githost ./cmd/githost

FROM alpine:3.20
RUN apk add --no-cache git ca-certificates tzdata \
 && addgroup -S githost \
 && adduser -S -G githost -h /data -s /sbin/nologin githost
COPY --from=build /out/githost /usr/local/bin/githost
USER githost
WORKDIR /data
VOLUME ["/data"]
EXPOSE 3000 2222
ENTRYPOINT ["githost"]
CMD ["serve", "--config", "/data/githost.yaml"]
