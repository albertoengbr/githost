// Command githost is the GitHost server binary.
//
// GitHost is a lightweight, self-hosted Git platform for personal and
// small-team use. See docs/ for architecture and status.
package main

import (
	"context"
	"os"

	"githost.dev/githost/internal/cli"
)

func main() {
	os.Exit(cli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
