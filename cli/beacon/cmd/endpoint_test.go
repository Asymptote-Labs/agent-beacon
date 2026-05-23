package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

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

func TestEndpointCrowdStrikeCommandsRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"crowdstrike", "print-config"},
		{"crowdstrike", "install-pack"},
		{"crowdstrike", "validate"},
	} {
		cmd, _, err := endpointCmd.Find(path)
		if err != nil {
			t.Fatalf("Find %v returned error: %v", path, err)
		}
		if cmd == nil || cmd.Use != path[len(path)-1] {
			t.Fatalf("crowdstrike command %v not registered: %#v", path, cmd)
		}
		for _, name := range []string{"user", "system", "log-path"} {
			if cmd.Flags().Lookup(name) == nil {
				t.Fatalf("crowdstrike command %v missing --%s flag", path, name)
			}
		}
	}
	if endpointCrowdStrikeInstallPackCmd.Flags().Lookup("output") == nil {
		t.Fatal("crowdstrike install-pack command missing --output flag")
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
