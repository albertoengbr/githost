// Package tests covers operational artifacts: CI workflow syntax and
// packaging files. These are deliverables; breakage here blocks release.
package tests

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestCIWorkflowParses ensures .github/workflows/ci.yml is valid YAML
// with the jobs CI depends on.
func TestCIWorkflowParses(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	var workflow map[string]any
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatalf("ci.yml is not valid YAML: %v", err)
	}
	jobs, ok := workflow["jobs"].(map[string]any)
	if !ok {
		t.Fatal("ci.yml has no jobs")
	}
	for _, want := range []string{"test", "cross", "package"} {
		if _, ok := jobs[want]; !ok {
			t.Errorf("ci.yml lacks job %q", want)
		}
	}
}

// TestPackagingFilesPresent keeps the service-manager deliverables from
// silently disappearing.
func TestPackagingFilesPresent(t *testing.T) {
	for _, path := range []string{
		"packaging/systemd/githost.service",
		"packaging/systemd/README.md",
		"packaging/macos/dev.githost.server.plist",
		"packaging/freebsd/githost",
		"packaging/windows/README.md",
		"packaging/docker/Dockerfile",
		"scripts/package.ps1",
		"scripts/package.sh",
	} {
		info, err := os.Stat(filepath.Join("..", path))
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty", path)
		}
	}
}

// TestSystemdUnitShape sanity-checks the unit file content.
func TestSystemdUnitShape(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "packaging", "systemd", "githost.service"))
	if err != nil {
		t.Fatal(err)
	}
	unit := string(raw)
	for _, want := range []string{
		"ExecStart=/usr/local/bin/githost serve",
		"User=githost",
		"Restart=on-failure",
		"MemoryDenyWriteExecute=true",
		"SystemCallFilter=@system-service",
	} {
		if !contains(unit, want) {
			t.Errorf("systemd unit missing %q", want)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
