// Package logging builds the application's structured logger from the
// configuration.
package logging

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"githost.dev/githost/internal/config"
)

// New returns a slog.Logger per the given configuration. Log records are
// written to standard output, which is the recommended integration point
// for service managers (systemd, launchd, Windows Service wrappers).
func New(cfg config.Logging) (*slog.Logger, error) {
	var level slog.Level
	switch strings.ToLower(cfg.Level) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return nil, fmt.Errorf("logging: unsupported level %q", cfg.Level)
	}
	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	switch strings.ToLower(cfg.Format) {
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, opts)
	case "text":
		handler = slog.NewTextHandler(os.Stdout, opts)
	default:
		return nil, fmt.Errorf("logging: unsupported format %q", cfg.Format)
	}
	return slog.New(handler), nil
}
