package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
)

// The install -> status -> uninstall round trip for Kimi Code, through the CLI entry points rather
// than the package API.
//
// Wiring a runtime into `beacon endpoint hooks` means five separate switches (install, uninstall,
// status collection, status printing, repair) plus a registry row, and every one of them fails by
// omission rather than by error. TestEveryHookTargetIsWired proves each switch has a case; this
// proves the cases do the thing their name claims -- and here that means writing into a file that
// holds the user's API keys and giving it back unchanged.

func kimiCLIHome(t *testing.T) (home, configPath string, cfg endpointconfig.Config) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("KIMI_CODE_HOME", "")

	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })
	endpointOpts.hookLevel = "user"

	return home,
		filepath.Join(home, ".kimi-code", "config.toml"),
		endpointconfig.Config{LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}
}

func TestEndpointHooksInstallAndUninstallKimi(t *testing.T) {
	_, configPath, cfg := kimiCLIHome(t)

	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	original := "# mine\ndefault_model = \"k2\"\n\n[providers.kimi]\napi_key = \"sk-do-not-touch\"\n"
	if err := os.WriteFile(configPath, []byte(original), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := installEndpointHookTarget("kimi", cfg); err != nil {
		t.Fatalf("installEndpointHookTarget(kimi) returned error: %v", err)
	}
	written, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("install did not write %s: %v", configPath, err)
	}
	if !strings.Contains(string(written), "--platform kimi") {
		t.Fatalf("the config does not carry the kimi hook command:\n%s", written)
	}
	// The property this integration is built around: the user's own file is appended to, never
	// rewritten.
	if !strings.HasPrefix(string(written), original) {
		t.Fatalf("install did not leave the user's config as a prefix:\n%s", written)
	}

	if !endpointhooks.IsKimiInstalled(endpointhooks.KimiOptions{
		Level: endpointhooks.LevelUser, LogPath: cfg.LogPath, UserMode: true,
	}) {
		t.Fatal("IsKimiInstalled = false after installEndpointHookTarget")
	}

	if err := uninstallEndpointHookTarget("kimi", cfg); err != nil {
		t.Fatalf("uninstallEndpointHookTarget(kimi) returned error: %v", err)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("uninstall removed the user's config file entirely: %v", err)
	}
	if string(after) != original {
		t.Fatalf("uninstall did not restore the config:\n--- want ---\n%s\n--- got ---\n%s", original, after)
	}
}

// A project-scope install is refused with its reason rather than silently becoming a machine-wide
// one. An operator asking for a repository-scoped install must not get something else.
func TestEndpointHooksKimiRefusesProjectLevel(t *testing.T) {
	home, _, cfg := kimiCLIHome(t)
	endpointOpts.hookLevel = "project"

	err := installEndpointHookTarget("kimi", cfg)
	if err == nil {
		t.Fatal("a project-level Kimi Code install succeeded; there is no project-scoped hook config")
	}
	if !strings.Contains(err.Error(), "user scope") {
		t.Fatalf("error %q does not tell the operator what to do instead", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".kimi-code")); !os.IsNotExist(statErr) {
		t.Fatal("the refused install wrote into the Kimi Code data root anyway")
	}
}

// Discovery reads the same file the installer writes, so it has to agree with it about what
// "installed" means -- and about the one document shape the installer refuses, which a user is
// better told about before they hit it than after.
func TestKimiDiscoveryTracksTheInstall(t *testing.T) {
	_, configPath, cfg := kimiCLIHome(t)
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	kimiRow := func(t *testing.T) (status, message string) {
		t.Helper()
		for _, h := range harness.DiscoverAll() {
			if h.Name == "kimi_code" {
				return string(h.TelemetryStatus), h.Message
			}
		}
		t.Fatal("kimi_code is missing from endpoint discovery")
		return "", ""
	}

	if err := os.WriteFile(configPath, []byte("default_model = \"k2\"\n"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if status, message := kimiRow(t); status != "disabled" {
		t.Fatalf("discovery = %q (%s) for a config with no Beacon hooks, want disabled", status, message)
	}

	if err := installEndpointHookTarget("kimi", cfg); err != nil {
		t.Fatalf("install: %v", err)
	}
	if status, message := kimiRow(t); status != "enabled" {
		t.Fatalf("discovery = %q (%s) after an install, want enabled", status, message)
	}

	// The shape the installer refuses. Reporting it at discovery time turns a refusal the user
	// meets later into something they can fix first.
	if err := os.WriteFile(configPath, []byte("hooks = [{ event = \"Stop\", command = \"x\" }]\n"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	status, message := kimiRow(t)
	if status != "misconfigured" || !strings.Contains(message, "inline array") {
		t.Fatalf("discovery = %q (%s) for an inline hooks array, want misconfigured naming the shape", status, message)
	}
}
