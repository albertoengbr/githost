// Package buildinfo carries version metadata injected at build time via
//
//	-ldflags "-X githost.dev/githost/internal/buildinfo.Version=..."
package buildinfo

// Version is the semantic version of the build. Local builds report
// "0.1.0-dev" until ldflags override it.
var Version = "0.1.0-dev"

// Commit is the source revision the binary was built from; "none" for
// local builds.
var Commit = "none"

// Date is the RFC 3339 build timestamp; "unknown" for local builds.
var Date = "unknown"
