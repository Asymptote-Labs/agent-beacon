package cmd

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestHandoffListIncludesLogOnlySession(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	stubHandoffSources(t)
	previous := handoffOpts
	t.Cleanup(func() { handoffOpts = previous })
	handoffOpts = handoffOptions{jsonOutput: true, logPath: handoffLog(t, "gemini_cli", "gem-1", "hello from gemini")}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := runHandoffList(cmd, nil); err != nil {
		t.Fatal(err)
	}
	var result handoffListResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 1 || result.Sessions[0].ID != "gem-1" {
		t.Fatalf("want log-only gem-1 session, got %s", out.String())
	}
}
