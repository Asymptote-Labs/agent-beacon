package hermessession

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// StatePathForDB scopes a default checkpoint path to the database it collects. The native,
// unsuffixed default database keeps legacyPath for compatibility; every other store starts with
// its own checkpoint. Existing legacy state has no source identity and cannot be safely imported.
func StatePathForDB(dbPath, legacyPath string) (string, error) {
	source, err := canonicalDBPath(dbPath)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		source = strings.ToLower(source)
	}
	home, _ := os.UserHomeDir()
	if root := nativeRootWithSuffix(home, ""); root != "" {
		native, err := canonicalDBPath(filepath.Join(root, "state.db"))
		if err != nil {
			return "", err
		}
		if runtime.GOOS == "windows" {
			native = strings.ToLower(native)
		}
		if source == native {
			return legacyPath, nil
		}
	}
	ext := filepath.Ext(legacyPath)
	return fmt.Sprintf("%s-%x%s", strings.TrimSuffix(legacyPath, ext), sha256.Sum256([]byte(source)), ext), nil
}

func canonicalDBPath(path string) (string, error) {
	if path == "" {
		path = DefaultDBPath()
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve Hermes database %q: %w", path, err)
	}
	return resolveExisting(absolute), nil
}
