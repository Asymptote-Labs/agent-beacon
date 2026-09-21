package inventory

import (
	"os"
	"path/filepath"
	"testing"
)

// Kimi Code's inventory row points at a file Beacon does not own: config.toml holds the runtime's
// provider credentials, its model catalog and its permission rules, and Beacon's hooks are a
// minority of it. So "this file exists" says nothing about whether Beacon is collecting, and the
// scan has to key on the hook command -- otherwise every machine with Kimi Code installed would
// report a managed config it never touched.
func TestScanReportsKimiConfigOnlyAsManagedWhenBeaconWroteIt(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", "")

	dir := filepath.Join(home, ".kimi-code")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configPath := filepath.Join(dir, "config.toml")

	// The user's own config, with their own hook in it and no Beacon entry.
	if err := os.WriteFile(configPath, []byte(`default_model = "k2"

[providers.kimi]
api_key = "sk-secret"

[[hooks]]
event = "PreToolUse"
command = "node ~/.kimi-code/hooks/block-dangerous-bash.mjs"
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	config := findConfig(Scan(Options{HomeDir: home, WorkingDir: work}).Configs, "kimi_code", configPath)
	if config == nil {
		t.Fatalf("scan did not report %s", configPath)
	}
	if !config.Exists {
		t.Fatal("the config exists but the scan reported otherwise")
	}
	if config.BeaconManaged {
		t.Fatal("a config carrying only the user's own hook was reported as Beacon-managed")
	}
	if config.ParserMode != formatTOML {
		t.Fatalf("parser mode = %q, want toml", config.ParserMode)
	}

	if err := os.WriteFile(configPath, []byte(`default_model = "k2"

[[hooks]]
event = "PreToolUse"
command = "'/opt/beacon/bin/beacon-hooks' --platform kimi --log '/l' pre-tool"
timeout = 10
`), 0o600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	config = findConfig(Scan(Options{HomeDir: home, WorkingDir: work}).Configs, "kimi_code", configPath)
	if config == nil || !config.BeaconManaged {
		t.Fatalf("a config carrying Beacon's hook command was not reported as managed: %+v", config)
	}
}

// The tell is the hook command rather than the comment Beacon writes above its block, and this is
// the case that makes the difference reachable: Kimi Code's legacy migration from `kimi-cli`
// rewrites config.toml by serializing the merged config, which keeps the `[[hooks]]` entries and
// drops every comment in the document. A marker-based tell would report "not managed" for an
// install that is still running.
func TestScanRecognizesKimiHooksAfterTheRuntimeDropsComments(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", "")

	dir := filepath.Join(home, ".kimi-code")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configPath := filepath.Join(dir, "config.toml")
	// No comments anywhere, the way a re-serialized config looks.
	if err := os.WriteFile(configPath, []byte(`default_model = 'k2'

[[hooks]]
command = "'/opt/beacon/bin/beacon-hooks' --platform kimi --log '/l' stop"
event = 'Stop'
timeout = 45
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	config := findConfig(Scan(Options{HomeDir: home, WorkingDir: work}).Configs, "kimi_code", configPath)
	if config == nil || !config.BeaconManaged {
		t.Fatalf("a re-serialized config still carrying Beacon's hooks was not reported as managed: %+v", config)
	}
}

// KIMI_CODE_HOME moves the whole Kimi Code data root. A scan that looked only under ~/.kimi-code
// would report "not found" for a machine whose config is live somewhere else.
func TestScanHonorsKimiCodeHome(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	elsewhere := filepath.Join(t.TempDir(), "kimi-root")
	t.Setenv("KIMI_CODE_HOME", elsewhere)
	if err := os.MkdirAll(elsewhere, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configPath := filepath.Join(elsewhere, "config.toml")
	if err := os.WriteFile(configPath, []byte("default_model = \"k2\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	result := Scan(Options{HomeDir: home, WorkingDir: work})
	if findConfig(result.Configs, "kimi_code", configPath) == nil {
		t.Fatalf("scan did not follow KIMI_CODE_HOME to %s", configPath)
	}
	if findConfig(result.Configs, "kimi_code", filepath.Join(home, ".kimi-code", "config.toml")) != nil {
		t.Fatal("scan also reported the default path while KIMI_CODE_HOME was set")
	}
}

// There is no project-scoped Kimi Code config to find: the runtime reads one user-level file, and
// the project-local .kimi-code directory holds a workspace override and an MCP server list,
// neither of which can register a hook. Reporting a project candidate would invite an operator to
// look for an install that cannot exist.
func TestScanReportsNoProjectScopedKimiConfig(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", "")

	for _, config := range Scan(Options{HomeDir: home, WorkingDir: work}).Configs {
		if config.Runtime == "kimi_code" && config.Scope == ScopeProject {
			t.Fatalf("scan reported a project-scoped Kimi Code config: %s", config.Path)
		}
	}
}
