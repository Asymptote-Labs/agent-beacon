package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/check"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/credentials"
)

// doctor's whole value is that its "fix" line is the right one. A check that reports a real
// problem but points at the wrong remedy is worse than one that says nothing, because it sends
// someone off to change something that was never broken.

// The original version collapsed every resolution failure into "not configured" and printed the
// environment-variable fix. Someone whose vault helper is locked would have been told to export
// a key they were deliberately not keeping in their environment.
func TestFailingKeyCommandIsBlamedOnTheHelperNotTheEnvironment(t *testing.T) {
	got := credentialCheck(
		credentials.Resolved{Source: credentials.SourceNone},
		errors.New("api key command failed: exit status 7"),
		"op read op://vault/anthropic/key",
	)

	if got.Status != statusFail {
		t.Fatalf("status = %s, want fail", got.Status)
	}
	if strings.Contains(got.Detail, "not configured") {
		t.Errorf("an explicitly configured helper that failed is not 'unconfigured': %q", got.Detail)
	}
	if !strings.Contains(got.Detail, "exit status 7") {
		t.Errorf("detail should carry the resolver's diagnosis, got %q", got.Detail)
	}
	if !strings.Contains(got.Fix, "op read op://vault/anthropic/key") {
		t.Errorf("fix should name the failing helper, got %q", got.Fix)
	}
	if strings.Contains(got.Fix, "export ANTHROPIC_API_KEY") {
		t.Errorf("fix must not redirect to the environment variable, got %q", got.Fix)
	}
}

// With nothing supplied at all, listing every option is exactly right -- this is the most
// common first-run failure and the reader has no idea which paths exist.
func TestNoCredentialListsEveryOption(t *testing.T) {
	got := credentialCheck(
		credentials.Resolved{Source: credentials.SourceNone},
		errors.New("no Anthropic credential found; supply one of: ..."),
		"",
	)

	if got.Status != statusFail {
		t.Fatalf("status = %s, want fail", got.Status)
	}
	for _, want := range []string{"ANTHROPIC_API_KEY", "--modal-secret", "--api-key-command"} {
		if !strings.Contains(got.Fix, want) {
			t.Errorf("fix should mention %s, got %q", want, got.Fix)
		}
	}
}

// A provider secret is the most secure option, so it must not read as a failure. But the
// artifact leak check cannot run against a value this process never held, and silently
// downgrading that to "ok" would turn an unrunnable check into an apparent pass.
func TestProviderSecretWarnsWithoutFailing(t *testing.T) {
	got := credentialCheck(
		credentials.Resolved{Source: credentials.SourceProviderSecret, ProviderSecretName: "team-key"},
		nil, "",
	)

	if got.Status != statusWarn {
		t.Fatalf("status = %s, want warn: secure but not fully verifiable", got.Status)
	}
	if got.Fix == "" {
		t.Error("the warning should say how to make the leak check runnable")
	}
	if strings.Contains(got.Detail, "sk-ant") {
		t.Errorf("detail must never carry key material, got %q", got.Detail)
	}
}

// The ordinary case must be a clean ok, or the noise trains people to ignore doctor.
func TestUsableLocalCredentialIsOK(t *testing.T) {
	got := credentialCheck(
		credentials.Resolved{Source: credentials.SourceEnv, Value: "sk-ant-secret-value"},
		nil, "",
	)

	if got.Status != statusOK {
		t.Fatalf("status = %s, want ok", got.Status)
	}
	if got.Fix != "" {
		t.Errorf("a passing check should not print a fix, got %q", got.Fix)
	}
	if strings.Contains(got.Detail, "sk-ant-secret-value") {
		t.Errorf("doctor output is pasted into issues and logs; it leaked the key: %q", got.Detail)
	}
}

// An offline re-judge must never search artifacts for a key the run did not use. Finding nothing
// then reports *clean* while a real disclosure of the original key goes unreported. Cursor Bugbot
// flagged this as High after an earlier fix closed only the provider-secret case.
func TestOfflineVerifyRefusesToSearchForTheWrongKey(t *testing.T) {
	const captured = "sk-ant-the-key-the-run-actually-used"
	t.Setenv(credentials.EnvVar, "sk-ant-a-completely-different-key")

	got := offlineCredential(map[string]string{
		"credential_source":      string(credentials.SourceEnv),
		"credential_fingerprint": credentials.Fingerprint(captured),
	})

	if got.LeakCheckPossible() {
		t.Fatal("a key that does not match the run's fingerprint must not be searched for")
	}
	if got.Source != credentials.SourceMismatch {
		t.Errorf("source = %s, want mismatch", got.Source)
	}
	if !strings.Contains(got.WithheldReason(), "not the one this run used") {
		t.Errorf("the reason should say the key differs, got %q", got.WithheldReason())
	}
}

// The matching case must still search, or the leak check would never run at all.
func TestOfflineVerifySearchesWhenTheKeyMatches(t *testing.T) {
	const key = "sk-ant-same-key-as-capture"
	t.Setenv(credentials.EnvVar, key)

	got := offlineCredential(map[string]string{
		"credential_source":      string(credentials.SourceEnv),
		"credential_fingerprint": credentials.Fingerprint(key),
	})

	if !got.LeakCheckPossible() || got.Value != key {
		t.Fatalf("a matching fingerprint must enable the search, got source=%s", got.Source)
	}
}

// A provider-secret run has no local value at all, so it stays unverified regardless of what is
// in the environment.
func TestOfflineVerifyKeepsProviderSecretRunsUnverified(t *testing.T) {
	t.Setenv(credentials.EnvVar, "sk-ant-irrelevant-local-key")

	got := offlineCredential(map[string]string{
		"credential_source":      string(credentials.SourceProviderSecret),
		"credential_secret_name": "team-key",
	})

	if got.LeakCheckPossible() {
		t.Fatal("a provider-secret run must stay unverified")
	}
	if !strings.Contains(got.WithheldReason(), "team-key") {
		t.Errorf("the reason should name the secret, got %q", got.WithheldReason())
	}
}

// Runs captured before fingerprints existed cannot be checked either way, so the unknown must be
// treated as unverified rather than assumed to match.
func TestOfflineVerifyTreatsAMissingFingerprintAsUnverified(t *testing.T) {
	t.Setenv(credentials.EnvVar, "sk-ant-some-key")

	got := offlineCredential(map[string]string{"credential_source": string(credentials.SourceEnv)})

	if got.LeakCheckPossible() {
		t.Error("without a recorded fingerprint there is no evidence the keys match")
	}
}

// The fingerprint goes into meta.json on disk, so it must not be reversible to the key.
func TestFingerprintDoesNotRevealTheKey(t *testing.T) {
	const key = "sk-ant-super-secret-value-12345"
	fp := credentials.Fingerprint(key)

	if strings.Contains(fp, key) || strings.Contains(fp, "sk-ant") {
		t.Fatalf("fingerprint leaked key material: %q", fp)
	}
	if !strings.HasPrefix(fp, "sha256:") || len(fp) != len("sha256:")+12 {
		t.Errorf("unexpected fingerprint shape: %q", fp)
	}
	if credentials.Fingerprint(key) != fp {
		t.Error("fingerprint must be stable to be comparable across runs")
	}
	if credentials.Fingerprint(key+"x") == fp {
		t.Error("different keys must produce different fingerprints")
	}
	if credentials.Fingerprint("") != "" {
		t.Error("no value means no fingerprint")
	}
}

// summarize's return value becomes the process exit status, and scripts and agents branch on it.
// Returning success when every scenario was INCONCLUSIVE let "nothing was verified" read as
// "verification succeeded" -- the vacuous pass this tool exists to prevent. Cursor Bugbot flagged
// it as High.
func TestAllInconclusiveDoesNotExitSuccessfully(t *testing.T) {
	err := summarize([]check.Verdict{
		{Scenario: "s01", Outcome: check.Inconclusive},
		{Scenario: "s02", Outcome: check.Inconclusive},
	})
	if err == nil {
		t.Fatal("an all-inconclusive run must not exit 0")
	}
	if !strings.Contains(err.Error(), "nothing was verified") {
		t.Errorf("the error should say nothing was verified, got: %v", err)
	}
	// The remedy differs from a failure, so the wording must too.
	if !strings.Contains(err.Error(), "retry") {
		t.Errorf("the error should point at a retry, got: %v", err)
	}
}

// A mixed run where something genuinely passed still succeeds -- an inconclusive scenario
// alongside a real pass is a retry-worthy nuisance, not a failed verification.
func TestMixedPassAndInconclusiveStillSucceeds(t *testing.T) {
	err := summarize([]check.Verdict{
		{Scenario: "s01", Outcome: check.Pass},
		{Scenario: "s02", Outcome: check.Inconclusive},
	})
	if err != nil {
		t.Errorf("a run with a real pass should not fail on an inconclusive sibling: %v", err)
	}
}

// A failure still wins, and still reports as a failure rather than as "nothing verified".
func TestFailureTakesPrecedenceOverInconclusive(t *testing.T) {
	err := summarize([]check.Verdict{
		{Scenario: "s01", Outcome: check.Fail},
		{Scenario: "s02", Outcome: check.Inconclusive},
	})
	if err == nil {
		t.Fatal("a failure must not exit 0")
	}
	if !strings.Contains(err.Error(), "failed") {
		t.Errorf("a failure should be reported as such, got: %v", err)
	}
}

// An empty run is not a pass either.
func TestNoScenariosIsAnError(t *testing.T) {
	if err := summarize(nil); err == nil {
		t.Error("running nothing must not report success")
	}
}

// doctor must report the cause CollectorIsStale actually found. The reason string grew from one
// case to three (uncommitted changes, drift from a downloaded release, sources newer than a local
// build) while doctor still prefixed every result with the uncommitted wording -- doubling that
// case and mislabelling the other two. A check that exists to stop you verifying the wrong binary
// is useless if it names the wrong cause. Cursor Bugbot caught it.
func TestCollectorFreshnessReportsTheRealCause(t *testing.T) {
	cases := map[string]string{
		"uncommitted": "uncommitted changes under collector-builder/: M collector-builder/exporter/exp.go",
		"release drift": "binary was downloaded from release v1.0.6, but collector-builder/ has " +
			"changed since: collector-builder/exporter/exp.go",
		"newer sources": "collector-builder/ sources are newer than the built binary: " +
			"collector-builder/exporter/exp.go",
	}
	for name, reason := range cases {
		got := freshnessCheck(true, reason)

		if got.Status != statusWarn {
			t.Errorf("%s: status = %s, want warn", name, got.Status)
		}
		if got.Detail != reason {
			t.Errorf("%s: detail must be the reason verbatim, got %q", name, got.Detail)
		}
		if name != "uncommitted" && strings.HasPrefix(got.Detail, "uncommitted changes") {
			t.Errorf("%s: a %s cause must not be labelled uncommitted: %q", name, name, got.Detail)
		}
		if strings.Count(got.Detail, "uncommitted changes under collector-builder/") > 1 {
			t.Errorf("%s: the uncommitted wording is doubled: %q", name, got.Detail)
		}
		if !strings.Contains(got.Fix, "beacon-otelcol") {
			t.Errorf("%s: the fix must explain which binary to rebuild, got %q", name, got.Fix)
		}
	}
}

// A fresh collector must report ok with no fix, or the warning becomes noise.
func TestCollectorFreshnessOKWhenNotStale(t *testing.T) {
	got := freshnessCheck(false, "")

	if got.Status != statusOK {
		t.Fatalf("status = %s, want ok", got.Status)
	}
	if got.Fix != "" {
		t.Errorf("a passing check should print no fix, got %q", got.Fix)
	}
	// The old wording claimed only "no uncommitted exporter changes", which understated what the
	// check now verifies and would read as green while a downloaded binary was drifting.
	if strings.Contains(got.Detail, "uncommitted") {
		t.Errorf("the ok detail should describe what was verified, not just one signal: %q", got.Detail)
	}
}

// verify writes both artifacts or neither. Rewriting only the fingerprint left verdict.json
// holding the old outcome while fingerprint.json held the new one, so the two files on disk
// disagreed about the same run and anything reading the verdict trusted a stale judgment. Cursor
// Bugbot reported it, and it had already happened in this repo's own run directories.
//
// Driven through the real `verify` command rather than a helper, because the bug was in which
// files the command chose to write -- a property no unit test on the judging logic would see.
func TestVerifyKeepsVerdictAndFingerprintInAgreement(t *testing.T) {
	dir := t.TempDir()
	// A log whose only command event lets a min_count-2 expectation fail, then pass once relaxed.
	lines := []string{
		`{"timestamp":"2026-08-02T18:00:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"prompt.submitted","category":"prompt"},"severity":"info","endpoint":{"os":"linux","hostname":"s"},"harness":{"name":"claude_code"},"session":{"id":"s1"},"prompt":{"text":"MARKER"},"message":"m"}`,
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	meta := map[string]string{
		"scenario": "s01-hello", "canary": "MARKER",
		"sentinel_present": "true", "sentinel_detail": "MARKER",
		"host_state": "host state unchanged", "argv_check_ran": "true", "secret_in_argv": "false",
	}
	b, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	// Plant a stale PASS. The minimal log above cannot satisfy s01-hello, so a real re-judge
	// concludes FAIL -- which makes "refreshed" distinguishable from "left alone".
	stale := check.Verdict{Scenario: "s01-hello", Outcome: check.Pass, Reason: "stale"}
	if err := stale.WriteJSON(filepath.Join(dir, "verdict.json")); err != nil {
		t.Fatal(err)
	}

	// A failing scenario is expected and is not a test error: what is under test is which files
	// the command writes, not the verdict it reaches.
	_ = cmdVerify([]string{dir})

	readOutcome := func(name string) string {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		return fmt.Sprint(m["outcome"])
	}

	verdict, fingerprint := readOutcome("verdict.json"), readOutcome("fingerprint.json")
	if verdict != fingerprint {
		t.Errorf("verdict.json (%s) and fingerprint.json (%s) must agree after a re-judge",
			verdict, fingerprint)
	}
	if verdict == "PASS" {
		t.Errorf("the stale PASS verdict was not refreshed; verdict.json still says %s", verdict)
	}
}

// A mutated verify must leave both artifacts alone: the mutation deliberately damages the input,
// so persisting its verdict would overwrite the run's real record with a fabricated failure.
func TestMutatedVerifyDoesNotOverwriteTheRealRecord(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		`{"timestamp":"2026-08-02T18:00:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"prompt.submitted","category":"prompt"},"severity":"info","endpoint":{"os":"linux","hostname":"s"},"harness":{"name":"claude_code"},"session":{"id":"s1"},"prompt":{"text":"MARKER"},"message":"m"}`,
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	meta := map[string]string{
		"scenario": "s01-hello", "canary": "MARKER",
		"sentinel_present": "true", "sentinel_detail": "MARKER",
		"host_state": "host state unchanged", "argv_check_ran": "true", "secret_in_argv": "false",
	}
	b, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	keep := check.Verdict{Scenario: "s01-hello", Outcome: check.Pass, Reason: "the real result"}
	if err := keep.WriteJSON(filepath.Join(dir, "verdict.json")); err != nil {
		t.Fatal(err)
	}

	// The mutation is expected to make the run fail; that is the point of the self-test.
	_ = cmdVerify([]string{"--mutate", "corrupt-line", dir})

	raw, err := os.ReadFile(filepath.Join(dir, "verdict.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(m["outcome"]); got != "PASS" {
		t.Errorf("a self-test must not persist its fabricated verdict; verdict.json now says %s", got)
	}
}

// A mutation that changes nothing proves nothing. drop-* already refused a no-op, but
// corrupt-line and plant-secret did not: an empty log, or a line without the `"message":` anchor,
// produced an unchanged file, a passing check, and a self-test that had verified nothing. Cursor
// Bugbot spotted the inconsistency between the branches.
func TestMutationsRefuseToNoOp(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-plantme")

	// An empty log cannot be damaged by any mutation.
	empty := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(empty, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"corrupt-line", "plant-secret", "drop-commands"} {
		if _, err := applyMutation(empty, mode); err == nil {
			t.Errorf("%s on an empty log must error rather than report a passing self-test", mode)
		}
	}
}

// plant-secret must not depend on a line happening to contain `"message":`.
func TestPlantSecretWorksWithoutTheMessageAnchor(t *testing.T) {
	const key = "sk-ant-plantme"
	t.Setenv("ANTHROPIC_API_KEY", key)

	dir := t.TempDir()
	p := filepath.Join(dir, "runtime.jsonl")
	// A valid object with no "message" field at all.
	if err := os.WriteFile(p, []byte(`{"timestamp":"2026-08-02T18:00:00Z","vendor":"beacon"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	mutatedPath, err := applyMutation(p, "plant-secret")
	if err != nil {
		t.Fatalf("plant-secret should handle any object, got: %v", err)
	}
	body, err := os.ReadFile(mutatedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), key) {
		t.Fatalf("the credential was not planted:\n%s", body)
	}
	// It should still be parseable, so the planted secret trips the leak check rather than the
	// parse invariant -- otherwise the self-test would pass for the wrong reason.
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(body))), &m); err != nil {
		t.Errorf("planting should keep the line valid JSON: %v\n%s", err, body)
	}
}

// The common case must keep working, and keep producing valid JSON.
func TestPlantSecretWithTheMessageAnchorStaysValidJSON(t *testing.T) {
	const key = "sk-ant-plantme"
	t.Setenv("ANTHROPIC_API_KEY", key)

	p := filepath.Join(t.TempDir(), "runtime.jsonl")
	line := `{"timestamp":"2026-08-02T18:00:00Z","vendor":"beacon","message":"m"}`
	if err := os.WriteFile(p, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	mutatedPath, err := applyMutation(p, "plant-secret")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(mutatedPath)
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(body))), &m); err != nil {
		t.Fatalf("planting broke the JSON: %v\n%s", err, body)
	}
	if m["leaked"] != key {
		t.Errorf("expected the key under \"leaked\", got %v", m["leaked"])
	}
}

// Absent session metadata must not decode as a *known* failure. Defaulting Known to true while OK
// defaulted to false meant an artifact lacking both keys read as "the session definitely failed",
// short-circuiting sentinel-less scenarios to INCONCLUSIVE without evaluating a single expectation
// -- the opposite of the "keep the original meaning" the comment claimed. Cursor Bugbot caught it.
func TestOfflineSessionDecodingNeverInventsAFailure(t *testing.T) {
	cases := []struct {
		name              string
		meta              map[string]string
		wantKnown, wantOK bool
	}{
		{"both absent (pre-dates either field)", map[string]string{}, false, false},
		{"only session_ok=true (pre-dates session_known)",
			map[string]string{"session_ok": "true"}, true, true},
		{"only session_ok=false (ambiguous: failed or unreadable)",
			map[string]string{"session_ok": "false"}, false, false},
		{"explicit known failure",
			map[string]string{"session_known": "true", "session_ok": "false"}, true, false},
		{"explicit known success",
			map[string]string{"session_known": "true", "session_ok": "true"}, true, true},
		{"explicit unknown",
			map[string]string{"session_known": "false", "session_ok": "false"}, false, false},
	}
	for _, c := range cases {
		known, ok := decodeSession(c.meta)
		if known != c.wantKnown || ok != c.wantOK {
			t.Errorf("%s: got known=%v ok=%v, want known=%v ok=%v",
				c.name, known, ok, c.wantKnown, c.wantOK)
		}
		// The dangerous combination is "known failure" invented from nothing.
		if len(c.meta) == 0 && known && !ok {
			t.Errorf("%s: absent metadata must never read as a known failed session", c.name)
		}
	}
}

// The sentinel signal needs the same treatment as the session one, and for the same reason:
// sentinel_present predates sentinel_probed, and the older probe returned empty stdout on a failed
// guest exec, which was recorded as present=false. So a recorded false without sentinel_probed
// means either an idle agent or a broken probe, and must not decode as a confident "did nothing".
// Cursor Bugbot reported the session half; this is its sibling one field away.
func TestOfflineSentinelDecodingNeverInventsAnIdleAgent(t *testing.T) {
	cases := []struct {
		name                    string
		meta                    map[string]string
		wantProbed, wantPresent bool
	}{
		{"both absent", map[string]string{}, false, false},
		{"only present=true (pre-dates sentinel_probed)",
			map[string]string{"sentinel_present": "true"}, true, true},
		{"only present=false (ambiguous: idle or broken probe)",
			map[string]string{"sentinel_present": "false"}, false, false},
		{"explicit probed, absent",
			map[string]string{"sentinel_probed": "true", "sentinel_present": "false"}, true, false},
		{"explicit probed, present",
			map[string]string{"sentinel_probed": "true", "sentinel_present": "true"}, true, true},
		{"explicit unprobed",
			map[string]string{"sentinel_probed": "false", "sentinel_present": "false"}, false, false},
	}
	for _, c := range cases {
		probed, present := decodeSentinel(c.meta)
		if probed != c.wantProbed || present != c.wantPresent {
			t.Errorf("%s: got probed=%v present=%v, want probed=%v present=%v",
				c.name, probed, present, c.wantProbed, c.wantPresent)
		}
	}
	// The dangerous combination: a confident "the agent did nothing" invented from nothing.
	if probed, present := decodeSentinel(map[string]string{}); probed && !present {
		t.Error("absent metadata must never decode as a probed-and-absent sentinel")
	}
}
