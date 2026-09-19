package inventory

import (
	"os"
	"path/filepath"
	"testing"
)

// A DeepSeek Harness install is two files and either one alone is inert: the hooks file is a file
// nothing reads until the patch layer mounts the bridge at it, and the mount registers nothing
// until the hooks file is there. So the inventory has to report both, and has to report each one's
// Beacon-managed state independently -- a scan that collapsed them would show a half-install as a
// whole one, which is the failure mode this runtime makes easiest to hit.
func TestScanReportsBothHalvesOfADshInstall(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("DSH_HOME", "")

	dir := filepath.Join(home, ".dsh")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	hooksPath := filepath.Join(dir, "beacon-endpoint-hooks.json")
	patchPath := filepath.Join(dir, "cordis.patch.yml")

	// Only the hooks file, with Beacon's command in it. The mount is missing.
	if err := os.WriteFile(hooksPath, []byte(`{"PreToolUse":[{"hooks":[{"type":"command",`+
		`"command":"'/opt/beacon/bin/beacon-hooks' --platform dsh --log '/l' --config '/c' pre-tool"}]}]}`), 0644); err != nil {
		t.Fatalf("write hooks: %v", err)
	}
	// And a patch file that is the user's own, mounting something else entirely.
	if err := os.WriteFile(patchPath, []byte("- insert:\n    - id: schedule\n      name: '@deepseek-ai/dsh-schedule'\n"), 0644); err != nil {
		t.Fatalf("write patch: %v", err)
	}

	result := Scan(Options{HomeDir: home, WorkingDir: work})

	hooks := findConfig(result.Configs, "deepseek_harness", hooksPath)
	if hooks == nil {
		t.Fatalf("scan did not report %s", hooksPath)
	}
	if !hooks.BeaconManaged {
		t.Fatal("the hooks file carries Beacon's hook command but was not reported as Beacon-managed")
	}
	patch := findConfig(result.Configs, "deepseek_harness", patchPath)
	if patch == nil {
		t.Fatalf("scan did not report %s", patchPath)
	}
	if patch.BeaconManaged {
		t.Fatal("a patch file mounting somebody else's plugin was reported as Beacon-managed; " +
			"a half-install would read as a whole one")
	}
	if patch.ParserMode != formatYAML {
		t.Fatalf("patch parser mode = %q, want yaml", patch.ParserMode)
	}
}

// The mount is recognized by the bridge package its row names rather than by a Beacon marker,
// because Beacon adds one row to a file the user also writes. A user who mounts the bridge
// themselves matches too, which is correct: that is a live route from this runtime into a hooks
// file, and the inventory exists to find those.
func TestScanRecognizesTheDshBridgeMount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DSH_HOME", "")

	dir := filepath.Join(home, ".dsh")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	patchPath := filepath.Join(dir, "cordis.patch.yml")
	if err := os.WriteFile(patchPath, []byte(
		"- insert:\n    - id: beacon-endpoint-hooks\n      name: '@deepseek-ai/dsh-hooks-claude-code'\n"+
			"      config:\n        configPath: '"+filepath.Join(dir, "beacon-endpoint-hooks.json")+"'\n"), 0644); err != nil {
		t.Fatalf("write patch: %v", err)
	}

	result := Scan(Options{HomeDir: home, WorkingDir: t.TempDir()})
	patch := findConfig(result.Configs, "deepseek_harness", patchPath)
	if patch == nil {
		t.Fatalf("scan did not report %s", patchPath)
	}
	if !patch.BeaconManaged {
		t.Fatal("a patch file mounting the hook bridge was not recognized")
	}
}

// DSH_HOME moves the Harness home, so an inventory that assumed ~/.dsh would scan a directory the
// runtime does not read and report a monitored machine as unmonitored.
func TestScanHonorsDshHome(t *testing.T) {
	home := t.TempDir()
	elsewhere := t.TempDir()
	t.Setenv("DSH_HOME", elsewhere)

	hooksPath := filepath.Join(elsewhere, "beacon-endpoint-hooks.json")
	if err := os.WriteFile(hooksPath, []byte(`{"Stop":[{"hooks":[{"type":"command",`+
		`"command":"'/opt/beacon/bin/beacon-hooks' --platform dsh --log '/l' --config '/c' stop"}]}]}`), 0644); err != nil {
		t.Fatalf("write hooks: %v", err)
	}

	result := Scan(Options{HomeDir: home, WorkingDir: t.TempDir()})
	if config := findConfig(result.Configs, "deepseek_harness", hooksPath); config == nil || !config.BeaconManaged {
		t.Fatalf("scan did not find the Beacon hooks file under DSH_HOME at %s", hooksPath)
	}
	if config := findConfig(result.Configs, "deepseek_harness", filepath.Join(home, ".dsh", "beacon-endpoint-hooks.json")); config != nil {
		t.Fatalf("scan also looked under ~/.dsh, which this machine's dsh does not read: %+v", config)
	}
}

// dsh has no project scope: its plugin tree is composed from the Harness home, so there is nowhere
// in a repository for a hook bridge to be mounted from. A project candidate would be a path the
// runtime never reads, reported as if it might be monitored.
func TestScanReportsNoProjectScopedDshConfig(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("DSH_HOME", "")

	result := Scan(Options{HomeDir: home, WorkingDir: work})
	for _, config := range result.Configs {
		if config.Runtime != "deepseek_harness" {
			continue
		}
		if config.Scope == ScopeProject {
			t.Fatalf("scan reported a project-scoped dsh config at %s", config.Path)
		}
	}
}
