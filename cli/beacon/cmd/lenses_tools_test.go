package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
)

func useLensToolsDefaults(t *testing.T) {
	t.Helper()
	useLensTestHome(t)
	prev := lensToolsOpts
	lensToolsOpts.example, lensToolsOpts.strict, lensToolsOpts.json = false, false, false
	lensToolsOpts.session, lensToolsOpts.trace, lensToolsOpts.logPath = "", "", ""
	lensToolsOpts.addr, lensToolsOpts.open = lensPreviewAddr, false
	t.Cleanup(func() { lensToolsOpts = prev })
}

func writeLensLog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	lines := []string{
		`{"timestamp":"2026-06-11T10:00:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"id":"old-1","kind":"agent_runtime","action":"prompt.submitted","category":"prompt"},"severity":"info","endpoint":{"os":"linux"},"harness":{"name":"claude_code"},"session":{"id":"older"},"prompt":{"text":"first"},"message":"prompt.submitted"}`,
		`{"timestamp":"2026-06-11T11:00:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"id":"new-1","kind":"agent_runtime","action":"command.executed","category":"command"},"severity":"info","endpoint":{"os":"linux"},"harness":{"name":"claude_code"},"session":{"id":"newer"},"command":{"command":"ls"},"message":"command.executed"}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLensesSpecPrintsTheSpecAndAnExample(t *testing.T) {
	useLensToolsDefaults(t)
	cmd, buf := newCmd()
	if err := lensesSpecCmd.RunE(cmd, nil); err != nil || !strings.HasPrefix(buf.String(), "# Lenses — Specification") {
		t.Fatalf("spec = %v, %.60q", err, buf.String())
	}
	lensToolsOpts.example = true
	cmd, buf = newCmd()
	if err := lensesSpecCmd.RunE(cmd, nil); err != nil || !strings.Contains(buf.String(), `<script type="application/beacon-lens+json">`) {
		t.Fatalf("spec --example = %v", err)
	}
}

func TestLensesLint(t *testing.T) {
	useLensToolsDefaults(t)
	dir := t.TempDir()
	clean := writeTestLens(t, dir, "clean", "1")
	warn := filepath.Join(dir, "warn.html")
	if err := os.WriteFile(warn, []byte(`<script type="application/beacon-lens+json">{"id":"warn","title":"W","version":1,"api":"beacon.lens.v1"}</script>
<script>window.beacon.getTrace().then(function (d) { document.body.innerHTML = d.trace.id; });</script>`), 0o644); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(dir, "broken.html")
	if err := os.WriteFile(broken, []byte("<p>nothing</p>"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd, buf := newCmd()
	if err := lensesLintCmd.RunE(cmd, []string{clean}); err != nil || !strings.Contains(buf.String(), "0 error(s), 0 warning(s) in 1 file(s).") {
		t.Fatalf("clean = %v, %q", err, buf.String())
	}
	cmd, buf = newCmd()
	if err := lensesLintCmd.RunE(cmd, []string{warn}); err != nil || !strings.Contains(buf.String(), warn+":2: warning:") {
		t.Fatalf("warning = %v, %q", err, buf.String())
	}
	lensToolsOpts.strict = true
	cmd, _ = newCmd()
	if err := lensesLintCmd.RunE(cmd, []string{warn}); err == nil {
		t.Fatal("--strict passed a lens with warnings")
	}
	lensToolsOpts.strict = false
	cmd, _ = newCmd()
	if err := lensesLintCmd.RunE(cmd, []string{clean, broken}); err == nil || !strings.Contains(err.Error(), "lint error") {
		t.Fatalf("broken = %v", err)
	}

	lensToolsOpts.json = true
	cmd, buf = newCmd()
	_ = lensesLintCmd.RunE(cmd, []string{warn, clean})
	var report struct {
		Files []struct {
			Path     string `json:"path"`
			Findings []struct {
				Line     int    `json:"line"`
				Severity string `json:"severity"`
			} `json:"findings"`
		} `json:"files"`
		Warnings int `json:"warnings"`
	}
	if err := json.Unmarshal(buf.Bytes(), &report); err != nil || len(report.Files) != 2 || report.Warnings == 0 || len(report.Files[1].Findings) != 0 {
		t.Fatalf("lint --json = %v, %s", err, buf.String())
	}
}

func TestLensesDataPrintsWhatALensReceives(t *testing.T) {
	useLensToolsDefaults(t)
	lensToolsOpts.logPath = writeLensLog(t)

	cmd, buf := newCmd()
	if err := lensesDataCmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	var data dashboard.LensDataV1
	if err := json.Unmarshal(buf.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if data.APIVersion != "beacon.lens.v1" || data.Trace.ID != "session:claude_code:newer" {
		t.Fatalf("default data = %s, %s; want the most recent session", data.APIVersion, data.Trace.ID)
	}

	lensToolsOpts.session = "older"
	cmd, buf = newCmd()
	if err := lensesDataCmd.RunE(cmd, nil); err != nil || !strings.Contains(buf.String(), `"id": "session:claude_code:older"`) {
		t.Fatalf("--session = %v", err)
	}
	lensToolsOpts.session = ""
	lensToolsOpts.trace = "session:claude_code:older"
	cmd, _ = newCmd()
	if err := lensesDataCmd.RunE(cmd, nil); err != nil {
		t.Fatalf("--trace = %v", err)
	}
	lensToolsOpts.session = "older"
	if err := lensesDataCmd.RunE(cmd, nil); err == nil {
		t.Fatal("--session with --trace was accepted")
	}
	lensToolsOpts.session, lensToolsOpts.trace = "nope", ""
	if err := lensesDataCmd.RunE(cmd, nil); err == nil {
		t.Fatal("an unknown session was accepted")
	}
}

func TestLensesPreviewServesTheFileAgainstARealSession(t *testing.T) {
	useLensToolsDefaults(t)
	lensToolsOpts.logPath = writeLensLog(t)
	var served dashboard.Options
	prev := dashboardListenAndServe
	dashboardListenAndServe = func(opts dashboard.Options) error { served = opts; return nil }
	t.Cleanup(func() { dashboardListenAndServe = prev })

	file := writeTestLens(t, t.TempDir(), "my-lens", "1")
	cmd, buf := newCmd()
	if err := lensesPreviewCmd.RunE(cmd, []string{file}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Open: http://127.0.0.1:8766/session.html?id=newer&lens=my-lens") {
		t.Fatalf("preview output = %q", buf.String())
	}
	if served.Addr != lensPreviewAddr || len(served.LensFiles) != 1 || !filepath.IsAbs(served.LensFiles[0]) || served.LogPath != lensToolsOpts.logPath {
		t.Fatalf("served = %+v", served)
	}

	cmd, _ = newCmd()
	if err := lensesPreviewCmd.RunE(cmd, []string{writeTestLens(t, t.TempDir(), "activity", "1")}); err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Fatalf("built-in id = %v", err)
	}
	broken := filepath.Join(t.TempDir(), "broken.html")
	if err := os.WriteFile(broken, []byte("<p>no manifest</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := lensesPreviewCmd.RunE(cmd, []string{broken}); err == nil {
		t.Fatal("a lens with lint errors was previewed")
	}
	lensToolsOpts.addr = "0.0.0.0:8766"
	if err := lensesPreviewCmd.RunE(cmd, []string{file}); err == nil {
		t.Fatal("a non-loopback preview address was accepted")
	}
}
