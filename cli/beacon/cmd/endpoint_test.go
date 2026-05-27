package cmd

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"

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
	if endpointElasticInstallPackCmd.Flags().Lookup("output") == nil {
		t.Fatal("elastic install-pack command missing --output flag")
	}
	if endpointElasticUpCmd.Flags().Lookup("pack-dir") == nil {
		t.Fatal("elastic up command missing --pack-dir flag")
	}
	if endpointElasticDownCmd.Flags().Lookup("pack-dir") == nil {
		t.Fatal("elastic down command missing --pack-dir flag")
	}
	for _, name := range []string{"user", "system", "log-path"} {
		if endpointElasticDownCmd.Flags().Lookup(name) == nil {
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
	if endpointDatadogInstallPackCmd.Flags().Lookup("output") == nil {
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
	if endpointSumoInstallPackCmd.Flags().Lookup("output") == nil {
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
	if endpointRapid7InstallPackCmd.Flags().Lookup("output") == nil {
		t.Fatal("rapid7 install-pack command missing --output flag")
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

func TestRobustEndpointCommandsRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"doctor"},
		{"inventory"},
		{"test-event"},
		{"bundle-diagnostics"},
		{"config", "show"},
		{"config", "validate"},
		{"config", "set-retention"},
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
