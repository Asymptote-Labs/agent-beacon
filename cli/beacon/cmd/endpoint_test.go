package cmd

import (
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
