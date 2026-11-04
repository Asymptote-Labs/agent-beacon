package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/handoff"
)

// Regression (e2a4cfc2): Beacon's Devin CLI hooks record "devin-cli", older ones recorded "devin".
// A session known only from the runtime log takes the registry's harness whichever name its rows
// carry, so export reports the runtime handoff can start and --harness finds rows of both names.
func TestHandoffExportFindsLogSessionsRecordedUnderARuntimeAlias(t *testing.T) {
	stubHandoffClock(t)
	stubHandoffSources(t, stubHandoffSource{harness: handoff.HarnessClaude})
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	var rows []string
	for harness, id := range map[string]string{"devin-cli": "devin-new-123", "devin": "devin-old-456"} {
		data, err := os.ReadFile(handoffLog(t, harness, id, "prompt for "+id))
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, string(data))
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(rows, "")), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ id, harnessFlag, wantID string }{
		{"devin-new-123", "", "devin-new-123"},
		{"devin-old-456", "", "devin-old-456"},
		{"devin-old", "devin-cli", "devin-old-456"},
		{"devin-new", "devin", "devin-new-123"},
	} {
		args := []string{"export", tc.id, "--json", "--log-path", logPath, "--output-dir", t.TempDir()}
		if tc.harnessFlag != "" {
			args = append(args, "--harness", tc.harnessFlag)
		}
		out, _, err := runHandoff(t, args...)
		if err != nil {
			t.Fatalf("export %s --harness %q: %v", tc.id, tc.harnessFlag, err)
		}
		var result handoffExportResult
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("export %s: %v\n%s", tc.id, err, out)
		}
		if result.From != handoff.FromRuntimeLog || result.Session.ID != tc.wantID || result.Session.Harness != handoff.HarnessDevin {
			t.Fatalf("export %s --harness %q = %+v; want %s from the runtime log as %s", tc.id, tc.harnessFlag, result, tc.wantID, handoff.HarnessDevin)
		}
	}
}
