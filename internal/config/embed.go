package config

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ExampleYAML is the configuration template written by `githost init`.
//
//go:embed config.example.yaml
var ExampleYAML []byte

// EnsureFile writes the embedded example configuration to path when the file
// does not exist yet. It returns whether the file was created. It is an
// error if the file exists but cannot be inspected, or cannot be written.
func EnsureFile(path string) (bool, error) {
	_, statErr := os.Stat(path)
	switch {
	case statErr == nil:
		return false, nil
	case !errors.Is(statErr, fs.ErrNotExist):
		return false, fmt.Errorf("inspect config %s: %w", path, statErr)
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return false, fmt.Errorf("create config directory: %w", err)
		}
	}
	if err := os.WriteFile(path, ExampleYAML, 0o600); err != nil {
		return false, fmt.Errorf("write config %s: %w", path, err)
	}
	return true, nil
}
