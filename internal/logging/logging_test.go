package logging

import (
	"log/slog"
	"testing"

	"githost.dev/githost/internal/config"
)

func TestNewJSONAndText(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		cfg := config.Logging{Level: "debug", Format: format}
		logger, err := New(cfg)
		if err != nil {
			t.Fatalf("New(%s): %v", format, err)
		}
		if logger == nil {
			t.Fatalf("New(%s): nil logger", format)
		}
		// The logger must accept records without panicking.
		logger.Info("probe", "format", format)
	}
}

func TestNewRejectsUnknownValues(t *testing.T) {
	if _, err := New(config.Logging{Level: "loud", Format: "json"}); err == nil {
		t.Fatal("want error for unknown level")
	}
	if _, err := New(config.Logging{Level: "info", Format: "yaml"}); err == nil {
		t.Fatal("want error for unknown format")
	}
}

func TestNewIsSlogLogger(t *testing.T) {
	logger, err := New(config.Logging{Level: "info", Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	var _ *slog.Logger = logger
}
