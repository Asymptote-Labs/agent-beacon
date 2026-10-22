package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/detect"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
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

// newerSpecRuleYAML declares a spec newer than this binary supports.
var newerSpecRuleYAML = strings.Replace(strings.Replace(validRuleYAML, "id: cli-test-rule\n", "id: cli-future-rule\nspec: threat-rules/v1.9\n", 1),
	"emit:", "future_field: true\nemit:", 1)

const upgradeMessage = `rule "cli-future-rule" requires threat-rules/v1.9, but this Beacon supports up to threat-rules/v1.1; upgrade Beacon to load it`

func TestRulesLintReportsNewerSpecAsSkip(t *testing.T) {
	dir := t.TempDir()
	writeRuleFile(t, dir, "ok.rule.yaml", validRuleYAML)
	writeRuleFile(t, dir, "future.rule.yaml", newerSpecRuleYAML)
	cmd, buf := newCmd()
	err := lintRulesPath(cmd, dir)
	if err == nil || !strings.Contains(err.Error(), "need a newer Beacon") {
		t.Fatalf("want a newer-Beacon lint error, got %v\n%s", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "ok   cli-test-rule") {
		t.Errorf("the rest of the pack should still be linted:\n%s", out)
	}
	if !strings.Contains(out, "SKIP ") || !strings.Contains(out, "future.rule.yaml: "+upgradeMessage) {
		t.Errorf("want a SKIP line with the upgrade message:\n%s", out)
	}
}

func TestRulesAddSkipsNewerSpec(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	src := t.TempDir()
	writeRuleFile(t, src, "ok.rule.yaml", validRuleYAML)
	writeRuleFile(t, src, "future.rule.yaml", newerSpecRuleYAML)
	cmd, buf := newCmd()
	rulesOpts.userMode, rulesOpts.systemMode, rulesOpts.force = true, false, false
	if err := rulesAddCmd.RunE(cmd, []string{src}); err != nil {
		t.Fatalf("rules add: %v\n%s", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "installed cli-test-rule") || !strings.Contains(out, "1 rule(s) installed.") {
		t.Errorf("want the current rule installed:\n%s", out)
	}
	if !strings.Contains(out, "warning: skipped ") || !strings.Contains(out, upgradeMessage) {
		t.Errorf("want a skip warning with the upgrade message:\n%s", out)
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

func TestExtractRuleTarballIgnoresAppleDoubleEntries(t *testing.T) {
	dir := t.TempDir()
	data := makeTarGz(t, map[string]string{
		"rules/approval-abuse/approval-denied-command-executed-anyway.rule.yaml":   "id: ok",
		"rules/approval-abuse/._approval-denied-command-executed-anyway.rule.yaml": "\x00\x05metadata",
		"__MACOSX/rules/._approval-denied-command-executed-anyway.rule.yaml":       "\x00\x05metadata",
	})
	if err := extractRuleTarball(bytes.NewReader(data), dir); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "rules", "approval-abuse", "approval-denied-command-executed-anyway.rule.yaml")); err != nil {
		t.Fatalf("expected real rule to be extracted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "rules", "approval-abuse", "._approval-denied-command-executed-anyway.rule.yaml")); !os.IsNotExist(err) {
		t.Fatalf("AppleDouble entry should not be extracted, stat err=%v", err)
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
	testenv.SetHome(t, t.TempDir())
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
	testenv.SetHome(t, t.TempDir())
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

// pullTestSetup isolates the store and resets the pull options and limits for a test.
func pullTestSetup(t *testing.T) {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	prevOpts := rulesOpts
	prevTransport := rulesPullTransport
	prevDownload, prevEntries, prevTar, prevEntry := rulesPullMaxDownloadBytes, rulesPullMaxTarEntries, rulesPullMaxTarBytes, rulesPullMaxEntryBytes
	rulesOpts.userMode, rulesOpts.systemMode, rulesOpts.force, rulesOpts.sha256 = true, false, false, ""
	t.Cleanup(func() {
		rulesOpts = prevOpts
		rulesPullTransport = prevTransport
		rulesPullMaxDownloadBytes, rulesPullMaxTarEntries, rulesPullMaxTarBytes, rulesPullMaxEntryBytes = prevDownload, prevEntries, prevTar, prevEntry
	})
}

// storeIsEmpty reports whether nothing was installed into the user-mode rule store.
func storeIsEmpty(t *testing.T) bool {
	t.Helper()
	entries, err := os.ReadDir(detect.StoreDir(true))
	if os.IsNotExist(err) {
		return true
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".rule.yaml") {
			return false
		}
	}
	return true
}

func TestRulesPullRefusesNonLoopbackHTTP(t *testing.T) {
	pullTestSetup(t)
	for _, u := range []string{
		"http://example.com/pack.tar.gz",
		"http://10.0.0.1/x.rule.yaml",
		"ftp://example.com/pack.tar.gz",
		"file:///tmp/pack.tar.gz",
	} {
		cmd, _ := newCmd()
		err := runRulesPull(cmd, []string{u})
		if err == nil || !strings.Contains(err.Error(), "https://") {
			t.Errorf("%s: want an https-required error, got %v", u, err)
		}
	}
	for _, u := range []string{"http://127.0.0.1:1/x", "http://localhost/x", "http://[::1]/x", "https://example.com/x"} {
		parsed, _ := neturl.Parse(u)
		if err := checkRulesPullURL(parsed); err != nil {
			t.Errorf("%s should be accepted: %v", u, err)
		}
	}
}

func TestRulesPullOverHTTPS(t *testing.T) {
	pullTestSetup(t)
	pack := makeTarGz(t, map[string]string{"cli-test-rule.rule.yaml": validRuleYAML})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(pack) }))
	t.Cleanup(srv.Close)
	rulesPullTransport = srv.Client().Transport

	sum := sha256.Sum256(pack)
	rulesOpts.sha256 = "SHA256:" + strings.ToUpper(hex.EncodeToString(sum[:]))
	cmd, buf := newCmd()
	if err := runRulesPull(cmd, []string{srv.URL + "/pack.tar.gz"}); err != nil {
		t.Fatalf("https pull: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "Verified sha256") || !strings.Contains(buf.String(), "installed cli-test-rule") {
		t.Fatalf("want a verified install, got:\n%s", buf.String())
	}
}

func TestRulesPullRefusesHTTPSToHTTPRedirect(t *testing.T) {
	pullTestSetup(t)
	pack := makeTarGz(t, map[string]string{"cli-test-rule.rule.yaml": validRuleYAML})
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(pack) }))
	t.Cleanup(plain.Close)
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/pack.tar.gz", http.StatusFound)
	}))
	t.Cleanup(tlsSrv.Close)
	rulesPullTransport = tlsSrv.Client().Transport

	cmd, buf := newCmd()
	err := runRulesPull(cmd, []string{tlsSrv.URL + "/pack.tar.gz"})
	if err == nil || !strings.Contains(err.Error(), "refusing redirect from https") {
		t.Fatalf("want the downgrade refused, got %v\n%s", err, buf.String())
	}
	if !storeIsEmpty(t) {
		t.Fatal("nothing may be installed after a refused redirect")
	}
}

func TestRulesPullRedirectChecks(t *testing.T) {
	req := &http.Request{URL: &neturl.URL{Scheme: "http", Host: "example.com", Path: "/x.tar.gz"}}
	via := []*http.Request{{URL: &neturl.URL{Scheme: "http", Host: "127.0.0.1", Path: "/x.tar.gz"}}}
	if err := rulesPullCheckRedirect(req, via); err == nil {
		t.Fatal("a redirect from loopback http to a remote http host must be refused")
	}
	same := &http.Request{URL: &neturl.URL{Scheme: "https", Host: "objects.example.com", Path: "/x.tar.gz"}}
	viaTLS := []*http.Request{{URL: &neturl.URL{Scheme: "https", Host: "github.com", Path: "/x.tar.gz"}}}
	if err := rulesPullCheckRedirect(same, viaTLS); err != nil {
		t.Fatalf("an https to https redirect must be followed: %v", err)
	}
}

func TestRulesPullSHA256MismatchInstallsNothing(t *testing.T) {
	pullTestSetup(t)
	pack := makeTarGz(t, map[string]string{"cli-test-rule.rule.yaml": validRuleYAML})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(pack) }))
	t.Cleanup(srv.Close)

	rulesOpts.sha256 = strings.Repeat("0", 64)
	cmd, buf := newCmd()
	err := runRulesPull(cmd, []string{srv.URL + "/pack.tar.gz"})
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") || !strings.Contains(err.Error(), "nothing was installed") {
		t.Fatalf("want a sha256 mismatch, got %v\n%s", err, buf.String())
	}
	if !storeIsEmpty(t) {
		t.Fatal("a checksum mismatch must install nothing")
	}

	rulesOpts.sha256 = "not-hex"
	if err := runRulesPull(cmd, []string{srv.URL + "/pack.tar.gz"}); err == nil || !strings.Contains(err.Error(), "--sha256") {
		t.Fatalf("want a malformed --sha256 refused, got %v", err)
	}
}

func TestRulesPullRefusesOversizedDownload(t *testing.T) {
	pullTestSetup(t)
	rulesPullMaxDownloadBytes = 1024
	big := strings.Repeat("#", 4096)
	for name, chunked := range map[string]bool{"content-length": false, "chunked": true} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if chunked {
				w.(http.Flusher).Flush() // no Content-Length: the body cap must catch it
			}
			w.Write([]byte(big))
		}))
		cmd, _ := newCmd()
		err := runRulesPull(cmd, []string{srv.URL + "/x.rule.yaml"})
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), "limit for a rule pack") {
			t.Errorf("%s: want the download limit refused, got %v", name, err)
		}
	}
	if !storeIsEmpty(t) {
		t.Fatal("an oversized download must install nothing")
	}
}

func TestExtractRuleTarballTooManyEntries(t *testing.T) {
	pullTestSetup(t)
	rulesPullMaxTarEntries = 5
	entries := map[string]string{}
	for i := 0; i < 6; i++ {
		entries[fmt.Sprintf("notes-%d.txt", i)] = "x"
	}
	err := extractRuleTarball(bytes.NewReader(makeTarGz(t, entries)), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "more than 5 entries") {
		t.Fatalf("want the entry cap enforced, got %v", err)
	}
}

func TestExtractRuleTarballTooManyBytes(t *testing.T) {
	pullTestSetup(t)
	rulesPullMaxTarBytes = 64 << 10
	// A highly compressible non-rule entry, as in a gzip bomb: small on the wire, large
	// once decompressed.
	data := makeTarGz(t, map[string]string{
		"a.rule.yaml": "id: a",
		"bomb.bin":    strings.Repeat("\x00", 1<<20),
	})
	if int64(len(data)) >= rulesPullMaxTarBytes {
		t.Fatalf("fixture should compress below the limit, got %d bytes", len(data))
	}
	err := extractRuleTarball(bytes.NewReader(data), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "uncompressed limit") {
		t.Fatalf("want the decompressed-size cap enforced, got %v", err)
	}
}

func TestExtractRuleTarballOversizedEntry(t *testing.T) {
	pullTestSetup(t)
	rulesPullMaxEntryBytes = 16
	err := extractRuleTarball(bytes.NewReader(makeTarGz(t, map[string]string{"big.rule.yaml": strings.Repeat("a", 64)})), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "limit for one rule") {
		t.Fatalf("want an oversized rule entry refused rather than truncated, got %v", err)
	}
}

func TestBoundedReaderAllowsExactLimit(t *testing.T) {
	r := &boundedReader{r: strings.NewReader("abcd"), n: 4}
	got, err := io.ReadAll(r)
	if err != nil || string(got) != "abcd" {
		t.Fatalf("exact-limit read: %q, %v", got, err)
	}
	r = &boundedReader{r: strings.NewReader("abcde"), n: 4}
	if _, err := io.ReadAll(r); !errors.Is(err, errTarStreamTooLarge) {
		t.Fatalf("over-limit read: want errTarStreamTooLarge, got %v", err)
	}
}

func TestRulesListAndRemoveShowBaseline(t *testing.T) {
	pullTestSetup(t)
	src := t.TempDir()
	writeRuleFile(t, src, "ok.rule.yaml", validRuleYAML)
	cmd, buf := newCmd()
	if err := rulesAddCmd.RunE(cmd, []string{src}); err != nil {
		t.Fatalf("rules add: %v", err)
	}
	buf.Reset()
	if err := rulesListCmd.RunE(cmd, nil); err != nil {
		t.Fatalf("rules list: %v", err)
	}
	base, err := detect.Baseline()
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%d rule(s): 1 from the store, %d from the built-in baseline.", len(base)+1, len(base))
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("want %q in:\n%s", want, buf.String())
	}

	// Installing and then removing a store copy of a baseline rule says the baseline
	// copy is active again.
	id := base[0].ID
	override := writeRuleFile(t, t.TempDir(), "override.rule.yaml", strings.Replace(validRuleYAML, "id: cli-test-rule", "id: "+id, 1))
	if err := rulesAddCmd.RunE(cmd, []string{override}); err != nil {
		t.Fatalf("rules add override: %v", err)
	}
	buf.Reset()
	if err := rulesRemoveCmd.RunE(cmd, []string{id}); err != nil {
		t.Fatalf("rules remove: %v", err)
	}
	if !strings.Contains(buf.String(), "baseline copy is active again") {
		t.Fatalf("want the baseline note, got:\n%s", buf.String())
	}
}
