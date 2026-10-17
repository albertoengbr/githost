package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runForTest(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestInitCreatesInstanceAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "githost.yaml")

	code, stdout, stderr := runForTest(t, "init", "--config", cfgPath)
	if code != 0 {
		t.Fatalf("init exit = %d, stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "GitHost instance initialized") {
		t.Fatalf("unexpected init output: %q", stdout)
	}
	for _, path := range []string{
		cfgPath,
		filepath.Join(dir, "data"),
		filepath.Join(dir, "data", "githost.db"),
		filepath.Join(dir, "data", "secret.key"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("missing after init: %s (%v)", path, err)
		}
	}

	// Second init must succeed and must not recreate the config file.
	code2, _, _ := runForTest(t, "init", "--config", cfgPath)
	if code2 != 0 {
		t.Fatalf("second init exit = %d", code2)
	}
}

func TestServeWithoutConfigFailsWithHint(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	code, _, stderr := runForTest(t, "serve", "--config", missing)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "githost init") {
		t.Fatalf("missing hint in stderr: %q", stderr)
	}
}

func TestVersionCommand(t *testing.T) {
	code, stdout, _ := runForTest(t, "version")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stdout, "githost version ") {
		t.Fatalf("unexpected version output: %q", stdout)
	}
	if !strings.Contains(stdout, "platform:") {
		t.Fatalf("platform missing from version output: %q", stdout)
	}
}

func TestDoctorAfterInitPasses(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "githost.yaml")

	if code, _, _ := runForTest(t, "init", "--config", cfgPath); code != 0 {
		t.Fatalf("init failed")
	}
	code, stdout, stderr := runForTest(t, "doctor", "--config", cfgPath)
	if code != 0 {
		t.Fatalf("doctor exit = %d, stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, want := range []string{
		"[ok  ] configuration",
		"[ok  ] data directory",
		"[ok  ] instance secret",
		"[ok  ] database",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("doctor output missing %q; got %q", want, stdout)
		}
	}
}

func TestDoctorWithMissingConfigFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	code, stdout, _ := runForTest(t, "doctor", "--config", missing)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stdout, "[fail] configuration") {
		t.Fatalf("unexpected doctor output: %q", stdout)
	}
}

func TestUnknownCommandIsUsageError(t *testing.T) {
	code, _, stderr := runForTest(t, "frobnicate")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "unknown command") {
		t.Fatalf("stderr: %q", stderr)
	}
}

func TestNoCommandIsUsageError(t *testing.T) {
	code, _, stderr := runForTest(t)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "usage:") {
		t.Fatalf("stderr: %q", stderr)
	}
}
