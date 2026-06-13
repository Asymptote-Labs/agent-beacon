package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const validRuleYAML = `
id: cli-test-rule
version: 1
title: CLI test rule
severity: low
status: experimental
posture: detect
match: 'e.event.action == "file.read"'
emit:
  reason: ok
tests:
  - name: p
    verdict: match
    events:
      - event: { action: file.read }
`

func writeRuleFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func newCmd() (*cobra.Command, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	c := &cobra.Command{}
	c.SetOut(buf)
	c.SetErr(buf)
	return c, buf
}

func TestRulesLintSuccess(t *testing.T) {
	dir := t.TempDir()
	writeRuleFile(t, dir, "ok.rule.yaml", validRuleYAML)
	cmd, buf := newCmd()
	if err := lintRulesPath(cmd, dir); err != nil {
		t.Fatalf("lint returned error: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "ok   cli-test-rule") {
		t.Fatalf("expected ok line, got: %s", buf.String())
	}
}

func TestRulesLintFailsOnFixtureMismatch(t *testing.T) {
	dir := t.TempDir()
	bad := strings.Replace(validRuleYAML, "action: file.read }\n", "action: tool.invoked }\n", 1)
	writeRuleFile(t, dir, "bad.rule.yaml", bad)
	cmd, buf := newCmd()
	if err := lintRulesPath(cmd, dir); err == nil {
		t.Fatalf("expected lint failure; output:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "FAIL") {
		t.Fatalf("expected FAIL line, got: %s", buf.String())
	}
}

func TestRulesLintEmptyDir(t *testing.T) {
	cmd, _ := newCmd()
	if err := lintRulesPath(cmd, t.TempDir()); err == nil {
		t.Fatalf("expected error for directory with no rule files")
	}
}

func makeTarGz(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractRuleTarballRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	data := makeTarGz(t, map[string]string{
		"ok.rule.yaml":                   "id: ok",
		"../../../../tmp/evil.rule.yaml": "id: evil",
	})
	err := extractRuleTarball(bytes.NewReader(data), dir)
	if err == nil || !strings.Contains(err.Error(), "path traversal") {
		t.Fatalf("expected path-traversal rejection, got: %v", err)
	}
	// The traversal target must NOT exist outside the dest dir.
	escaped := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(dir)))), "tmp", "evil.rule.yaml")
	if _, statErr := os.Stat(escaped); statErr == nil {
		t.Fatalf("traversal entry escaped the destination: %s", escaped)
	}
}

func TestExtractRuleTarballExtractsCleanEntries(t *testing.T) {
	dir := t.TempDir()
	// A top-level rule, a nested-dir rule, and a non-rule file.
	data := makeTarGz(t, map[string]string{
		"ok.rule.yaml":              "id: ok",
		"pack/sub/nested.rule.yaml": "id: nested",
		"notes.txt":                 "ignored",
	})
	if err := extractRuleTarball(bytes.NewReader(data), dir); err != nil {
		t.Fatalf("extract: %v", err)
	}
	// Rule entries keep their relative archive layout inside dir.
	for _, name := range []string{"ok.rule.yaml", filepath.Join("pack", "sub", "nested.rule.yaml")} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s inside dest dir: %v", name, err)
		}
	}
	// Non-rule files are ignored.
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err == nil {
		t.Errorf("notes.txt should not be extracted")
	}
}

// TestExtractRuleTarballKeepsSameBaseNameEntries guards against the silent
// overwrite where two archive paths sharing a base name flattened to the same
// destination, so a pack could install fewer rules than it contained.
func TestExtractRuleTarballKeepsSameBaseNameEntries(t *testing.T) {
	dir := t.TempDir()
	data := makeTarGz(t, map[string]string{
		"network/exfil.rule.yaml": "id: network-exfil",
		"secrets/exfil.rule.yaml": "id: secrets-exfil",
	})
	if err := extractRuleTarball(bytes.NewReader(data), dir); err != nil {
		t.Fatalf("extract: %v", err)
	}
	// Both same-base-name entries must survive at their distinct relative paths.
	cases := map[string]string{
		filepath.Join("network", "exfil.rule.yaml"): "id: network-exfil",
		filepath.Join("secrets", "exfil.rule.yaml"): "id: secrets-exfil",
	}
	for rel, want := range cases {
		got, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatalf("expected %s to be extracted: %v", rel, err)
		}
		if string(got) != want {
			t.Errorf("%s: got %q, want %q (entries overwrote each other)", rel, got, want)
		}
	}
}

func TestRulesPullAcceptsURLWithQuery(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prev := rulesOpts
	rulesOpts.userMode, rulesOpts.systemMode, rulesOpts.force = true, false, false
	t.Cleanup(func() { rulesOpts = prev })

	pack := makeTarGz(t, map[string]string{"cli-test-rule.rule.yaml": validRuleYAML})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(pack)
	}))
	t.Cleanup(srv.Close)

	// The path is a valid pack; the query string must not defeat suffix detection.
	cmd, buf := newCmd()
	if err := runRulesPull(cmd, []string{srv.URL + "/pack.tar.gz?version=1&token=abc"}); err != nil {
		t.Fatalf("pull with query string failed: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "cli-test-rule") {
		t.Fatalf("expected the pack rule to be installed, got: %s", buf.String())
	}
}

func TestRulesPullRejectsUnsupportedPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prev := rulesOpts
	rulesOpts.userMode, rulesOpts.systemMode = true, false
	t.Cleanup(func() { rulesOpts = prev })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("nope"))
	}))
	t.Cleanup(srv.Close)

	cmd, _ := newCmd()
	// A non-pack path must still be rejected even with a pack-like query string.
	if err := runRulesPull(cmd, []string{srv.URL + "/index.html?file=pack.tar.gz"}); err == nil {
		t.Fatal("expected unsupported-url rejection for a non-pack path")
	}
}

func TestLoadRuleFilesSingleFile(t *testing.T) {
	dir := t.TempDir()
	p := writeRuleFile(t, dir, "ok.rule.yaml", validRuleYAML)
	rules, err := loadRuleFiles(p)
	if err != nil {
		t.Fatalf("loadRuleFiles single: %v", err)
	}
	if len(rules) != 1 || rules[0].ID != "cli-test-rule" {
		t.Fatalf("unexpected: %+v", rules)
	}
}
