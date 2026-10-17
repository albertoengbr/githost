package git

import (
	"fmt"
	"strings"
)

// ValidateRefPath reports whether ref is a safe, well-formed full ref name
// (e.g. refs/heads/main). It encodes the rules of git-check-ref-format(1)
// in-process: callers must pass user-influenced names through this (or
// through git check-ref-format) before using them anywhere near a
// filesystem path or a subprocess argument.
func ValidateRefPath(ref string) error {
	if ref == "" {
		return fmt.Errorf("ref: empty")
	}
	if !strings.HasPrefix(ref, "refs/") {
		return fmt.Errorf("ref %q: must start with refs/", ref)
	}
	if strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") {
		return fmt.Errorf("ref %q: cannot end with '/' or '.'", ref)
	}
	if strings.HasSuffix(ref, ".lock") {
		return fmt.Errorf("ref %q: cannot end with .lock", ref)
	}
	if strings.Contains(ref, "//") {
		return fmt.Errorf("ref %q: cannot contain '//'", ref)
	}
	if strings.Contains(ref, "..") {
		return fmt.Errorf("ref %q: cannot contain '..'", ref)
	}
	if strings.Contains(ref, "@{") {
		return fmt.Errorf("ref %q: cannot contain '@{'", ref)
	}
	for _, r := range ref {
		// Control characters and space are not allowed in refs.
		if r < 0x20 || r == 0x20 || r == 0x7f {
			return fmt.Errorf("ref %q: control characters and spaces are not allowed", ref)
		}
		if strings.ContainsRune("~^:?*[\\", r) {
			return fmt.Errorf("ref %q: %q is not allowed in refs", ref, r)
		}
	}
	for _, seg := range strings.Split(ref, "/") {
		if seg == "" {
			return fmt.Errorf("ref %q: empty path segment", ref)
		}
		if strings.HasPrefix(seg, ".") {
			return fmt.Errorf("ref %q: segment %q starts with '.'", ref, seg)
		}
	}
	return nil
}

// ValidateBranchName validates a short branch name (e.g. "main") for
// creation or default-branch configuration.
func ValidateBranchName(name string) error {
	if name == "" {
		return fmt.Errorf("branch name: empty")
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("branch name %q: cannot start with '-'", name)
	}
	switch name {
	case "HEAD", "FETCH_HEAD", "ORIG_HEAD", "MERGE_HEAD":
		return fmt.Errorf("branch name %q: reserved", name)
	}
	return ValidateRefPath("refs/heads/" + name)
}
