package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/spf13/cobra"
)

// Every session-store sync takes --rotate-bytes and refuses one smaller than the shared size
// before reading or writing anything (#973).
func TestEverySessionSyncRefusesARotateBytesBelowTheDefault(t *testing.T) {
	cases := []struct {
		name string
		cmd  *cobra.Command
		run  func(*cobra.Command, []string) error
		opt  *int64
	}{
		{"claude", endpointClaudeSyncCmd, runEndpointClaudeSync, &endpointClaudeOpts.rotateBytes},
		{"devin", endpointDevinSyncCmd, runEndpointDevinSync, &endpointDevinOpts.rotateBytes},
		{"codex", endpointCodexSyncCmd, runEndpointCodexSync, &endpointCodexOpts.rotateBytes},
		{"cline", endpointClineSyncCmd, runEndpointClineSync, &endpointClineOpts.rotateBytes},
		{"copilot", endpointCopilotSyncCmd, runEndpointCopilotSync, &endpointCopilotOpts.rotateBytes},
		{"cursor", endpointCursorSyncCmd, runEndpointCursorSync, &endpointCursorOpts.rotateBytes},
		{"dsh", endpointDshSyncCmd, runEndpointDshSync, &endpointDshOpts.rotateBytes},
		{"factory", endpointFactorySyncCmd, runEndpointFactorySync, &endpointFactoryOpts.rotateBytes},
		{"fx", endpointFxSyncCmd, runEndpointFxSync, &endpointFxOpts.rotateBytes},
		{"grok", endpointGrokSyncCmd, runEndpointGrokSync, &endpointGrokOpts.rotateBytes},
		{"hermes", endpointHermesSyncCmd, runEndpointHermesSync, &endpointHermesOpts.rotateBytes},
		{"openclaw sessions", endpointOpenClawSessionsSyncCmd, runEndpointOpenClawSessionsSync, &endpointOpenClawSessionsOpts.rotateBytes},
		{"opencode", endpointOpenCodeSyncCmd, runEndpointOpenCodeSync, &endpointOpenCodeOpts.rotateBytes},
		{"pi", endpointPiSyncCmd, runEndpointPiSync, &endpointPiOpts.rotateBytes},
		{"prime", endpointPrimeSyncCmd, runEndpointPrimeSync, &endpointPrimeOpts.rotateBytes},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			testenv.SetHome(t, home)
			if tc.cmd.Flags().Lookup("rotate-bytes") == nil {
				t.Fatal("sync has no --rotate-bytes flag")
			}
			*tc.opt = 1024
			t.Cleanup(func() { *tc.opt = 0 })
			var out bytes.Buffer
			tc.cmd.SetOut(&out)
			tc.cmd.SetErr(&out)
			t.Cleanup(func() { tc.cmd.SetOut(nil); tc.cmd.SetErr(nil) })

			err := tc.run(tc.cmd, nil)
			if err == nil || !strings.Contains(err.Error(), "--rotate-bytes") {
				t.Fatalf("err = %v, want a --rotate-bytes error", err)
			}
			if _, statErr := os.Stat(filepath.Join(home, ".beacon")); !os.IsNotExist(statErr) {
				t.Error("a refused --rotate-bytes still wrote endpoint state")
			}
		})
	}
}

func TestReportSyncRetention(t *testing.T) {
	var out bytes.Buffer
	reportSyncRetention(&out, sessionsync.Retention{})
	if out.Len() != 0 {
		t.Fatalf("an unlimited sweep reported %q", out.String())
	}
	reportSyncRetention(&out, sessionsync.Retention{RetentionLimited: true, EventsRotatedOut: 3})
	for _, want := range []string{"left for the next sweep", "--rotate-bytes", "3 event(s)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report %q lacks %q", out.String(), want)
		}
	}
	if checkSyncRotateBytes(0) != nil || checkSyncRotateBytes(writer.DefaultRotateBytes) != nil || checkSyncRotateBytes(writer.DefaultRotateBytes-1) == nil {
		t.Error("checkSyncRotateBytes accepts the wrong sizes")
	}
}
