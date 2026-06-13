package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
)

const scanTestRule = `
id: scan-test-secret-read
version: 1
title: Secret file read
severity: high
status: stable
posture: detect
match: 'e.event.action == "file.read" && e.file.path.matches("\\.env")'
emit:
  reason: Secret file was read
tests:
  - name: pos
    verdict: match
    events:
      - event: { action: file.read }
        file: { path: ".env" }
  - name: neg
    verdict: no_match
    events:
      - event: { action: file.read }
        file: { path: "README.md" }
`

// scanFixture writes a temp rules dir + a temp runtime log, and points scanOpts at them.
func scanFixture(t *testing.T, logLines []string) {
	t.Helper()
	rulesDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rulesDir, "secret.rule.yaml"), []byte(scanTestRule), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(logPath, []byte(strings.Join(logLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// reset and set scan options for this test
	scanOpts.userMode = true
	scanOpts.systemMode = false
	scanOpts.rulesDir = rulesDir
	scanOpts.logPath = logPath
	scanOpts.jsonOutput = false
	scanOpts.minSeverity = ""
	scanOpts.session = ""
	scanOpts.failOn = ""
}

const (
	evSecretRead = `{"timestamp":"2026-06-13T10:00:00Z","event":{"action":"file.read"},"file":{"path":".env"},"session":{"id":"s1"}}`
	evBenignRead = `{"timestamp":"2026-06-13T10:00:01Z","event":{"action":"file.read"},"file":{"path":"README.md"},"session":{"id":"s1"}}`
)

func TestScanReportsFindings(t *testing.T) {
	scanFixture(t, []string{evSecretRead, evBenignRead})
	cmd, buf := newCmd()
	if err := runScan(cmd, nil); err != nil {
		t.Fatalf("scan: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "scan-test-secret-read") {
		t.Fatalf("expected finding for the .env read, got:\n%s", out)
	}
	if !strings.Contains(out, "1 finding") {
		t.Fatalf("expected exactly one finding, got:\n%s", out)
	}
}

func TestScanNoFindings(t *testing.T) {
	scanFixture(t, []string{evBenignRead})
	cmd, buf := newCmd()
	if err := runScan(cmd, nil); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !strings.Contains(buf.String(), "No findings") {
		t.Fatalf("expected no findings, got:\n%s", buf.String())
	}
}

func TestScanJSON(t *testing.T) {
	scanFixture(t, []string{evSecretRead})
	scanOpts.jsonOutput = true
	cmd, buf := newCmd()
	if err := runScan(cmd, nil); err != nil {
		t.Fatalf("scan: %v", err)
	}
	var findings []threatrules.Finding
	if err := json.Unmarshal(buf.Bytes(), &findings); err != nil {
		t.Fatalf("scan --json did not emit valid JSON: %v\n%s", err, buf.String())
	}
	if len(findings) != 1 || findings[0].RuleID != "scan-test-secret-read" {
		t.Fatalf("unexpected json findings: %+v", findings)
	}
	if len(findings[0].Events) != 1 || findings[0].Events[0].File == nil {
		t.Fatalf("expected evidence event with file info: %+v", findings[0])
	}
}

func TestScanSessionFilterMatchesDashboardQuery(t *testing.T) {
	secretReadMixedCaseSession := `{"timestamp":"2026-06-13T10:00:00Z","event":{"action":"file.read"},"file":{"path":".env"},"session":{"id":"Session-ABC-123"}}`
	secretReadOtherSession := `{"timestamp":"2026-06-13T10:00:01Z","event":{"action":"file.read"},"file":{"path":".env"},"session":{"id":"session-other"}}`
	scanFixture(t, []string{secretReadMixedCaseSession, secretReadOtherSession})
	scanOpts.session = "abc"
	cmd, buf := newCmd()
	if err := runScan(cmd, nil); err != nil {
		t.Fatalf("scan: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "1 finding(s) across 1 events") {
		t.Fatalf("expected partial case-insensitive session filter to scan exactly one event, got:\n%s", out)
	}
	if !strings.Contains(out, "session=Session-ABC-123") {
		t.Fatalf("expected finding from the matching session, got:\n%s", out)
	}
	if strings.Contains(out, "session=session-other") {
		t.Fatalf("did not expect finding from the non-matching session, got:\n%s", out)
	}
}

func TestScanFailOn(t *testing.T) {
	scanFixture(t, []string{evSecretRead})
	scanOpts.failOn = "high"
	cmd, _ := newCmd()
	if err := runScan(cmd, nil); err == nil {
		t.Fatalf("expected non-zero exit (error) with --fail-on high and a high finding")
	}

	// Below threshold: critical is higher than the high finding -> no failure.
	scanFixture(t, []string{evSecretRead})
	scanOpts.failOn = "critical"
	cmd, _ = newCmd()
	if err := runScan(cmd, nil); err != nil {
		t.Fatalf("did not expect failure when no finding reaches --fail-on critical: %v", err)
	}
}

// TestScanFailOnIgnoresMinSeverityFilter guards the CI gate: --min-severity is a
// display-only filter, so a detection at or above --fail-on must still fail the
// scan even when --min-severity hides it from the printed output.
func TestScanFailOnIgnoresMinSeverityFilter(t *testing.T) {
	scanFixture(t, []string{evSecretRead})
	scanOpts.minSeverity = "critical" // high finding filtered out of output
	scanOpts.failOn = "high"          // ...but still meets the fail gate
	cmd, buf := newCmd()
	err := runScan(cmd, nil)
	if err == nil {
		t.Fatalf("expected non-zero exit: a high finding meets --fail-on high even when --min-severity critical hides it; output:\n%s", buf.String())
	}
	if !strings.Contains(err.Error(), "1 finding") {
		t.Fatalf("expected fail gate to count the filtered finding, got: %v", err)
	}
	if !strings.Contains(buf.String(), "No findings") {
		t.Fatalf("expected the high finding hidden from output by --min-severity critical, got:\n%s", buf.String())
	}
}

func TestScanMinSeverityFilters(t *testing.T) {
	scanFixture(t, []string{evSecretRead})
	scanOpts.minSeverity = "critical" // finding is high -> filtered out
	cmd, buf := newCmd()
	if err := runScan(cmd, nil); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !strings.Contains(buf.String(), "No findings") {
		t.Fatalf("expected high finding filtered by --min-severity critical, got:\n%s", buf.String())
	}
}
