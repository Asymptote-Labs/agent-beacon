package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/tokens"
)

func TestEndpointTokensCommandRegistered(t *testing.T) {
	cmd, _, err := endpointCmd.Find([]string{"tokens"})
	if err != nil {
		t.Fatalf("Find tokens returned error: %v", err)
	}
	if cmd == nil || cmd.Use != "tokens" {
		t.Fatalf("tokens command not registered: %#v", cmd)
	}
	for _, flag := range []string{"log-path", "json", "since", "until", "session", "model", "harness", "repository", "run-id", "bucket", "top"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Fatalf("tokens command missing --%s flag", flag)
		}
	}
}

func writeTokensFixtureLog(t *testing.T) string {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	lines := []string{
		`{"timestamp":"2026-06-11T10:00:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"token.usage","category":"metric"},"severity":"info","endpoint":{"os":"darwin"},"harness":{"name":"claude_code"},"session":{"id":"session-1"},"model":"claude-sonnet-4-5","gen_ai":{"usage":{"input_tokens":100}},"message":"claude_code.token.usage","raw":{"metric_name":"claude_code.token.usage","metric_temporality":"Delta"}}`,
		`{"timestamp":"2026-06-11T10:00:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"token.usage","category":"metric"},"severity":"info","endpoint":{"os":"darwin"},"harness":{"name":"claude_code"},"session":{"id":"session-1"},"model":"claude-sonnet-4-5","gen_ai":{"usage":{"output_tokens":40}},"message":"claude_code.token.usage","raw":{"metric_name":"claude_code.token.usage","metric_temporality":"Delta"}}`,
		`{"timestamp":"2026-06-11T10:05:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"cost.usage","category":"metric"},"severity":"info","endpoint":{"os":"darwin"},"harness":{"name":"claude_code"},"session":{"id":"session-1"},"model":"claude-sonnet-4-5","run":{"provider":"github_actions","run_id":"777"},"gen_ai":{"usage":{"cost_usd":0.5}},"message":"claude_code.cost.usage","raw":{"metric_name":"claude_code.cost.usage","metric_temporality":"Delta"}}`,
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatalf("write fixture log: %v", err)
	}
	return logPath
}

func runTokensCommand(t *testing.T, args ...string) string {
	t.Helper()
	endpointTokensOpts = struct {
		userMode   bool
		systemMode bool
		logPath    string
		jsonOutput bool
		since      string
		until      string
		session    string
		model      string
		harness    string
		repository string
		runID      string
		bucket     string
		top        int
	}{userMode: true}
	if err := endpointTokensCmd.Flags().Parse(args); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	var out bytes.Buffer
	endpointTokensCmd.SetOut(&out)
	defer endpointTokensCmd.SetOut(nil)
	if err := runEndpointTokens(endpointTokensCmd, nil); err != nil {
		t.Fatalf("runEndpointTokens returned error: %v", err)
	}
	return out.String()
}

func TestEndpointTokensTextReport(t *testing.T) {
	logPath := writeTokensFixtureLog(t)
	output := runTokensCommand(t, "--log-path", logPath)
	for _, want := range []string{
		"3 of 3 events carry usage",
		"claude-sonnet-4-5",
		"session-1",
		"claude_code",
		"github_actions/777",
		"0.5000",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("text report missing %q:\n%s", want, output)
		}
	}
}

func TestEndpointTokensJSONReport(t *testing.T) {
	logPath := writeTokensFixtureLog(t)
	output := runTokensCommand(t, "--log-path", logPath, "--json", "--session", "session-1")
	var report tokens.Report
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("unmarshal JSON report: %v\n%s", err, output)
	}
	if report.Totals.InputTokens != 100 || report.Totals.OutputTokens != 40 || report.Totals.CostUSD != 0.5 {
		t.Fatalf("totals = %#v", report.Totals)
	}
	if len(report.ByRun) != 1 || report.ByRun[0].Key != "github_actions/777" {
		t.Fatalf("by_run = %#v", report.ByRun)
	}
	if report.SessionDetail == nil || report.SessionDetail.SessionID != "session-1" || len(report.SessionDetail.Steps) != 3 {
		t.Fatalf("session detail = %#v", report.SessionDetail)
	}
}

func TestEndpointTokensEmptyLogSucceeds(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	output := runTokensCommand(t, "--log-path", logPath)
	if !strings.Contains(output, "0 of 0 events carry usage") {
		t.Fatalf("empty report = %q", output)
	}
}
