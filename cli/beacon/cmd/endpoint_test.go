package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
	endpointinventory "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/inventory"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/selfupdate"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"

	"github.com/spf13/cobra"
)

func TestSplitCSV(t *testing.T) {
	got := splitCSV("cursor, claude-cowork,,codex")
	want := []string{"cursor", "claude-cowork", "codex"}
	if len(got) != len(want) {
		t.Fatalf("splitCSV length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("splitCSV[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSplitHarnessCSVAllowsCollectorOnly(t *testing.T) {
	got := splitHarnessCSV("")
	if len(got) != 0 {
		t.Fatalf("splitHarnessCSV empty = %#v, want empty slice", got)
	}
	got = splitHarnessCSV("claude,codex")
	if len(got) != 2 || got[0] != "claude" || got[1] != "codex" {
		t.Fatalf("splitHarnessCSV populated = %#v, want claude/codex", got)
	}
}

func TestSplitEndpointTargetsDedupesHookAliases(t *testing.T) {
	otlp, hooks, err := splitEndpointTargets([]string{"claude", "codex", "devin", "devin-cli", "devin_desktop", "hermes-agent"})
	if err != nil {
		t.Fatalf("splitEndpointTargets returned error: %v", err)
	}
	if got, want := strings.Join(otlp, ","), "claude,codex"; got != want {
		t.Fatalf("otlp targets = %q, want %q", got, want)
	}
	if got, want := strings.Join(hooks, ","), "devin-cli,devin-desktop,hermes"; got != want {
		t.Fatalf("hook targets = %q, want %q", got, want)
	}
}

func TestCanonicalHookTargetsNormalizesAliasesToSwitchNames(t *testing.T) {
	got, err := canonicalHookTargets([]string{"claude_code", "vs_code", "droid", "devin", "devin_desktop", "antigravity_cli", "hermes-agent"})
	if err != nil {
		t.Fatalf("canonicalHookTargets returned error: %v", err)
	}
	want := []string{"claude", "vscode", "factory", "devin-cli", "devin-desktop", "antigravity", "hermes"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("canonicalHookTargets = %#v, want %#v", got, want)
	}
}

func TestPlannedInstallActionsSeparatesHookHarnesses(t *testing.T) {
	old := endpointOpts
	t.Cleanup(func() { endpointOpts = old })
	endpointOpts.userMode = true
	endpointOpts.harnesses = "claude,devin,devin-cli,devin-desktop"
	endpointOpts.logPath = filepath.Join(t.TempDir(), "runtime.jsonl")
	endpointOpts.noStart = true

	actions := plannedInstallActions(false)
	counts := map[string]int{}
	for _, action := range actions {
		if action.Action == "configure_harness" {
			counts[action.Target]++
		}
	}
	if counts["claude"] != 1 || counts["devin-cli"] != 1 || counts["devin-desktop"] != 1 {
		t.Fatalf("configure_harness counts = %#v, want claude/devin-cli/devin-desktop once", counts)
	}
	if counts["devin"] != 0 {
		t.Fatalf("legacy devin alias should be deduped, counts=%#v", counts)
	}
}

func TestEndpointDashboardCommandRegistered(t *testing.T) {
	cmd, _, err := endpointCmd.Find([]string{"dashboard"})
	if err != nil {
		t.Fatalf("Find dashboard returned error: %v", err)
	}
	if cmd == nil || cmd.Use != "dashboard" {
		t.Fatalf("dashboard command not registered: %#v", cmd)
	}
	if cmd.Flags().Lookup("addr") == nil {
		t.Fatal("dashboard command missing --addr flag")
	}
	if cmd.Flags().Lookup("open") == nil {
		t.Fatal("dashboard command missing --open flag")
	}
}

func TestEndpointInstallAndRepairRegisterFalconFlags(t *testing.T) {
	for _, cmd := range []*cobra.Command{endpointInstallCmd, endpointRepairCmd} {
		for _, name := range []string{
			"falcon-hec-endpoint",
			"falcon-hec-token",
			"falcon-index",
			"falcon-source",
			"falcon-sourcetype",
			"falcon-insecure-skip-verify",
			"falcon-ca-file",
		} {
			if cmd.Flags().Lookup(name) == nil {
				t.Fatalf("%s missing --%s flag", cmd.Use, name)
			}
		}
	}
}

func TestFalconHECOptionsDefaultIsNil(t *testing.T) {
	old := endpointOpts
	t.Cleanup(func() { endpointOpts = old })
	endpointOpts.falconSource = endpointconfig.DefaultFalconSource
	endpointOpts.falconSourcetype = endpointconfig.DefaultFalconSourcetype
	if got := falconHECOptions(); got != nil {
		t.Fatalf("falconHECOptions() = %#v, want nil", got)
	}
}

func TestRepairCollectorServiceRollsBackWhenReadinessFails(t *testing.T) {
	oldOpts := endpointOpts
	oldLoadConfig := repairLoadEndpointConfig
	oldSaveConfig := repairSaveEndpointConfig
	oldResolveBinary := repairResolveCollectorBinary
	oldWriteConfig := repairWriteCollectorConfig
	oldWaitReady := repairWaitCollectorReady
	oldNewManager := newRepairServiceManager
	t.Cleanup(func() {
		endpointOpts = oldOpts
		repairLoadEndpointConfig = oldLoadConfig
		repairSaveEndpointConfig = oldSaveConfig
		repairResolveCollectorBinary = oldResolveBinary
		repairWriteCollectorConfig = oldWriteConfig
		repairWaitCollectorReady = oldWaitReady
		newRepairServiceManager = oldNewManager
	})

	home := t.TempDir()
	t.Setenv("HOME", home)
	collectorConfigPath := filepath.Join(home, "otelcol.yaml")
	plistPath := filepath.Join(home, "agent.plist")
	cfg := endpointconfig.Config{
		UserMode: true,
		LogPath:  filepath.Join(home, "old-runtime.jsonl"),
		Collector: endpointconfig.Collector{
			ConfigPath: collectorConfigPath,
			SpoolPath:  filepath.Join(home, "spool", "otlp.jsonl"),
			GRPCPort:   endpointconfig.DefaultGRPCPort,
			HTTPPort:   endpointconfig.DefaultHTTPPort,
		},
	}
	if _, err := endpointconfig.Save(cfg); err != nil {
		t.Fatalf("save original config: %v", err)
	}
	if err := os.WriteFile(collectorConfigPath, []byte("old collector config"), 0644); err != nil {
		t.Fatalf("write original collector config: %v", err)
	}
	if err := os.WriteFile(plistPath, []byte("old plist"), 0644); err != nil {
		t.Fatalf("write original plist: %v", err)
	}
	originalConfig, err := os.ReadFile(endpointconfig.ConfigPath(true))
	if err != nil {
		t.Fatalf("read original config: %v", err)
	}

	fakeManager := &fakeRepairServiceManager{plistPath: plistPath}
	newRepairServiceManager = func(userMode bool) repairServiceManager {
		if !userMode {
			t.Fatal("repair should use effective user mode")
		}
		return fakeManager
	}
	repairResolveCollectorBinary = func(configured string) (string, error) {
		return filepath.Join(home, "beacon-otelcol"), nil
	}
	repairWriteCollectorConfig = func(cfg endpointconfig.Config) error {
		return os.WriteFile(cfg.Collector.ConfigPath, []byte("new collector config"), 0644)
	}
	repairWaitCollectorReady = func(cfg endpointconfig.Config, timeout time.Duration) error {
		return errors.New("collector did not become ready before timeout")
	}
	endpointOpts.logPath = filepath.Join(home, "new-runtime.jsonl")

	err = repairCollectorServiceFromStatus(lifecycle.Status{
		RuntimeLog: lifecycle.RuntimeLogSource{EffectiveUserMode: true},
		Service:    service.Status{Loaded: true, Running: true},
	})
	if err == nil || !strings.Contains(err.Error(), "collector did not become ready") {
		t.Fatalf("repairCollectorServiceFromStatus error = %v, want readiness failure", err)
	}
	if fakeManager.loads != 2 {
		t.Fatalf("Load calls = %d, want repair load plus rollback reload", fakeManager.loads)
	}
	if fakeManager.unloads != 1 {
		t.Fatalf("Unload calls = %d, want rollback unload", fakeManager.unloads)
	}
	assertFileContent(t, endpointconfig.ConfigPath(true), string(originalConfig))
	assertFileContent(t, collectorConfigPath, "old collector config")
	assertFileContent(t, plistPath, "old plist")
}

type fakeRepairServiceManager struct {
	plistPath string
	loads     int
	unloads   int
}

func (m *fakeRepairServiceManager) PlistPath() (string, error) {
	return m.plistPath, nil
}

func (m *fakeRepairServiceManager) WritePlist(program, configPath string) (string, error) {
	return m.plistPath, os.WriteFile(m.plistPath, []byte("new plist"), 0644)
}

func (m *fakeRepairServiceManager) Load() error {
	m.loads++
	return nil
}

func (m *fakeRepairServiceManager) Unload() error {
	m.unloads++
	return nil
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, string(data), want)
	}
}

func TestEndpointCoworkSetupCommandRegistered(t *testing.T) {
	cmd, _, err := endpointCmd.Find([]string{"integrations", "claude-cowork", "setup"})
	if err != nil {
		t.Fatalf("Find cowork setup returned error: %v", err)
	}
	if cmd == nil || cmd.Use != "setup" {
		t.Fatalf("cowork setup command not registered: %#v", cmd)
	}
	for _, name := range []string{"endpoint", "headers", "resource-attributes", "ngrok", "open"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("cowork setup command missing --%s flag", name)
		}
	}
}

func TestEndpointOpenClawCommandsRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"integrations", "openclaw", "print-config"},
		{"integrations", "openclaw", "status"},
		{"integrations", "openclaw", "validate"},
	} {
		cmd, _, err := endpointCmd.Find(path)
		if err != nil {
			t.Fatalf("Find %v returned error: %v", path, err)
		}
		if cmd == nil || cmd.Use != path[len(path)-1] {
			t.Fatalf("openclaw command %v not registered: %#v", path, cmd)
		}
	}
	if endpointOpenClawPrintConfigCmd.Flags().Lookup("endpoint") == nil {
		t.Fatal("openclaw print-config command missing --endpoint flag")
	}
	if endpointOpenClawStatusCmd.Flags().Lookup("json") == nil {
		t.Fatal("openclaw status command missing --json flag")
	}
	if endpointOpenClawValidateCmd.Flags().Lookup("since") == nil {
		t.Fatal("openclaw validate command missing --since flag")
	}
}

func TestEndpointVSCodeCommandsRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"integrations", "vscode", "print-config"},
		{"integrations", "vscode", "setup"},
		{"integrations", "vscode", "status"},
		{"integrations", "vscode", "validate"},
	} {
		cmd, _, err := endpointCmd.Find(path)
		if err != nil {
			t.Fatalf("Find %v returned error: %v", path, err)
		}
		if cmd == nil || cmd.Use != path[len(path)-1] {
			t.Fatalf("vscode command %v not registered: %#v", path, cmd)
		}
	}
	for _, name := range []string{"endpoint", "workspace", "capture-content"} {
		if endpointVSCodeSetupCmd.Flags().Lookup(name) == nil {
			t.Fatalf("vscode setup command missing --%s flag", name)
		}
	}
	if endpointVSCodeSetupCmd.Flags().Lookup("dry-run") == nil {
		t.Fatal("vscode setup command missing --dry-run flag")
	}
	if endpointVSCodeStatusCmd.Flags().Lookup("json") == nil {
		t.Fatal("vscode status command missing --json flag")
	}
	if endpointVSCodeValidateCmd.Flags().Lookup("since") == nil {
		t.Fatal("vscode validate command missing --since flag")
	}
}

func TestEndpointElasticCommandsRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"elastic", "print-config"},
		{"elastic", "install-pack"},
		{"elastic", "up"},
		{"elastic", "down"},
	} {
		cmd, _, err := endpointCmd.Find(path)
		if err != nil {
			t.Fatalf("Find %v returned error: %v", path, err)
		}
		if cmd == nil || cmd.Use != path[len(path)-1] {
			t.Fatalf("elastic command %v not registered: %#v", path, cmd)
		}
	}
	if findEndpointLeaf("elastic", "install-pack").Flags().Lookup("output") == nil {
		t.Fatal("elastic install-pack command missing --output flag")
	}
	if findEndpointLeaf("elastic", "up").Flags().Lookup("pack-dir") == nil {
		t.Fatal("elastic up command missing --pack-dir flag")
	}
	if findEndpointLeaf("elastic", "down").Flags().Lookup("pack-dir") == nil {
		t.Fatal("elastic down command missing --pack-dir flag")
	}
	for _, name := range []string{"user", "system", "log-path"} {
		if findEndpointLeaf("elastic", "down").Flags().Lookup(name) == nil {
			t.Fatalf("elastic down command missing --%s flag", name)
		}
	}
}

func TestEndpointDatadogCommandsRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"datadog", "print-config"},
		{"datadog", "install-pack"},
		{"datadog", "validate"},
	} {
		cmd, _, err := endpointCmd.Find(path)
		if err != nil {
			t.Fatalf("Find %v returned error: %v", path, err)
		}
		if cmd == nil || cmd.Use != path[len(path)-1] {
			t.Fatalf("datadog command %v not registered: %#v", path, cmd)
		}
		for _, name := range []string{"user", "system", "log-path"} {
			if cmd.Flags().Lookup(name) == nil {
				t.Fatalf("datadog command %v missing --%s flag", path, name)
			}
		}
	}
	if findEndpointLeaf("datadog", "install-pack").Flags().Lookup("output") == nil {
		t.Fatal("datadog install-pack command missing --output flag")
	}
}

func TestEndpointSumoCommandsRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"sumo", "print-config"},
		{"sumo", "install-pack"},
		{"sumo", "validate"},
	} {
		cmd, _, err := endpointCmd.Find(path)
		if err != nil {
			t.Fatalf("Find %v returned error: %v", path, err)
		}
		if cmd == nil || cmd.Use != path[len(path)-1] {
			t.Fatalf("sumo command %v not registered: %#v", path, cmd)
		}
		for _, name := range []string{"user", "system", "log-path"} {
			if cmd.Flags().Lookup(name) == nil {
				t.Fatalf("sumo command %v missing --%s flag", path, name)
			}
		}
	}
	if findEndpointLeaf("sumo", "install-pack").Flags().Lookup("output") == nil {
		t.Fatal("sumo install-pack command missing --output flag")
	}
}

func TestEndpointRapid7CommandsRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"rapid7", "print-config"},
		{"rapid7", "install-pack"},
		{"rapid7", "validate"},
	} {
		cmd, _, err := endpointCmd.Find(path)
		if err != nil {
			t.Fatalf("Find %v returned error: %v", path, err)
		}
		if cmd == nil || cmd.Use != path[len(path)-1] {
			t.Fatalf("rapid7 command %v not registered: %#v", path, cmd)
		}
		for _, name := range []string{"user", "system", "log-path"} {
			if cmd.Flags().Lookup(name) == nil {
				t.Fatalf("rapid7 command %v missing --%s flag", path, name)
			}
		}
	}
	if findEndpointLeaf("rapid7", "install-pack").Flags().Lookup("output") == nil {
		t.Fatal("rapid7 install-pack command missing --output flag")
	}
}

func TestEndpointS3CommandsRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"s3", "print-config"},
		{"s3", "install-pack"},
		{"s3", "validate"},
	} {
		cmd, _, err := endpointCmd.Find(path)
		if err != nil {
			t.Fatalf("Find %v returned error: %v", path, err)
		}
		if cmd == nil || cmd.Use != path[len(path)-1] {
			t.Fatalf("s3 command %v not registered: %#v", path, cmd)
		}
		for _, name := range []string{"user", "system", "log-path"} {
			if cmd.Flags().Lookup(name) == nil {
				t.Fatalf("s3 command %v missing --%s flag", path, name)
			}
		}
	}
	if findEndpointLeaf("s3", "install-pack").Flags().Lookup("output") == nil {
		t.Fatal("s3 install-pack command missing --output flag")
	}
}

func TestEndpointCloudWatchCommandsRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"cloudwatch", "print-config"},
		{"cloudwatch", "install-pack"},
		{"cloudwatch", "validate"},
	} {
		cmd, _, err := endpointCmd.Find(path)
		if err != nil {
			t.Fatalf("Find %v returned error: %v", path, err)
		}
		if cmd == nil || cmd.Use != path[len(path)-1] {
			t.Fatalf("cloudwatch command %v not registered: %#v", path, cmd)
		}
		for _, name := range []string{"user", "system", "log-path"} {
			if cmd.Flags().Lookup(name) == nil {
				t.Fatalf("cloudwatch command %v missing --%s flag", path, name)
			}
		}
	}
	if findEndpointLeaf("cloudwatch", "install-pack").Flags().Lookup("output") == nil {
		t.Fatal("cloudwatch install-pack command missing --output flag")
	}
}

func TestEndpointGCSCommandsRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"gcs", "print-config"},
		{"gcs", "install-pack"},
		{"gcs", "validate"},
	} {
		cmd, _, err := endpointCmd.Find(path)
		if err != nil {
			t.Fatalf("Find %v returned error: %v", path, err)
		}
		if cmd == nil || cmd.Use != path[len(path)-1] {
			t.Fatalf("gcs command %v not registered: %#v", path, cmd)
		}
		for _, name := range []string{"user", "system", "log-path"} {
			if cmd.Flags().Lookup(name) == nil {
				t.Fatalf("gcs command %v missing --%s flag", path, name)
			}
		}
	}
	if findEndpointLeaf("gcs", "install-pack").Flags().Lookup("output") == nil {
		t.Fatal("gcs install-pack command missing --output flag")
	}
}

func TestEndpointSentinelCommandsRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"sentinel", "print-config"},
		{"sentinel", "install-pack"},
		{"sentinel", "validate"},
	} {
		cmd, _, err := endpointCmd.Find(path)
		if err != nil {
			t.Fatalf("Find %v returned error: %v", path, err)
		}
		if cmd == nil || cmd.Use != path[len(path)-1] {
			t.Fatalf("sentinel command %v not registered: %#v", path, cmd)
		}
		for _, name := range []string{"user", "system", "log-path"} {
			if cmd.Flags().Lookup(name) == nil {
				t.Fatalf("sentinel command %v missing --%s flag", path, name)
			}
		}
	}
	if findEndpointLeaf("sentinel", "install-pack").Flags().Lookup("output") == nil {
		t.Fatal("sentinel install-pack command missing --output flag")
	}
}

func TestEnsureElasticPackDoesNotOverwriteExistingPack(t *testing.T) {
	dir := t.TempDir()
	composePath := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte("custom compose"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ensureElasticPack(dir, "/tmp/beacon/runtime.jsonl"); err != nil {
		t.Fatalf("ensureElasticPack returned error: %v", err)
	}
	got, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "custom compose" {
		t.Fatalf("ensureElasticPack overwrote existing pack: %s", got)
	}
}

func TestRunEndpointElasticDownIgnoresMissingPack(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("elastic down is currently macOS-only")
	}
	oldPackDir := endpointOpts.elasticPackDir
	endpointOpts.elasticPackDir = filepath.Join(t.TempDir(), "missing-pack")
	t.Cleanup(func() {
		endpointOpts.elasticPackDir = oldPackDir
	})
	if err := runEndpointElasticDown(t.Context()); err != nil {
		t.Fatalf("runEndpointElasticDown returned error for missing pack: %v", err)
	}
}

func TestEnvDefault(t *testing.T) {
	t.Setenv("BEACON_TEST_VALUE", "")
	if got := envDefault("BEACON_TEST_VALUE", "fallback"); got != "fallback" {
		t.Fatalf("envDefault empty = %q", got)
	}
	t.Setenv("BEACON_TEST_VALUE", "  12345  ")
	if got := envDefault("BEACON_TEST_VALUE", "fallback"); got != "12345" {
		t.Fatalf("envDefault value = %q", got)
	}
}

func TestEndpointCoworkValidateSinceFlagRegistered(t *testing.T) {
	cmd, _, err := endpointCmd.Find([]string{"integrations", "claude-cowork", "validate"})
	if err != nil {
		t.Fatalf("Find cowork validate returned error: %v", err)
	}
	if cmd.Flags().Lookup("since") == nil {
		t.Fatal("cowork validate command missing --since flag")
	}
}

func TestParseNgrokURL(t *testing.T) {
	line := `lvl=info msg="started tunnel" obj=tunnels name=command_line addr=http://localhost:4318 url=https://abc-123.ngrok-free.app`
	if got, want := parseNgrokURL(line), "https://abc-123.ngrok-free.app"; got != want {
		t.Fatalf("parseNgrokURL = %q, want %q", got, want)
	}
}

func TestBasicAuthHeader(t *testing.T) {
	got := basicAuthHeader("beacon", "secret")
	if got != "Authorization=Basic YmVhY29uOnNlY3JldA==" {
		t.Fatalf("basicAuthHeader = %q", got)
	}
}

func TestEndpointHarnessDefaultsDoNotClobberInstall(t *testing.T) {
	installFlag := endpointInstallCmd.Flags().Lookup("harness")
	if installFlag == nil {
		t.Fatal("install command missing --harness flag")
	}
	if got, want := installFlag.DefValue, "claude,codex"; got != want {
		t.Fatalf("install --harness default = %q, want %q", got, want)
	}

	hooksFlag := endpointHooksInstallCmd.Flags().Lookup("harness")
	if hooksFlag == nil {
		t.Fatal("hooks install command missing --harness flag")
	}
	if got, want := hooksFlag.DefValue, "cursor"; got != want {
		t.Fatalf("hooks install --harness default = %q, want %q", got, want)
	}
}

func TestEndpointRepairSupportsNoStart(t *testing.T) {
	if endpointRepairCmd.Flags().Lookup("no-start") == nil {
		t.Fatal("repair command missing --no-start flag")
	}
}

func TestEndpointInstallAndRepairSupportSplunkFlags(t *testing.T) {
	for _, cmd := range []*cobra.Command{endpointInstallCmd, endpointRepairCmd} {
		for _, name := range []string{
			"splunk-hec-endpoint",
			"splunk-hec-token",
			"splunk-index",
			"splunk-source",
			"splunk-sourcetype",
			"splunk-insecure-skip-verify",
			"splunk-ca-file",
		} {
			if cmd.Flags().Lookup(name) == nil {
				t.Fatalf("%s command missing --%s flag", cmd.Use, name)
			}
		}
	}
}

func TestEndpointInstallAndRepairSupportDebugTelemetryFlags(t *testing.T) {
	for _, cmd := range []*cobra.Command{endpointInstallCmd, endpointRepairCmd} {
		if cmd.Flags().Lookup("include-runtime-metrics") == nil {
			t.Fatalf("%s command missing --include-runtime-metrics flag", cmd.Use)
		}
		if cmd.Flags().Lookup("include-codex-spans") == nil {
			t.Fatalf("%s command missing --include-codex-spans flag", cmd.Use)
		}
	}
}

func TestEndpointCommandsDefaultToUserMode(t *testing.T) {
	for _, cmd := range []*cobra.Command{endpointInstallCmd, endpointStatusCmd, endpointDashboardCmd, endpointHooksInstallCmd} {
		userFlag := cmd.Flags().Lookup("user")
		if userFlag == nil {
			t.Fatalf("%s missing --user flag", cmd.Use)
		}
		if userFlag.DefValue != "true" {
			t.Fatalf("%s --user default = %q, want true", cmd.Use, userFlag.DefValue)
		}
		if cmd.Flags().Lookup("system") == nil {
			t.Fatalf("%s missing --system flag", cmd.Use)
		}
	}
}

func TestRunEndpointDashboardPassesRequestedLogPathToHandler(t *testing.T) {
	oldOpts := endpointOpts
	oldListen := dashboardListenAndServe
	t.Cleanup(func() {
		endpointOpts = oldOpts
		dashboardListenAndServe = oldListen
	})
	home := t.TempDir()
	t.Setenv("HOME", home)
	configuredLog := filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")
	if _, err := endpointconfig.Save(endpointconfig.Default(true, configuredLog)); err != nil {
		t.Fatalf("save endpoint config: %v", err)
	}

	var got dashboard.Options
	dashboardListenAndServe = func(opts dashboard.Options) error {
		got = opts
		return nil
	}
	endpointOpts.userMode = true
	endpointOpts.systemMode = false
	endpointOpts.logPath = ""
	endpointOpts.dashboardAddr = "127.0.0.1:0"

	if err := runEndpointDashboard(nil, nil); err != nil {
		t.Fatalf("runEndpointDashboard returned error: %v", err)
	}
	if got.LogPath != "" {
		t.Fatalf("dashboard LogPath = %q, want empty requested log path so Handler can resolve runtime source", got.LogPath)
	}
	if !got.UserMode {
		t.Fatal("dashboard UserMode = false, want requested user mode")
	}
}

func TestRobustEndpointCommandsRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"doctor"},
		{"inventory"},
		{"test-event"},
		{"bundle-diagnostics"},
		{"config", "show"},
		{"config", "validate"},
		{"integrations", "validate"},
	} {
		cmd, _, err := endpointCmd.Find(path)
		if err != nil {
			t.Fatalf("Find %v returned error: %v", path, err)
		}
		if cmd == nil {
			t.Fatalf("command %v not registered", path)
		}
	}
}

func TestWriteInventoryEventsAppendsInventorySnapshot(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	cfg := endpointconfig.Default(true, logPath)
	result := endpointinventory.Result{
		Configs: []endpointinventory.Config{
			{
				Runtime:      "claude_code",
				Path:         filepath.Join(t.TempDir(), ".claude", "settings.json"),
				PathHash:     "sha256:path",
				Scope:        endpointinventory.ScopeUser,
				Exists:       true,
				Readable:     true,
				ParserStatus: endpointinventory.StatusOK,
				Redaction:    endpointinventory.RedactionFull,
			},
			{
				Runtime:      "cursor",
				PathHash:     "sha256:missing",
				Scope:        endpointinventory.ScopeUser,
				Exists:       false,
				ParserStatus: endpointinventory.StatusNotFound,
				Redaction:    endpointinventory.RedactionFull,
			},
		},
		MCPServers: []endpointinventory.MCPServer{
			{
				Runtime:        "claude_code",
				ServerName:     "filesystem",
				SourcePathHash: "sha256:path",
				SourceScope:    endpointinventory.ScopeUser,
				Transport:      endpointinventory.TransportStdio,
				DefinitionHash: "sha256:def",
				ParserStatus:   endpointinventory.StatusOK,
				Redaction:      endpointinventory.RedactionFull,
			},
		},
		Skills: []endpointinventory.Skill{
			{
				Runtime:          "claude_code",
				SkillName:        "deploy",
				SkillNameHash:    "sha256:skill",
				RootPathHash:     "sha256:root",
				SourceScope:      endpointinventory.ScopeUser,
				ManifestPath:     filepath.Join(t.TempDir(), ".claude", "skills", "deploy", "SKILL.md"),
				ManifestPathHash: "sha256:manifest",
				Exists:           true,
				Readable:         true,
				ParserStatus:     endpointinventory.StatusOK,
				FileSHA256:       "sha256:body",
				Redaction:        endpointinventory.RedactionFull,
			},
			{
				Runtime:      "cursor",
				RootPathHash: "sha256:missing-skill-root",
				SourceScope:  endpointinventory.ScopeUser,
				Exists:       false,
				ParserStatus: endpointinventory.StatusNotFound,
				Redaction:    endpointinventory.RedactionFull,
			},
		},
	}

	if err := writeInventoryEvents(cfg, result); err != nil {
		t.Fatalf("writeInventoryEvents returned error: %v", err)
	}
	if data, err := os.ReadFile(logPath); err == nil && strings.Contains(string(data), "inventory.") {
		t.Fatalf("runtime log should not contain inventory events: %s", string(data))
	}
	inventoryLogPath := endpointinventory.LogPath(logPath, cfg.UserMode)
	data, err := os.ReadFile(inventoryLogPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Count(text, "inventory.heartbeat") != 1 {
		t.Fatalf("heartbeat events = %d, want 1; log=%s", strings.Count(text, "inventory.heartbeat"), text)
	}
	if strings.Count(text, "inventory.snapshot") != 1 {
		t.Fatalf("snapshot events = %d, want 1; log=%s", strings.Count(text, "inventory.snapshot"), text)
	}
	if !strings.Contains(text, "filesystem") {
		t.Fatalf("inventory log missing MCP server summary: %s", text)
	}
	if !strings.Contains(text, "deploy") {
		t.Fatalf("inventory log missing skill summary: %s", text)
	}
	if strings.Contains(text, "missing-skill-root") {
		t.Fatalf("inventory log should not include missing skill roots: %s", text)
	}
}

func TestWriteInventoryEventsHonorsConfiguredRuntimes(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	cfg := endpointconfig.Default(true, logPath)
	cfg.Inventory = &endpointconfig.Inventory{Runtimes: []string{"cursor"}}
	result := endpointinventory.Result{
		Configs: []endpointinventory.Config{
			{Runtime: "cursor", PathHash: "sha256:cursor", Exists: true, ParserStatus: endpointinventory.StatusOK, Redaction: endpointinventory.RedactionFull},
			{Runtime: "claude_code", PathHash: "sha256:claude", Exists: true, ParserStatus: endpointinventory.StatusOK, Redaction: endpointinventory.RedactionFull},
		},
		MCPServers: []endpointinventory.MCPServer{
			{Runtime: "cursor", ServerName: "cursor-server", SourcePathHash: "sha256:cursor", DefinitionHash: "sha256:cursor-def", ParserStatus: endpointinventory.StatusOK, Redaction: endpointinventory.RedactionFull},
			{Runtime: "claude_code", ServerName: "claude-server", SourcePathHash: "sha256:claude", DefinitionHash: "sha256:claude-def", ParserStatus: endpointinventory.StatusOK, Redaction: endpointinventory.RedactionFull},
		},
		Skills: []endpointinventory.Skill{
			{Runtime: "cursor", SkillName: "cursor-skill", Exists: true, ParserStatus: endpointinventory.StatusOK, Redaction: endpointinventory.RedactionFull},
			{Runtime: "claude_code", SkillName: "claude-skill", Exists: true, ParserStatus: endpointinventory.StatusOK, Redaction: endpointinventory.RedactionFull},
		},
	}

	if err := writeInventoryEvents(cfg, result); err != nil {
		t.Fatalf("writeInventoryEvents returned error: %v", err)
	}
	data, err := os.ReadFile(endpointinventory.LogPath(logPath, cfg.UserMode))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"cursor-server", "cursor-skill"} {
		if !strings.Contains(text, want) {
			t.Fatalf("inventory log missing %q: %s", want, text)
		}
	}
	for _, forbidden := range []string{"claude-server", "claude-skill"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("inventory log should not include filtered runtime %q: %s", forbidden, text)
		}
	}
}

func TestInventoryHeartbeatTTLAndSnapshotDigest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	writeTestFile(t, filepath.Join(home, ".cursor", "mcp.json"), `{"mcpServers":{"one":{"command":"npx"}}}`)
	cfg := endpointconfig.Default(true, logPath)
	enabled := true
	cfg.Inventory = &endpointconfig.Inventory{Enabled: &enabled, TTLSeconds: 86400, Runtimes: []string{"cursor", "claude_code"}}
	settings := endpointconfig.InventoryConfig(cfg)

	first, err := writeInventoryHeartbeat(cfg, settings, false, work, "hook", "cursor")
	if err != nil {
		t.Fatalf("first heartbeat returned error: %v", err)
	}
	if !first.Written || !first.SnapshotWritten || first.SnapshotDigest == "" {
		t.Fatalf("first heartbeat = %#v, want heartbeat and snapshot", first)
	}
	second, err := writeInventoryHeartbeat(cfg, settings, false, work, "hook", "cursor")
	if err != nil {
		t.Fatalf("second heartbeat returned error: %v", err)
	}
	if second.Written || second.SkippedReason != "ttl_active" {
		t.Fatalf("second heartbeat = %#v, want TTL skip", second)
	}
	forced, err := writeInventoryHeartbeat(cfg, settings, true, work, "hook", "cursor")
	if err != nil {
		t.Fatalf("forced heartbeat returned error: %v", err)
	}
	if !forced.Written || forced.SnapshotWritten {
		t.Fatalf("forced unchanged heartbeat = %#v, want heartbeat only", forced)
	}

	writeTestFile(t, filepath.Join(home, ".cursor", "mcp.json"), `{"mcpServers":{"one":{"command":"npx"},"two":{"command":"uvx"}}}`)
	changed, err := writeInventoryHeartbeat(cfg, settings, true, work, "hook", "cursor")
	if err != nil {
		t.Fatalf("changed heartbeat returned error: %v", err)
	}
	if !changed.Written || !changed.SnapshotWritten || changed.SnapshotDigest == forced.SnapshotDigest {
		t.Fatalf("changed heartbeat = %#v, want new snapshot digest", changed)
	}

	if data, err := os.ReadFile(logPath); err == nil && strings.Contains(string(data), "inventory.") {
		t.Fatalf("runtime log should not contain inventory events: %s", string(data))
	}
	inventoryLogPath := endpointinventory.LogPath(logPath, cfg.UserMode)
	data, err := os.ReadFile(inventoryLogPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Count(text, "inventory.heartbeat") != 3 {
		t.Fatalf("heartbeat count = %d, want 3; log=%s", strings.Count(text, "inventory.heartbeat"), text)
	}
	if strings.Count(text, "inventory.snapshot") != 2 {
		t.Fatalf("snapshot count = %d, want 2; log=%s", strings.Count(text, "inventory.snapshot"), text)
	}
	if !strings.Contains(text, `"server_name":"two"`) {
		t.Fatalf("changed snapshot missing new MCP server: %s", text)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(logPath), "inventory-state.json")); err != nil {
		t.Fatalf("inventory state should live beside inventory log: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".beacon", "endpoint", "inventory-state.json")); !os.IsNotExist(err) {
		t.Fatalf("inventory state should not use endpoint config base, err=%v", err)
	}
}

func TestInventoryHeartbeatAppendFailureRecordsAttemptOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	logDir := t.TempDir()
	logPath := filepath.Join(logDir, "runtime.jsonl")
	inventoryLogPath := endpointinventory.LogPath(logPath, true)
	if err := os.MkdirAll(inventoryLogPath, 0755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(home, ".cursor", "mcp.json"), `{"mcpServers":{"one":{"command":"npx"}}}`)
	cfg := endpointconfig.Default(true, logPath)
	cfg.Inventory = &endpointconfig.Inventory{TTLSeconds: 86400, Runtimes: []string{"cursor"}}
	settings := endpointconfig.InventoryConfig(cfg)

	first, err := writeInventoryHeartbeat(cfg, settings, false, work, "hook", "cursor")
	if err == nil {
		t.Fatal("writeInventoryHeartbeat should fail when inventory log path is a directory")
	}
	if first.SnapshotDigest == "" {
		t.Fatalf("failed heartbeat should still report snapshot digest: %#v", first)
	}
	statePath := endpointinventory.StatePathForLog(logPath, true)
	state, err := endpointinventory.ReadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastAttemptAt == "" {
		t.Fatalf("failed append should persist attempt timestamp: %#v", state)
	}
	if state.LastEmittedAt != "" || state.LastSnapshotDigest != "" {
		t.Fatalf("failed append should not mark emission complete: %#v", state)
	}

	second, err := writeInventoryHeartbeat(cfg, settings, false, work, "hook", "cursor")
	if err != nil {
		t.Fatalf("second heartbeat should be skipped by attempt backoff, got error: %v", err)
	}
	if second.SkippedReason != "attempt_backoff" {
		t.Fatalf("second heartbeat = %#v, want attempt_backoff", second)
	}
}

func TestTopLevelAliasesRegistered(t *testing.T) {
	for _, name := range []string{"doctor", "status", "inventory"} {
		cmd, _, err := rootCmd.Find([]string{name})
		if err != nil {
			t.Fatalf("Find %s returned error: %v", name, err)
		}
		if cmd == nil || cmd.Use != name {
			t.Fatalf("top-level %s alias not registered: %#v", name, cmd)
		}
		if cmd.Flags().Lookup("user") == nil || cmd.Flags().Lookup("log-path") == nil {
			t.Fatalf("top-level %s alias missing endpoint flags", name)
		}
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRobustCLIFlagsRegistered(t *testing.T) {
	for _, cmd := range []*cobra.Command{endpointInstallCmd, endpointRepairCmd, endpointUninstallCmd, endpointHooksInstallCmd, endpointHooksUninstallCmd} {
		if cmd.Flags().Lookup("dry-run") == nil {
			t.Fatalf("%s missing --dry-run", cmd.Use)
		}
	}
	for _, cmd := range []*cobra.Command{endpointDiscoverCmd, endpointInventoryCmd, endpointHooksStatusCmd, endpointHooksInstallCmd, endpointHooksUninstallCmd, endpointIntegrationsValidateCmd} {
		if cmd.Flags().Lookup("all") == nil {
			t.Fatalf("%s missing --all", cmd.Use)
		}
	}
	if endpointBundleDiagnosticsCmd.Flags().Lookup("include-event-summaries") == nil {
		t.Fatal("bundle-diagnostics missing --include-event-summaries")
	}
	if endpointBundleDiagnosticsCmd.Flags().Lookup("include-raw-events") == nil {
		t.Fatal("bundle-diagnostics missing --include-raw-events")
	}
	for _, cmd := range []*cobra.Command{endpointDoctorCmd, topLevelDoctorCmd} {
		if cmd.Flags().Lookup("fix") == nil {
			t.Fatalf("%s missing --fix", cmd.Use)
		}
		if cmd.Flags().Lookup("dry-run") != nil {
			t.Fatalf("%s should not register --dry-run", cmd.Use)
		}
	}
	for _, cmd := range []*cobra.Command{endpointInventoryCmd, topLevelInventoryCmd} {
		for _, name := range []string{"mcp", "skills", "hooks"} {
			if cmd.Flags().Lookup(name) == nil {
				t.Fatalf("%s missing --%s", cmd.Use, name)
			}
		}
	}
}

func TestFilterInventorySectionsKeepsOnlyRequestedBuckets(t *testing.T) {
	old := endpointOpts
	t.Cleanup(func() { endpointOpts = old })
	result := inventoryResult{
		Hooks: map[string]hookTargetResult{
			"cursor": {Target: "cursor", Status: "ok"},
		},
		Configs: []endpointinventory.Config{
			{Runtime: "cursor", ConfigKind: endpointinventory.KindNativeConfig, MCPServerCount: 1},
			{Runtime: "cursor", ConfigKind: endpointinventory.KindHookConfig},
			{Runtime: "claude_code", ConfigKind: endpointinventory.KindNativeConfig},
		},
		MCPServers: []endpointinventory.MCPServer{
			{Runtime: "cursor", ServerName: "filesystem"},
		},
		Skills: []endpointinventory.Skill{
			{Runtime: "cursor", SkillName: "review"},
		},
	}

	endpointOpts.inventoryMCP = true
	filtered := filterInventorySections(result)
	if len(filtered.MCPServers) != 1 || len(filtered.Configs) != 1 {
		t.Fatalf("mcp filter kept wrong inventory: %#v", filtered)
	}
	if filtered.Hooks != nil || filtered.Skills != nil {
		t.Fatalf("mcp filter should drop hooks and skills: %#v", filtered)
	}
	if filtered.Configs[0].MCPServerCount != 1 {
		t.Fatalf("mcp filter kept wrong config: %#v", filtered.Configs)
	}

	endpointOpts.inventoryMCP = false
	endpointOpts.inventoryHooks = true
	endpointOpts.inventorySkills = true
	filtered = filterInventorySections(result)
	if len(filtered.Skills) != 1 || len(filtered.Hooks) != 1 {
		t.Fatalf("hooks+skills filter dropped requested inventory: %#v", filtered)
	}
	if len(filtered.MCPServers) != 0 {
		t.Fatalf("hooks+skills filter should drop MCP servers: %#v", filtered.MCPServers)
	}
	if len(filtered.Configs) != 1 || filtered.Configs[0].ConfigKind != endpointinventory.KindHookConfig {
		t.Fatalf("hooks filter kept wrong configs: %#v", filtered.Configs)
	}
}

func TestEndpointInventoryHooksJSONIncludesDefaultHookStatuses(t *testing.T) {
	old := endpointOpts
	t.Cleanup(func() { endpointOpts = old })
	t.Setenv("HOME", t.TempDir())
	endpointOpts.userMode = true
	endpointOpts.logPath = filepath.Join(t.TempDir(), "runtime.jsonl")
	endpointOpts.jsonOutput = true
	endpointOpts.inventoryHooks = true
	endpointOpts.hookHarnesses = ""

	output, err := captureStdout(t, func() error {
		return runEndpointInventory(endpointInventoryCmd, nil)
	})
	if err != nil {
		t.Fatalf("runEndpointInventory returned error: %v", err)
	}
	var result inventoryResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("inventory JSON did not decode: %v output=%s", err, output)
	}
	if len(result.Hooks) == 0 {
		t.Fatalf("hooks inventory was empty: %#v", result)
	}
	if hook, ok := result.Hooks["cursor"]; !ok || hook.Target != "cursor" {
		t.Fatalf("hooks inventory missing cursor status: %#v", result.Hooks)
	}
}

func TestRunEndpointInventoryWriteEventHonorsConfigContentOptIn(t *testing.T) {
	old := endpointOpts
	t.Cleanup(func() { endpointOpts = old })
	home := t.TempDir()
	t.Setenv("HOME", home)
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	writeTestFile(t, filepath.Join(home, ".cursor", "mcp.json"), `{"mcpServers":{"one":{"command":"npx","env":{"API_TOKEN":"secret"}}}}`)
	include := true
	cfg := endpointconfig.Default(true, logPath)
	cfg.Inventory = &endpointconfig.Inventory{
		Runtimes:        []string{"cursor"},
		IncludeContents: &include,
		MaxContentBytes: 4096,
	}
	if _, err := endpointconfig.Save(cfg); err != nil {
		t.Fatal(err)
	}
	endpointOpts.userMode = true
	endpointOpts.jsonOutput = true
	endpointOpts.writeInventoryEvent = true
	endpointOpts.inventoryContents = false

	output, err := captureStdout(t, func() error {
		return runEndpointInventory(endpointInventoryCmd, nil)
	})
	if err != nil {
		t.Fatalf("runEndpointInventory returned error: %v", err)
	}
	var result inventoryResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("inventory JSON did not decode: %v output=%s", err, output)
	}
	foundContent := false
	for _, config := range result.Configs {
		if config.Runtime == "cursor" && config.Exists && config.Content != nil {
			foundContent = true
			break
		}
	}
	if !foundContent {
		t.Fatalf("manual inventory JSON did not include opted-in content: %#v", result.Configs)
	}
	data, err := os.ReadFile(endpointinventory.LogPath(logPath, true))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"content"`) || !strings.Contains(text, "mcpServers") {
		t.Fatalf("manual inventory snapshot log did not include opted-in content: %s", text)
	}
	if strings.Contains(text, "secret") {
		t.Fatalf("manual inventory snapshot log leaked secret content: %s", text)
	}
}

func TestEndpointUpdateCommandsRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"update"},
		{"update", "enable"},
		{"update", "disable"},
		{"update", "status"},
	} {
		cmd, _, err := endpointCmd.Find(path)
		if err != nil {
			t.Fatalf("endpoint command %v not registered: %v", path, err)
		}
		if cmd == nil {
			t.Fatalf("endpoint command %v not registered", path)
		}
	}
	if cmd, _, err := rootCmd.Find([]string{"update"}); err == nil && cmd != nil && cmd.Name() == "update" {
		t.Fatal("top-level update command should not be registered")
	}
}

func TestConfigAutoUpdateModeDoesNotFallBackOnSystemLoadError(t *testing.T) {
	systemErr := fmt.Errorf("invalid system config")
	got := configAutoUpdateModeFrom(
		func() (endpointconfig.Config, error) { return endpointconfig.Config{}, systemErr },
		func() (endpointconfig.Config, error) {
			return endpointconfig.Config{AutoUpdate: &endpointconfig.AutoUpdate{Mode: "check-only"}}, nil
		},
	)
	if got != "" {
		t.Fatalf("mode = %q, want empty when system config load fails", got)
	}
}

func TestEndpointSystemLogPathUsesSystemPathForPackageInstall(t *testing.T) {
	oldDetect := detectUpdateInstall
	oldLogPath := endpointOpts.logPath
	oldUserMode := endpointOpts.userMode
	oldSystemMode := endpointOpts.systemMode
	t.Cleanup(func() {
		detectUpdateInstall = oldDetect
		endpointOpts.logPath = oldLogPath
		endpointOpts.userMode = oldUserMode
		endpointOpts.systemMode = oldSystemMode
	})
	detectUpdateInstall = func() selfupdate.Install {
		return selfupdate.Install{Kind: selfupdate.InstallSystemPkg, BinaryPath: "/opt/beacon/bin/beacon"}
	}
	endpointOpts.logPath = ""
	endpointOpts.userMode = true
	endpointOpts.systemMode = false

	if got, want := endpointSystemLogPath(), "/var/log/beacon-agent/system.jsonl"; got != want {
		t.Fatalf("endpointSystemLogPath = %q, want %q", got, want)
	}
}

func TestRequireSystemPackageForUpdater(t *testing.T) {
	if err := requireSystemPackageForUpdater(selfupdate.Install{Kind: selfupdate.InstallSystemPkg}); err != nil {
		t.Fatalf("system package rejected: %v", err)
	}
	if err := requireSystemPackageForUpdater(selfupdate.Install{Kind: selfupdate.InstallHomebrew}); err == nil {
		t.Fatal("homebrew install should not be allowed to install system updater")
	}
	if err := requireSystemPackageForUpdater(selfupdate.Install{Kind: selfupdate.InstallOther}); err == nil {
		t.Fatal("other install should not be allowed to install system updater")
	}
}

func TestCompletionAndDocsCommandsRegistered(t *testing.T) {
	for _, name := range []string{"completion", "docs"} {
		cmd, _, err := rootCmd.Find([]string{name})
		if err != nil {
			t.Fatalf("Find %s returned error: %v", name, err)
		}
		if cmd == nil || cmd.Use == "" {
			t.Fatalf("%s command not registered: %#v", name, cmd)
		}
	}
	if docsCmd.Flags().Lookup("output") == nil {
		t.Fatal("docs command missing --output")
	}
}

func TestRedactConfigScrubsSplunkToken(t *testing.T) {
	cfg := endpointconfig.Default(true, "/tmp/runtime.jsonl")
	cfg.Destinations = &endpointconfig.Destinations{SplunkHEC: &endpointconfig.SplunkHEC{
		Endpoint: "https://splunk.example/services/collector",
		Token:    "secret-token",
	}}
	redacted := redactConfig(cfg)
	if redacted.Destinations.SplunkHEC.Token != "[REDACTED]" {
		t.Fatalf("token was not redacted: %#v", redacted.Destinations.SplunkHEC)
	}
	if cfg.Destinations.SplunkHEC.Token != "secret-token" {
		t.Fatal("redactConfig mutated input config")
	}
}

func TestRedactConfigScrubsFalconToken(t *testing.T) {
	cfg := endpointconfig.Default(true, "/tmp/runtime.jsonl")
	cfg.Destinations = &endpointconfig.Destinations{FalconHEC: &endpointconfig.FalconHEC{
		Endpoint: "https://cloud.us.humio.com/api/v1/ingest/hec",
		Token:    "secret-token",
	}}
	redacted := redactConfig(cfg)
	if redacted.Destinations.FalconHEC.Token != "[REDACTED]" {
		t.Fatalf("token was not redacted: %#v", redacted.Destinations.FalconHEC)
	}
	if cfg.Destinations.FalconHEC.Token != "secret-token" {
		t.Fatal("redactConfig mutated input config")
	}
}

func TestAggregateCheckStatus(t *testing.T) {
	if got := aggregateCheckStatus([]diagnostics.Check{{Status: "ok"}}); got != "ok" {
		t.Fatalf("ok aggregate = %q", got)
	}
	if got := aggregateCheckStatus([]diagnostics.Check{{Status: "ok"}, {Status: "warn"}}); got != "warn" {
		t.Fatalf("warn aggregate = %q", got)
	}
	if got := aggregateCheckStatus([]diagnostics.Check{{Status: "warn"}, {Status: "fail"}}); got != "fail" {
		t.Fatalf("fail aggregate = %q", got)
	}
}

func TestConfigValidationCheckReportsInvalidConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := endpointconfig.ConfigPath(true)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not-json"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	check := configValidationCheck(true)
	if check.Status != diagnostics.StatusFail {
		t.Fatalf("config validation status = %#v, want fail", check)
	}
	if !strings.Contains(check.Action, "fix the endpoint config JSON") {
		t.Fatalf("config validation action = %q", check.Action)
	}
}

func TestActionableChecksUsesRequestedRuntimeLogMode(t *testing.T) {
	checks := actionableChecks([]diagnostics.Check{{
		Name:   "runtime_log_source",
		Status: diagnostics.StatusWarn,
	}}, lifecycle.RuntimeLogSource{
		RequestedUserMode: true,
		EffectiveUserMode: false,
	})
	if len(checks) != 1 {
		t.Fatalf("actionableChecks returned %d checks", len(checks))
	}
	if !strings.Contains(checks[0].Action, "stop the system collector") {
		t.Fatalf("runtime log source action = %q", checks[0].Action)
	}
}

func TestPrintDoctorResultIncludesActionsAndSummary(t *testing.T) {
	old := endpointOpts
	t.Cleanup(func() { endpointOpts = old })
	endpointOpts.jsonOutput = false
	result := doctorResult{
		Status: diagnostics.StatusWarn,
		Checks: []diagnostics.Check{
			{Name: "ok_check", Status: diagnostics.StatusOK, Severity: diagnostics.SeverityInfo},
			{Name: "warn_check", Status: diagnostics.StatusWarn, Severity: diagnostics.SeverityLow, Message: "needs attention", Action: "beacon endpoint test-event"},
		},
		GeneratedAt: "2026-06-05T12:00:00Z",
	}

	output, err := captureStdout(t, func() error { return printDoctorResult(result) })
	if err != nil {
		t.Fatalf("printDoctorResult returned error: %v", err)
	}
	if !strings.Contains(output, `action="beacon endpoint test-event"`) {
		t.Fatalf("doctor output missing action: %s", output)
	}
	if !strings.Contains(output, "Summary: 0 failure(s), 1 warning(s)") {
		t.Fatalf("doctor output missing summary: %s", output)
	}
}

func TestPlanDoctorFixesAllowsRuntimeLogCreationAndSkipsInvalidConfig(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	status := lifecycle.Status{
		LogPath:    logPath,
		RuntimeLog: lifecycle.RuntimeLogSource{EffectiveUserMode: true},
	}
	result := doctorResult{
		Checks: []diagnostics.Check{
			{Name: "config_valid", Target: "/tmp/config.json", Status: diagnostics.StatusFail, Message: "invalid json"},
			{Name: "runtime_log_permissions", Target: logPath, Status: diagnostics.StatusWarn, Evidence: "runtime_log_missing"},
			{Name: "collector_reachability", Status: diagnostics.StatusFail},
		},
	}

	plan := planDoctorFixes(result, status)
	if len(plan.Fixes) != 1 || plan.Fixes[0].Action != "create_runtime_log" {
		t.Fatalf("fix plan = %#v, want runtime log creation only", plan)
	}
	foundSkip := false
	for _, skipped := range plan.Skipped {
		if skipped.Action == "repair_collector_service" {
			foundSkip = true
		}
	}
	if !foundSkip {
		t.Fatalf("fix plan did not skip endpoint repair for invalid config: %#v", plan)
	}
}

func TestPlanDoctorFixesDoesNotTreatMissingEventAsAppliedFix(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	status := lifecycle.Status{
		LogPath:    logPath,
		RuntimeLog: lifecycle.RuntimeLogSource{EffectiveUserMode: true},
	}
	result := doctorResult{
		Checks: []diagnostics.Check{
			{Name: "last_event", Target: logPath, Status: diagnostics.StatusWarn, Evidence: "last_event_missing"},
		},
	}

	plan := planDoctorFixes(result, status)
	if len(plan.Fixes) != 0 {
		t.Fatalf("missing last event should not be applied as a fix: %#v", plan)
	}
	if len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Message, "test-event") {
		t.Fatalf("missing last event should suggest test-event: %#v", plan)
	}
}

func TestApplyDoctorFixesContinuesAfterCollectorRepairFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	logPath := filepath.Join(t.TempDir(), "logs", "runtime.jsonl")
	status := lifecycle.Status{
		LogPath:    logPath,
		RuntimeLog: lifecycle.RuntimeLogSource{EffectiveUserMode: true},
	}
	plan := doctorFixPlan{
		Fixes: []plannedAction{
			{Action: "repair_collector_service", Target: filepath.Join(home, "missing-config.json")},
			{Action: "create_runtime_log", Target: logPath},
		},
	}

	err := applyDoctorFixes(plan, status)
	if err == nil {
		t.Fatal("applyDoctorFixes returned nil, want collector repair error")
	}
	if !strings.Contains(err.Error(), "repair_collector_service") {
		t.Fatalf("applyDoctorFixes error = %q, want repair action context", err)
	}
	if _, statErr := os.Stat(logPath); statErr != nil {
		t.Fatalf("runtime log was not created after collector repair failure: %v", statErr)
	}
}

func TestEndpointIntegrationsValidateAllReturnsErrorForBrokenText(t *testing.T) {
	oldAllTargets := endpointOpts.allTargets
	oldJSONOutput := endpointOpts.jsonOutput
	oldLogPath := endpointOpts.logPath
	endpointOpts.allTargets = true
	endpointOpts.jsonOutput = false
	endpointOpts.logPath = filepath.Join(t.TempDir(), "missing.jsonl")
	t.Cleanup(func() {
		endpointOpts.allTargets = oldAllTargets
		endpointOpts.jsonOutput = oldJSONOutput
		endpointOpts.logPath = oldLogPath
	})

	output, err := captureStdout(t, func() error {
		return runEndpointIntegrationsValidate(endpointIntegrationsValidateCmd, nil)
	})
	if err == nil {
		t.Fatal("runEndpointIntegrationsValidate returned nil for broken integrations")
	}
	if !strings.Contains(output, "claude-cowork: broken") {
		t.Fatalf("text output missing claude-cowork broken status: %s", output)
	}
	if !strings.Contains(output, "openclaw: broken") {
		t.Fatalf("text output missing openclaw broken status: %s", output)
	}
}

func TestEndpointIntegrationsValidateAllReturnsErrorForBrokenJSON(t *testing.T) {
	oldAllTargets := endpointOpts.allTargets
	oldJSONOutput := endpointOpts.jsonOutput
	oldLogPath := endpointOpts.logPath
	endpointOpts.allTargets = true
	endpointOpts.jsonOutput = true
	endpointOpts.logPath = filepath.Join(t.TempDir(), "missing.jsonl")
	t.Cleanup(func() {
		endpointOpts.allTargets = oldAllTargets
		endpointOpts.jsonOutput = oldJSONOutput
		endpointOpts.logPath = oldLogPath
	})

	output, err := captureStdout(t, func() error {
		return runEndpointIntegrationsValidate(endpointIntegrationsValidateCmd, nil)
	})
	if err == nil {
		t.Fatal("runEndpointIntegrationsValidate returned nil for broken integrations")
	}
	var results map[string]validationStage
	if err := json.Unmarshal([]byte(output), &results); err != nil {
		t.Fatalf("json output did not decode: %v output=%s", err, output)
	}
	for _, target := range []string{"claude-cowork", "openclaw"} {
		if got := results[target].Status; got != "broken" {
			t.Fatalf("%s status = %q, want broken", target, got)
		}
	}
}

func TestSyntheticEventDestinations(t *testing.T) {
	event := syntheticEvent("pipeline")
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if event.Destination.Type != "pipeline" || event.Destination.Mode != "local_jsonl" {
		t.Fatalf("pipeline destination = %#v json=%s", event.Destination, data)
	}

	rapid7Event := syntheticEvent("rapid7")
	if rapid7Event.Destination.Type != "rapid7" || rapid7Event.Destination.Mode != "custom_logs_webhook_ndjson" {
		t.Fatalf("rapid7 destination = %#v", rapid7Event.Destination)
	}
	if rapid7Event.Message != "Beacon endpoint Rapid7 validation event" {
		t.Fatalf("rapid7 message = %q", rapid7Event.Message)
	}

	s3Event := syntheticEvent("s3")
	if s3Event.Destination.Type != "s3" || s3Event.Destination.Mode != "aws_s3_jsonl" {
		t.Fatalf("s3 destination = %#v", s3Event.Destination)
	}
	if s3Event.Message != "Beacon endpoint S3 validation event" {
		t.Fatalf("s3 message = %q", s3Event.Message)
	}

	cloudwatchEvent := syntheticEvent("cloudwatch")
	if cloudwatchEvent.Destination.Type != "cloudwatch" || cloudwatchEvent.Destination.Mode != "aws_cloudwatch_logs" {
		t.Fatalf("cloudwatch destination = %#v", cloudwatchEvent.Destination)
	}
	if cloudwatchEvent.Message != "Beacon endpoint AWS CloudWatch Logs validation event" {
		t.Fatalf("cloudwatch message = %q", cloudwatchEvent.Message)
	}

	gcsEvent := syntheticEvent("gcs")
	if gcsEvent.Destination.Type != "gcs" || gcsEvent.Destination.Mode != "google_cloud_storage_jsonl" {
		t.Fatalf("gcs destination = %#v", gcsEvent.Destination)
	}
	if gcsEvent.Message != "Beacon endpoint GCS validation event" {
		t.Fatalf("gcs message = %q", gcsEvent.Message)
	}
}

func TestEndpointS3ValidatePrintsAWSCLIInspectionGuidance(t *testing.T) {
	oldLogPath := endpointOpts.logPath
	oldUserMode := endpointOpts.userMode
	oldSystemMode := endpointOpts.systemMode
	endpointOpts.logPath = filepath.Join(t.TempDir(), "runtime.jsonl")
	endpointOpts.userMode = true
	endpointOpts.systemMode = false
	t.Cleanup(func() {
		endpointOpts.logPath = oldLogPath
		endpointOpts.userMode = oldUserMode
		endpointOpts.systemMode = oldSystemMode
	})

	output, err := runEndpointLeaf(t, "s3", "validate")
	if err != nil {
		t.Fatalf("s3 validate returned error: %v", err)
	}
	for _, want := range []string{
		"Validation event written to",
		"destination.type=s3 destination.mode=aws_s3_jsonl",
		"aws s3 ls",
		"aws s3 cp",
		"Beacon endpoint S3 validation event",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("S3 validation output missing %q: %s", want, output)
		}
	}
	if strings.Contains(output, "verified delivery") {
		t.Fatalf("S3 validation output should not claim remote delivery verification: %s", output)
	}
}

func TestEndpointCloudWatchPrintConfigPrintsVectorConfig(t *testing.T) {
	oldLogPath := endpointOpts.logPath
	oldUserMode := endpointOpts.userMode
	oldSystemMode := endpointOpts.systemMode
	endpointOpts.logPath = filepath.Join(t.TempDir(), "runtime.jsonl")
	endpointOpts.userMode = true
	endpointOpts.systemMode = false
	t.Cleanup(func() {
		endpointOpts.logPath = oldLogPath
		endpointOpts.userMode = oldUserMode
		endpointOpts.systemMode = oldSystemMode
	})

	output, err := runEndpointLeaf(t, "cloudwatch", "print-config")
	if err != nil {
		t.Fatalf("endpoint cloudwatch print-config returned error: %v", err)
	}
	for _, want := range []string{
		"aws_cloudwatch_logs",
		"BEACON_CLOUDWATCH_LOG_GROUP",
		"BEACON_CLOUDWATCH_LOG_STREAM_PREFIX",
		endpointOpts.logPath,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("CloudWatch print-config output missing %q: %s", want, output)
		}
	}
}

func TestEndpointCloudWatchValidatePrintsAWSInspectionGuidance(t *testing.T) {
	oldLogPath := endpointOpts.logPath
	oldUserMode := endpointOpts.userMode
	oldSystemMode := endpointOpts.systemMode
	endpointOpts.logPath = filepath.Join(t.TempDir(), "runtime.jsonl")
	endpointOpts.userMode = true
	endpointOpts.systemMode = false
	t.Cleanup(func() {
		endpointOpts.logPath = oldLogPath
		endpointOpts.userMode = oldUserMode
		endpointOpts.systemMode = oldSystemMode
	})

	output, err := runEndpointLeaf(t, "cloudwatch", "validate")
	if err != nil {
		t.Fatalf("cloudwatch validate returned error: %v", err)
	}
	for _, want := range []string{
		"Validation event written to",
		"destination.type=cloudwatch destination.mode=aws_cloudwatch_logs",
		"aws logs filter-log-events",
		"CloudWatch Logs Insights query",
		"Beacon endpoint AWS CloudWatch Logs validation event",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("CloudWatch validation output missing %q: %s", want, output)
		}
	}
	if strings.Contains(output, "verified delivery") {
		t.Fatalf("CloudWatch validation output should not claim remote delivery verification: %s", output)
	}
}

func TestEndpointGCSValidatePrintsGoogleCloudCLIInspectionGuidance(t *testing.T) {
	oldLogPath := endpointOpts.logPath
	oldUserMode := endpointOpts.userMode
	oldSystemMode := endpointOpts.systemMode
	endpointOpts.logPath = filepath.Join(t.TempDir(), "runtime.jsonl")
	endpointOpts.userMode = true
	endpointOpts.systemMode = false
	t.Cleanup(func() {
		endpointOpts.logPath = oldLogPath
		endpointOpts.userMode = oldUserMode
		endpointOpts.systemMode = oldSystemMode
	})

	output, err := runEndpointLeaf(t, "gcs", "validate")
	if err != nil {
		t.Fatalf("gcs validate returned error: %v", err)
	}
	for _, want := range []string{
		"Validation event written to",
		"destination.type=gcs destination.mode=google_cloud_storage_jsonl",
		"gcloud storage ls",
		"gcloud storage cat",
		"Beacon endpoint GCS validation event",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("GCS validation output missing %q: %s", want, output)
		}
	}
	if strings.Contains(output, "verified delivery") {
		t.Fatalf("GCS validation output should not claim remote delivery verification: %s", output)
	}
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	oldStdout := os.Stdout
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writeEnd
	runErr := fn()
	if err := writeEnd.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = oldStdout
	output, err := io.ReadAll(readEnd)
	if err != nil {
		t.Fatal(err)
	}
	if err := readEnd.Close(); err != nil {
		t.Fatal(err)
	}
	return string(output), runErr
}
