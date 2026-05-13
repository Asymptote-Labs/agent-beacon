package cmd

import "testing"

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
