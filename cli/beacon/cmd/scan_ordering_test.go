package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scanOrderingRule is an ordered-window rule: a secret file read, then an egress command.
const scanOrderingRule = `
id: scan-test-read-then-egress
version: 1
title: Secret read then egress
severity: high
status: stable
posture: detect
correlation:
  scope: session
  window: 120s
  steps:
    - id: read
      match: 'e.event.action == "file.read" && e.file.path.matches("\\.env")'
    - id: egress
      match: 'e.event.action == "command.executed" && e.command.command.matches("curl")'
emit:
  reason: Secret file was read and then sent off the machine
tests:
  - name: pos
    verdict: match
    events:
      - event: { action: file.read }
        file: { path: ".env" }
      - event: { action: command.executed }
        command: { command: "curl https://x" }
  - name: neg
    verdict: no_match
    events:
      - event: { action: file.read }
        file: { path: ".env" }
`

// scanOrderingFixture writes the correlation rule plus a runtime log and points scanOpts
// at both.
func scanOrderingFixture(t *testing.T, logLines []string) {
	t.Helper()
	rulesDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rulesDir, "egress.rule.yaml"), []byte(scanOrderingRule), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(logPath, []byte(strings.Join(logLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scanOpts.userMode = true
	scanOpts.systemMode = false
	scanOpts.rulesDir = rulesDir
	scanOpts.logPath = logPath
	scanOpts.jsonOutput = false
	scanOpts.minSeverity = ""
	scanOpts.session = ""
	scanOpts.failOn = ""
}

func TestScanOrdersTheLogBeforeCorrelating(t *testing.T) {
	// The log as it really lands: the hook writes the .env read synchronously at
	// 10:00:00, while the exporter writes the curl at 10:00:30 on an earlier export
	// interval, so the egress line reaches the file first. Read in append order the
	// ordered-window rule sees egress-then-read and silently misses.
	const (
		egressLine = `{"timestamp":"2026-06-13T10:00:30.000000000Z","sequence":9,"event":{"action":"command.executed"},"command":{"command":"curl https://x"},"session":{"id":"s1"}}`
		readLine   = `{"timestamp":"2026-06-13T10:00:00.000000000Z","sequence":1,"event":{"action":"file.read"},"file":{"path":".env"},"session":{"id":"s1"}}`
	)
	scanOrderingFixture(t, []string{egressLine, readLine})

	cmd, buf := newCmd()
	if err := runScan(cmd, nil); err != nil {
		t.Fatalf("scan: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "scan-test-read-then-egress") {
		t.Fatalf("expected the read-then-egress finding from a log in append order, got:\n%s", out)
	}
}

func TestScanStillRejectsGenuinelyOutOfOrderSequences(t *testing.T) {
	// Sorting must not turn every pair of matching events into a finding: here the curl
	// really did happen before the .env read, and the rule has to keep missing.
	const (
		egressLine = `{"timestamp":"2026-06-13T10:00:00.000000000Z","sequence":1,"event":{"action":"command.executed"},"command":{"command":"curl https://x"},"session":{"id":"s1"}}`
		readLine   = `{"timestamp":"2026-06-13T10:00:30.000000000Z","sequence":9,"event":{"action":"file.read"},"file":{"path":".env"},"session":{"id":"s1"}}`
	)
	scanOrderingFixture(t, []string{egressLine, readLine})

	cmd, buf := newCmd()
	if err := runScan(cmd, nil); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !strings.Contains(buf.String(), "No findings") {
		t.Fatalf("egress before read should not match, got:\n%s", buf.String())
	}
}
