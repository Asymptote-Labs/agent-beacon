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
