package lifecycle

import (
	"os"
	"path/filepath"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// #640, end to end at the lifecycle layer: a repair with an explicit empty OTLP selection (what
// `endpoint repair --harness omp` passes) must leave Claude Code's and Codex's own config files
// byte-for-byte alone, record no harness configs in the manifest, and replace a config.json
// written by an earlier install that defaulted to claude,codex rather than inheriting it.
func TestRepairWithExplicitEmptyHarnessesLeavesClaudeAndCodexConfigAlone(t *testing.T) {
	testenv.RequirePOSIXExecutableFixtures(t)
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("CODEX_HOME", "")
	installFakeInventoryJob(t, false)

	claudeSettings := filepath.Join(home, ".claude", "settings.json")
	codexConfig := filepath.Join(home, ".codex", "config.toml")
	const claudeBody = "{\n  \"model\": \"opus\"\n}\n"
	const codexBody = "model = \"gpt-5\"\n"
	for path, body := range map[string]string{claudeSettings: claudeBody, codexConfig: codexBody} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	collectorPath := filepath.Join(home, "bin", "beacon-otelcol")
	if err := os.MkdirAll(filepath.Dir(collectorPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(collectorPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")

	// The config.json an affected install left behind: harnesses filled in by the default.
	prior := endpointconfig.Default(true, logPath)
	if _, err := endpointconfig.Save(prior); err != nil {
		t.Fatal(err)
	}

	if _, err := Repair(InstallOptions{
		UserMode:      true,
		LogPath:       logPath,
		Harnesses:     []string{},
		GRPCPort:      freePort(t),
		HTTPPort:      freePort(t),
		HealthPort:    freePort(t),
		CollectorPath: collectorPath,
		StartService:  false,
		ServiceKind:   service.KindSupervised,
	}); err != nil {
		t.Fatalf("Repair: %v", err)
	}

	for path, want := range map[string]string{claudeSettings: claudeBody, codexConfig: codexBody} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("%s was rewritten by a repair that selected no OTLP runtime:\n%s", path, got)
		}
		if backups, _ := filepath.Glob(path + ".beacon.*.bak"); len(backups) != 0 {
			t.Fatalf("%s gained Beacon backups %v, so it was written", path, backups)
		}
	}
	manifest, err := ReadManifest(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.HarnessConfigs) != 0 || len(manifest.Backups) != 0 {
		t.Fatalf("manifest records harness configs %v / backups %v, want none", manifest.HarnessConfigs, manifest.Backups)
	}
	cfg, err := endpointconfig.Load(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Harnesses) != 0 {
		t.Fatalf("config.json harnesses = %#v, want the explicit empty selection, not the prior default", cfg.Harnesses)
	}
}
