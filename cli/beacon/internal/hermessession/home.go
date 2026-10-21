package hermessession

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
)

// HomeDir returns the directory a plain `hermes` invocation reads config.yaml and state.db from,
// resolved the way the Hermes launcher resolves it (_apply_profile_override in hermes_cli/main.py,
// over get_default_hermes_root in hermes_constants.py):
//
//   - The base is HERMES_HOME (see HomeOverride), or the native root: ~/.hermes plus
//     HERMES_DATA_DIR_SUFFIX, or %LOCALAPPDATA%\hermes plus the suffix on Windows.
//   - A HERMES_HOME that already names a profile, <root>/profiles/<name>, is used as it is.
//   - Otherwise a sticky profile, the name `hermes profile use` writes to <root>/active_profile,
//     selects <root>/profiles/<name>. The profile root is the native root when HERMES_HOME is
//     unset or inside it, and HERMES_HOME itself when it points elsewhere.
//
// home is the user's home directory, for the native root and a leading ~ in HERMES_HOME. A profile
// chosen for one invocation with `hermes -p` leaves no trace on disk, so it is not followed. An
// active_profile naming an invalid or missing profile is an error, because Hermes refuses to start
// with one rather than falling back to the default profile.
func HomeDir(home string) (string, error) {
	_, dir, err := resolveHome(home)
	return dir, err
}

// HomeOverride returns HERMES_HOME as Hermes reads it, as an absolute path: trimmed, with $VAR and
// ${VAR} expanded as Python's os.path.expandvars does and a leading ~ expanded against home (the
// current user's when home is empty). It is empty when HERMES_HOME is unset or blank.
func HomeOverride(home string) (string, error) {
	override := strings.TrimSpace(os.Getenv("HERMES_HOME"))
	if override == "" {
		return "", nil
	}
	path := hermesExpandVars(override)
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home != "" {
		if path == "~" {
			path = home
		} else if strings.HasPrefix(path, "~/") || (runtime.GOOS == "windows" && strings.HasPrefix(path, `~\`)) {
			path = filepath.Join(home, path[2:])
		}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve HERMES_HOME %q: %w", override, err)
	}
	return absolute, nil
}

// DefaultDBPath is the state.db in HomeDir. When the active profile cannot be used, Hermes writes
// nothing new anywhere, so status, sync and handoff keep reading the base directory's store, which
// holds whatever Hermes wrote there before.
func DefaultDBPath() string {
	home, _ := os.UserHomeDir()
	base, dir, err := resolveHome(strings.TrimSpace(home))
	if err != nil {
		dir = base
	}
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "hermes")
	}
	return filepath.Join(dir, "state.db")
}

// resolveHome returns the directory Hermes uses before profiles are considered, and the one it
// uses after.
func resolveHome(home string) (base, dir string, err error) {
	override, err := HomeOverride(home)
	if err != nil {
		return "", "", err
	}
	if override != "" && isProfileDir(override) {
		return override, override, nil
	}
	root := nativeRoot(home)
	if override != "" && (root == "" || !pathWithin(override, root)) {
		root = override
	}
	if root == "" {
		return "", "", errors.New("resolve Hermes home: home directory is empty")
	}
	base = override
	if base == "" {
		base = root
	}
	name, ok := activeProfile(root)
	if !ok {
		return base, base, nil
	}
	dir, err = profileDir(root, name)
	return base, dir, err
}

// nativeRoot is Hermes's platform default home, or empty when there is no home to put it in.
func nativeRoot(home string) string {
	name := "hermes" + os.Getenv("HERMES_DATA_DIR_SUFFIX")
	if runtime.GOOS == "windows" {
		if base := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); base != "" {
			return filepath.Join(base, name)
		}
		if home == "" {
			return ""
		}
		return filepath.Join(home, "AppData", "Local", name)
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, "."+name)
}

// DefaultProfile is the name Hermes gives the profile whose home is the root itself.
const DefaultProfile = "default"

// Profile names the Hermes profile whose home is dir: <root>/profiles/<name> is <name>, and any
// other home is the default profile.
func Profile(dir string) string {
	if isProfileDir(dir) {
		return filepath.Base(dir)
	}
	return DefaultProfile
}

// isProfileDir is the launcher's test for a HERMES_HOME that already selects a profile: its parent
// directory is named "profiles".
func isProfileDir(path string) bool {
	return filepath.Base(filepath.Dir(path)) == "profiles"
}

// pathWithin reports whether path is root or inside it, comparing the paths with symlinks resolved
// as Python's non-strict Path.resolve does.
func pathWithin(path, root string) bool {
	rel, err := filepath.Rel(resolveExisting(root), resolveExisting(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveExisting resolves symlinks in the longest prefix of path that exists and keeps the rest as
// written.
func resolveExisting(path string) string {
	rest := ""
	for dir := filepath.Clean(path); ; dir = filepath.Dir(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest)
		}
		if filepath.Dir(dir) == dir {
			return path
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

// activeProfile reads the sticky profile name from <root>/active_profile. A missing, unreadable or
// empty file, or the name "default", selects no profile, as in Hermes.
func activeProfile(root string) (string, bool) {
	data, err := sessionfs.ReadFile(filepath.Join(root, "active_profile"))
	if err != nil || !utf8.Valid(data) {
		return "", false
	}
	name := strings.TrimSpace(string(data))
	if name == "" || name == "default" {
		return "", false
	}
	return name, true
}

var hermesProfileName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// hermesReservedProfiles are the names Hermes's validate_profile_name rejects.
var hermesReservedProfiles = map[string]bool{"hermes": true, "test": true, "tmp": true, "root": true, "sudo": true}

// profileDir is Hermes's resolve_profile_env: the name is matched case-insensitively, "default" is
// the root itself, and any other profile must be a valid name with an existing directory.
func profileDir(root, name string) (string, error) {
	canonical := strings.ToLower(name)
	if canonical == "default" {
		return root, nil
	}
	activePath := filepath.Join(root, "active_profile")
	if !hermesProfileName.MatchString(canonical) || hermesReservedProfiles[canonical] {
		return "", fmt.Errorf("Hermes active profile %q in %s is not a valid profile name", name, activePath)
	}
	dir := filepath.Join(root, "profiles", canonical)
	if info, err := sessionfs.Stat(dir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("Hermes active profile %q in %s does not exist: %s is not a directory", name, activePath, dir)
	}
	return dir, nil
}

var hermesEnvReference = regexp.MustCompile(`\$\{([^}]*)\}|\$([A-Za-z0-9_]+)`)

// hermesExpandVars expands $VAR and ${VAR} like Python's os.path.expandvars, which Hermes
// uses: references to unset variables are left as written.
func hermesExpandVars(path string) string {
	return hermesEnvReference.ReplaceAllStringFunc(path, func(reference string) string {
		parts := hermesEnvReference.FindStringSubmatch(reference)
		name := parts[1]
		if parts[2] != "" {
			name = parts[2]
		}
		if value, ok := os.LookupEnv(name); ok {
			return value
		}
		return reference
	})
}
