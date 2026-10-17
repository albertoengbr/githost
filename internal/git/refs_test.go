package git

import "testing"

func TestValidateRefPath(t *testing.T) {
	valid := []string{
		"refs/heads/main",
		"refs/heads/feature/x-y",
		"refs/tags/v1.0.1",
		"refs/heads/a.b_c",
	}
	for _, ref := range valid {
		if err := ValidateRefPath(ref); err != nil {
			t.Errorf("ValidateRefPath(%q) = %v, want nil", ref, err)
		}
	}
	invalid := []string{
		"",
		"main",                   // not a full ref
		"refs/heads/main/",       // trailing slash
		"refs/heads//x",          // empty segment
		"refs/heads/a..b",        // dotdot
		"refs/heads/.hidden",     // leading dot segment
		"refs/heads/main.lock",   // .lock suffix
		"refs/heads/main@{yday}", // @{ is reserved
		"refs/heads/foo^bar",     // ^ reserved
		"refs/heads/foo:bar",     // colon reserved
		"refs/heads/foo bar",     // space reserved
		"refs/heads/foo~1",       // ~ reserved
		"refs/heads/a.",          // trailing dot
	}
	for _, ref := range invalid {
		if err := ValidateRefPath(ref); err == nil {
			t.Errorf("ValidateRefPath(%q) = nil, want error", ref)
		}
	}
}

func TestValidateBranchName(t *testing.T) {
	if err := ValidateBranchName("main"); err != nil {
		t.Fatalf("main: %v", err)
	}
	if err := ValidateBranchName("release/1.2"); err != nil {
		t.Fatalf("release/1.2: %v", err)
	}
	for _, name := range []string{"", "-tricky", "HEAD", "a..b", "x.lock", "feat@{1}"} {
		if err := ValidateBranchName(name); err == nil {
			t.Errorf("ValidateBranchName(%q) = nil, want error", name)
		}
	}
}
