// Package homepath resolves a leading `~` in a runtime's directory override the way the runtimes
// themselves do, so every installer, discovery probe, inventory scan and session reader that
// follows an override such as QWEN_HOME or PI_CODING_AGENT_DIR lands on the same directory.
package homepath

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ExpandTilde expands a bare `~` or a `~/`- or `~\`-rooted path against home. Anything else,
// `~user/...` included, is returned unchanged, which is the runtimes' own rule. Without it, an
// install would create a literal `~` directory beside the working directory and report success
// while the runtime kept reading the real home.
func ExpandTilde(path, home string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path, nil
	}
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("home directory is required to resolve %q", path)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, filepath.FromSlash(path[2:])), nil
}
