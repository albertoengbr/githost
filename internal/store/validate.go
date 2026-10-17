package store

import (
	"fmt"
	"regexp"
	"strings"
)

// usernamePattern: 1-39 chars, lowercase letters/digits with . _ - inside;
// must start and end with a letter or digit.
var usernamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,37}[a-z0-9])?$`)

// repositoryNamePattern: 1-100 chars, letters/digits with . _ - inside.
var repositoryNamePattern = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9._-]{0,98}[a-zA-Z0-9])?$`)

// windowsReservedNames collide with device names on Windows filesystems
// even when an extension is appended (CON.git is invalid). Repositories are
// addressed by UUID on disk, but names appear in URLs and future UI paths,
// so reject them outright.
var windowsReservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true,
	"com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true,
	"lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// ValidateUsername checks a login name. Names are stored lowercase.
func ValidateUsername(name string) error {
	if len(name) < 1 || len(name) > 39 {
		return fmt.Errorf("username %q: must be 1-39 characters", name)
	}
	if !usernamePattern.MatchString(name) {
		return fmt.Errorf("username %q: only lowercase letters, digits, '.', '_' and '-' are allowed (alphanumeric first/last)", name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("username %q: cannot contain '..'", name)
	}
	return nil
}

// NormalizeUsername lowercases a username (the canonical form).
func NormalizeUsername(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// ValidateRepositoryName checks a repository name.
func ValidateRepositoryName(name string) error {
	if len(name) < 1 || len(name) > 100 {
		return fmt.Errorf("repository name %q: must be 1-100 characters", name)
	}
	if !repositoryNamePattern.MatchString(name) {
		return fmt.Errorf("repository name %q: only letters, digits, '.', '_' and '-' are allowed (alphanumeric first/last)", name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("repository name %q: cannot contain '..'", name)
	}
	if strings.HasSuffix(strings.ToLower(name), ".git") {
		return fmt.Errorf("repository name %q: the .git suffix is not part of the name", name)
	}
	base := strings.ToLower(strings.SplitN(name, ".", 2)[0])
	if windowsReservedNames[base] {
		return fmt.Errorf("repository name %q: reserved device name", name)
	}
	return nil
}

// Valid visibilities and member permissions.
const (
	VisibilityPrivate = "private"
	VisibilityPublic  = "public"

	PermissionRead  = "read"
	PermissionWrite = "write"
	PermissionAdmin = "admin"
)

// ValidateVisibility checks a repository visibility value.
func ValidateVisibility(v string) error {
	if v != VisibilityPrivate && v != VisibilityPublic {
		return fmt.Errorf("visibility %q: must be private or public", v)
	}
	return nil
}

// ValidateMemberPermission checks a repository member permission value.
func ValidateMemberPermission(p string) error {
	switch p {
	case PermissionRead, PermissionWrite, PermissionAdmin:
		return nil
	}
	return fmt.Errorf("permission %q: must be read, write or admin", p)
}
