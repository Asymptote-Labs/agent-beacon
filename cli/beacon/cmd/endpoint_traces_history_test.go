package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/onboarding"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// traceHistoryFixture gives the test its own home, history path and a small runtime log, and
// replaces the prompt's terminal with answers.
func traceHistoryFixture(t *testing.T, interactive bool, answers string) (logPath, historyPath string, prompt *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	historyPath = filepath.Join(home, ".beacon", "endpoint", "history.db")
	t.Setenv(endpointconfig.HistoryStoreEnv, historyPath)
	t.Setenv("CI", "")
	logPath = filepath.Join(home, "logs", "runtime.jsonl")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `","event":{"id":"e1","action":"prompt.submitted","category":"prompt"},"harness":{"name":"cursor"},"session":{"id":"s1"},"prompt":{"text":"keep this session"}}` + "\n"
	if err := os.WriteFile(logPath, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	prompt = &bytes.Buffer{}
	previous := traceHistoryIO
	traceHistoryIO.in = strings.NewReader(answers)
	traceHistoryIO.out = prompt
	traceHistoryIO.interactive = func() bool { return interactive }
	previousOpts, previousHistory := endpointOpts, endpointTraceHistoryOpts
	endpointOpts.logPath = logPath
	endpointOpts.jsonOutput = false
	t.Cleanup(func() {
		traceHistoryIO = previous
		endpointOpts, endpointTraceHistoryOpts = previousOpts, previousHistory
	})
	return logPath, historyPath, prompt
}

func TestTraceHistoryIsNeverPromptedWithoutATerminal(t *testing.T) {
	logPath, historyPath, prompt := traceHistoryFixture(t, false, "1\n")
	offerTraceHistory(logPath, true)
	if prompt.Len() != 0 {
		t.Fatalf("prompted without a terminal: %q", prompt.String())
	}
	if _, err := os.Stat(historyPath); !os.IsNotExist(err) {
		t.Fatalf("history created without an answer: %v", err)
	}
}

func TestTraceHistoryIsNeverPromptedForJSON(t *testing.T) {
	logPath, historyPath, prompt := traceHistoryFixture(t, true, "1\n")
	endpointOpts.jsonOutput = true
	offerTraceHistory(logPath, true)
	if prompt.Len() != 0 {
		t.Fatalf("prompted for --json: %q", prompt.String())
	}
	if _, err := os.Stat(historyPath); !os.IsNotExist(err) {
		t.Fatalf("history created for --json: %v", err)
	}
}

func TestTraceHistoryPromptCreatesTheStore(t *testing.T) {
	logPath, historyPath, prompt := traceHistoryFixture(t, true, "1\n")
	offerTraceHistory(logPath, true)
	if !strings.Contains(prompt.String(), "Keep a local history") || !strings.Contains(prompt.String(), "Local history ready: 1 traces, 1 events") {
		t.Fatalf("prompt output = %q", prompt.String())
	}
	if _, err := os.Stat(historyPath); err != nil {
		t.Fatalf("history not created: %v", err)
	}
	// Asked once: with the store in place the next command says nothing.
	prompt.Reset()
	offerTraceHistory(logPath, true)
	if prompt.Len() != 0 {
		t.Fatalf("prompted again after creating the store: %q", prompt.String())
	}
}

func TestTraceHistoryPromptRemembersNotNow(t *testing.T) {
	logPath, historyPath, prompt := traceHistoryFixture(t, true, "2\n")
	offerTraceHistory(logPath, true)
	if _, err := os.Stat(historyPath); !os.IsNotExist(err) {
		t.Fatalf("history created after \"Not now\": %v", err)
	}
	if !onboarding.Load().HistoryDeclined() {
		t.Fatal("\"Not now\" was not recorded in the profile")
	}
	prompt.Reset()
	traceHistoryIO.in = strings.NewReader("1\n")
	offerTraceHistory(logPath, true)
	if prompt.Len() != 0 {
		t.Fatalf("asked again after \"Not now\": %q", prompt.String())
	}
	// reindex still opts in.
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := runEndpointTracesReindex(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(historyPath); err != nil {
		t.Fatalf("reindex did not create the history: %v", err)
	}
}

func TestTraceHistoryResetNeedsConfirmation(t *testing.T) {
	logPath, historyPath, _ := traceHistoryFixture(t, false, "")
	if err := dashboard.ReindexTraceStore(logPath); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	endpointTracesResetCmd.SetOut(&out)
	if err := endpointTracesResetCmd.RunE(endpointTracesResetCmd, nil); err == nil {
		t.Fatal("reset without --yes and without a terminal should refuse")
	}
	if _, err := os.Stat(historyPath); err != nil {
		t.Fatalf("history removed without confirmation: %v", err)
	}
	endpointTraceHistoryOpts.yes = true
	if err := endpointTracesResetCmd.RunE(endpointTracesResetCmd, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(historyPath); !os.IsNotExist(err) {
		t.Fatalf("history still present after reset --yes: %v", err)
	}
}

func TestTraceHistoryReindexRejectsOutOfRangeLimits(t *testing.T) {
	traceHistoryFixture(t, false, "")
	for _, set := range []func(){
		func() { endpointTraceHistoryOpts.retentionDays = -1 },
		func() { endpointTraceHistoryOpts.retentionDays = maxHistoryRetentionDays + 1 },
		func() { endpointTraceHistoryOpts.maxSizeMB = -1 },
		func() { endpointTraceHistoryOpts.maxSizeMB = maxHistorySizeMB + 1 },
	} {
		endpointTraceHistoryOpts.retentionDays, endpointTraceHistoryOpts.maxSizeMB = 0, 0
		set()
		if err := runEndpointTracesReindex(&cobra.Command{}, nil); err == nil {
			t.Fatalf("reindex accepted retention %d, size %d", endpointTraceHistoryOpts.retentionDays, endpointTraceHistoryOpts.maxSizeMB)
		}
	}
}
