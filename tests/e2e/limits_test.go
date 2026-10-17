package e2e

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"githost.dev/githost/internal/auth"
	"githost.dev/githost/internal/config"
)

// TestPushSizeLimit verifies limits.max_push_bytes end-to-end: a push
// whose pack exceeds the configured cap is rejected by git itself
// (receive.maxInputSize) while small pushes continue to work.
func TestPushSizeLimit(t *testing.T) {
	s := startServerCfg(t, func(cfg *config.Config) {
		cfg.Limits.MaxPushBytes = 2500 // deliberately tiny
	})
	base := s.url()

	owner, token := s.userWithToken(auth.ScopeRepoRead + " " + auth.ScopeRepoWrite)
	s.createRepo(owner, "limits", false)

	work := t.TempDir()
	repoURL := authURL(base, owner, token, owner+"/limits.git")
	clone := filepath.Join(work, "clone")
	mustGit(t, work, "clone", repoURL, clone)

	// A small push fits under the cap.
	writeFile(t, clone, "small.txt", "tiny\n")
	mustGit(t, clone, "add", "small.txt")
	mustGit(t, clone, "commit", "-m", "small push")
	mustGit(t, clone, "push", "origin", "main")

	// A push above the cap is rejected. The payload must be
	// incompressible (random bytes): a repetitive file would deflate
	// below the limit and legitimately pass.
	big := make([]byte, 16*1024)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "big.bin"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, clone, "add", "big.bin")
	mustGit(t, clone, "commit", "-m", "big push that must be rejected")
	out, err := clientGit(t, clone, "push", "origin", "main")
	if err == nil {
		t.Fatalf("oversized push accepted:\n%s", out)
	}
	if !strings.Contains(out, "maximum allowed size") && !strings.Contains(out, "error") {
		t.Fatalf("unexpected rejection output:\n%s", out)
	}
}
