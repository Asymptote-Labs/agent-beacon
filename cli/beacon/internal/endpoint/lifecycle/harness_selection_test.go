package lifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// Model-context capture is on after an install with the flag, stays on through a repair that does
// not mention it -- the package upgrades, MDM repairs and self-updates that re-run install -- and is
// off after a repair that turns it off, in Claude Code's settings, config.json and the collector
// config alike.
func TestClaudeModelContextCaptureFollowsTheLastInstallThatChoseIt(t *testing.T) {
	testenv.RequirePOSIXExecutableFixtures(t)
	home := t.TempDir()
	testenv.SetHome(t, home)
	installFakeInventoryJob(t, false)
	collectorPath := filepath.Join(home, "bin", "beacon-otelcol")
	if err := os.MkdirAll(filepath.Dir(collectorPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(collectorPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := InstallOptions{
		UserMode:      true,
		LogPath:       filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl"),
		Harnesses:     []string{"claude"},
		GRPCPort:      freePort(t),
		HTTPPort:      freePort(t),
		HealthPort:    freePort(t),
		CollectorPath: collectorPath,
		ServiceKind:   service.KindSupervised,
	}
	rawBodies := func() (string, bool) {
		data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		var settings struct {
			Env map[string]string `json:"env"`
		}
		if err := json.Unmarshal(data, &settings); err != nil {
			t.Fatal(err)
		}
		value, set := settings.Env["OTEL_LOG_RAW_API_BODIES"]
		return value, set
	}
	recorded := func() bool {
		cfg, err := endpointconfig.Load(true)
		if err != nil {
			t.Fatal(err)
		}
		collectorConfig, err := os.ReadFile(cfg.Collector.ConfigPath)
		if err != nil {
			t.Fatal(err)
		}
		if inCollector := strings.Contains(string(collectorConfig), "capture_model_context: true"); inCollector != cfg.ClaudeCaptureModelContext {
			t.Fatalf("config.json records %t but the collector config has capture_model_context %t", cfg.ClaudeCaptureModelContext, inCollector)
		}
		return cfg.ClaudeCaptureModelContext
	}
	on, off := true, false

	opts.ClaudeCaptureModelContext = &on
	if _, err := Install(opts); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if value, _ := rawBodies(); value != "1" || !recorded() {
		t.Fatalf("after install with the flag: OTEL_LOG_RAW_API_BODIES = %q, recorded = %t; want 1 and true", value, recorded())
	}

	opts.ClaudeCaptureModelContext = nil
	if _, err := Repair(opts); err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if value, _ := rawBodies(); value != "1" || !recorded() {
		t.Fatalf("after repair without the flag: OTEL_LOG_RAW_API_BODIES = %q, recorded = %t; want capture still on", value, recorded())
	}

	opts.ClaudeCaptureModelContext = &off
	if _, err := Repair(opts); err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if value, set := rawBodies(); set || recorded() {
		t.Fatalf("after repair with the flag false: OTEL_LOG_RAW_API_BODIES = %q (set %t), recorded = %t; want capture off", value, set, recorded())
	}
}
