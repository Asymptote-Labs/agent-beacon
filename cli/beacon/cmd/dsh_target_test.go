package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks/dshtest"
)

// The install -> status -> uninstall round trip for DeepSeek Harness, through the CLI entry points
// rather than the package API.
//
// Wiring a runtime into `beacon endpoint hooks` means five separate switches (install, uninstall,
// status collection, status printing, repair) plus a registry row, and every one of them fails by
// omission rather than by error. TestEveryHookTargetIsWired proves each switch has a case; this
// proves the cases do the thing their name claims -- and here that means both halves of a two-file
// install, either of which is inert on its own.
func TestEndpointHooksInstallAndUninstallDsh(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DSH_HOME", "")
	dshtest.Ready(t, filepath.Join(home, ".dsh"), "0.2.1-rc.2")
	cfg := endpointconfig.Config{LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}

	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })
	endpointOpts.hookLevel = "user"

	hooksPath := filepath.Join(home, ".dsh", "beacon-endpoint-hooks.json")
	patchPath := filepath.Join(home, ".dsh", "cordis.patch.yml")

	if err := installEndpointHookTarget("dsh", cfg); err != nil {
		t.Fatalf("installEndpointHookTarget(dsh) returned error: %v", err)
	}
	hooks, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("install did not write %s: %v", hooksPath, err)
	}
	if !strings.Contains(string(hooks), "--platform dsh") {
		t.Fatalf("the hooks file does not carry the dsh hook command:\n%s", hooks)
	}
	patch, err := os.ReadFile(patchPath)
	if err != nil {
		t.Fatalf("install did not mount the bridge in %s: %v", patchPath, err)
	}
	if !strings.Contains(string(patch), "@deepseek-ai/dsh-hooks-claude-code") {
		t.Fatalf("the patch file does not mount the hook bridge:\n%s", patch)
	}
	if !strings.Contains(string(patch), hooksPath) {
		t.Fatalf("the mount does not point at the hooks file Beacon wrote:\n%s", patch)
	}

	if !endpointhooks.IsDshInstalled(endpointhooks.DshOptions{
		Level: endpointhooks.LevelUser, LogPath: cfg.LogPath, UserMode: true,
	}) {
		t.Fatal("IsDshInstalled = false after installEndpointHookTarget")
	}

	if err := uninstallEndpointHookTarget("dsh", cfg); err != nil {
		t.Fatalf("uninstallEndpointHookTarget(dsh) returned error: %v", err)
	}
	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Fatalf("uninstall left the hooks file behind: %v", err)
	}
	// The one that would break the runtime rather than merely leave litter: an empty or
	// comments-only patch file fails dsh boot, so an uninstall that emptied it must remove it.
	if _, err := os.Stat(patchPath); !os.IsNotExist(err) {
		t.Fatalf("uninstall left a patch file behind; an empty patch layer fails dsh boot: %v", err)
	}
}
