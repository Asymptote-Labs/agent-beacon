package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

// Every hook target the CLI advertises must be handled by the install, uninstall, and status
// switches.
//
// The three switches end in `unsupported hook harness %q`, and the target registry is a separate
// list, so adding a runtime to the registry without wiring a switch produces a harness that
// tab-completes, documents, and then refuses to install. Uninstall is the safe probe: it removes
// nothing when nothing is installed, and it fails the same way for an unwired target.
func TestEveryHookTargetIsWired(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cfg := endpointconfig.Config{LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}

	seen := map[string]bool{}
	for _, target := range harnessTargets {
		if target.endpointKind != endpointTargetHook && len(target.hookAliases) == 0 {
			continue
		}
		if seen[target.name] {
			continue
		}
		seen[target.name] = true
		t.Run(target.name, func(t *testing.T) {
			if err := uninstallEndpointHookTarget(target.name, cfg); err != nil &&
				strings.Contains(err.Error(), "unsupported hook harness") {
				t.Errorf("%s is a hook target but is not wired into the CLI: %v", target.name, err)
			}
		})
	}
	if !seen["cline"] {
		t.Error("cline is missing from the hook target registry")
	}
}
