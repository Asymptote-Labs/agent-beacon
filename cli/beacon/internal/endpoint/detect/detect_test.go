package detect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
)

func TestBaselineValid(t *testing.T) {
	rules, err := Baseline()
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if len(rules) == 0 {
		t.Fatal("embedded baseline is empty")
	}
	// Every embedded rule must pass full conformance (validate + maturity + fixtures).
	for _, r := range rules {
		if _, err := threatrules.CheckRule(r); err != nil {
			t.Errorf("baseline rule %q fails conformance: %v", r.ID, err)
		}
	}
}

func TestBaselineRulePathUsesEmbedFSSeparator(t *testing.T) {
	got := baselineRulePath("example.rule.yaml")
	if got != "baseline/example.rule.yaml" {
		t.Fatalf("embedded FS paths must use forward slashes, got %q", got)
	}
}

const aRule = `
id: %ID%
version: 1
title: T
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

func ruleWithID(id string) string { return strings.ReplaceAll(aRule, "%ID%", id) }

func TestLoadActiveFallsBackToBaseline(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // empty store -> baseline
	loaded, err := LoadActive(true, "")
	if err != nil {
		t.Fatalf("load active: %v", err)
	}
	if len(loaded) == 0 || loaded[0].Source != SourceBaseline {
		t.Fatalf("expected baseline fallback, got %d rules src=%v", len(loaded), srcOf(loaded))
	}
}

func TestInstallThenLoadActiveUsesStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	src := filepath.Join(t.TempDir(), "my.rule.yaml")
	if err := os.WriteFile(src, []byte(ruleWithID("custom-rule")), 0o644); err != nil {
		t.Fatal(err)
	}
	installed, err := InstallFiles(true, src, false)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if len(installed) != 1 || installed[0].ID != "custom-rule" {
		t.Fatalf("unexpected install result: %+v", installed)
	}

	loaded, err := LoadActive(true, "")
	if err != nil {
		t.Fatalf("load active: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Source != SourceStore || loaded[0].Rule.ID != "custom-rule" {
		t.Fatalf("expected store rule, got %+v", loaded)
	}
}

func TestInstallRejectsInvalidRule(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	src := filepath.Join(t.TempDir(), "bad.rule.yaml")
	// valid YAML, invalid CEL field -> CheckRule fails
	bad := strings.Replace(ruleWithID("bad-rule"), `e.event.action == "file.read"`, `e.nope.field == 1`, 1)
	if err := os.WriteFile(src, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallFiles(true, src, false); err == nil {
		t.Fatal("expected install to reject an invalid rule")
	}
	// Nothing should have been written.
	if hasRuleFiles(StoreDir(true)) {
		t.Fatal("invalid rule must not be written to the store")
	}
}

func TestInstallRejectsFailingFixture(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	src := filepath.Join(t.TempDir(), "mismatch.rule.yaml")
	// Valid, compilable rule whose embedded fixture asserts the wrong verdict:
	// the match condition only fires on file.read, but the fixture feeds a
	// file.write event while expecting a match. CheckRule returns no error here,
	// only a failing FixtureResult.
	bad := strings.Replace(ruleWithID("mismatch-rule"), "action: file.read }", "action: file.write }", 1)
	if err := os.WriteFile(src, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallFiles(true, src, false); err == nil {
		t.Fatal("expected install to reject a rule with a failing fixture")
	}
	if hasRuleFiles(StoreDir(true)) {
		t.Fatal("rule with a failing fixture must not be written to the store")
	}
}

func TestInstallRejectsDuplicateWithoutForce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	src := filepath.Join(t.TempDir(), "dup.rule.yaml")
	if err := os.WriteFile(src, []byte(ruleWithID("dup-rule")), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallFiles(true, src, false); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if _, err := InstallFiles(true, src, false); err == nil {
		t.Fatal("expected duplicate-id rejection without --force")
	}
	if _, err := InstallFiles(true, src, true); err != nil {
		t.Fatalf("force re-install should succeed: %v", err)
	}
}

func TestInstallRollsBackWhenLaterWriteFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "a.rule.yaml"), []byte(ruleWithID("a-rule")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "b.rule.yaml"), []byte(ruleWithID("b-rule")), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := EnsureStore(true)
	if err != nil {
		t.Fatal(err)
	}
	blockedDest := filepath.Join(store, "b-rule"+ruleFileSuffix)
	if err := os.Mkdir(blockedDest, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := InstallFiles(true, srcDir, false); err == nil {
		t.Fatal("expected install to fail on blocked destination")
	}
	if _, err := os.Stat(filepath.Join(store, "a-rule"+ruleFileSuffix)); !os.IsNotExist(err) {
		t.Fatalf("first rule should have been rolled back, stat err=%v", err)
	}
	if info, err := os.Stat(blockedDest); err != nil || !info.IsDir() {
		t.Fatalf("blocked destination directory should remain, info=%v err=%v", info, err)
	}
}

func TestForceInstallRestoresOverwrittenRuleWhenLaterWriteFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	initial := strings.Replace(ruleWithID("a-rule"), "title: T", "title: Old", 1)
	initialSrc := filepath.Join(t.TempDir(), "a.rule.yaml")
	if err := os.WriteFile(initialSrc, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallFiles(true, initialSrc, false); err != nil {
		t.Fatalf("initial install: %v", err)
	}

	srcDir := t.TempDir()
	updated := strings.Replace(ruleWithID("a-rule"), "title: T", "title: New", 1)
	if err := os.WriteFile(filepath.Join(srcDir, "a.rule.yaml"), []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "b.rule.yaml"), []byte(ruleWithID("b-rule")), 0o644); err != nil {
		t.Fatal(err)
	}

	store := StoreDir(true)
	blockedDest := filepath.Join(store, "b-rule"+ruleFileSuffix)
	if err := os.Mkdir(blockedDest, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := InstallFiles(true, srcDir, true); err == nil {
		t.Fatal("expected force install to fail on blocked destination")
	}
	got, err := os.ReadFile(filepath.Join(store, "a-rule"+ruleFileSuffix))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != initial {
		t.Fatalf("overwritten rule was not restored:\n%s", got)
	}
}

func TestRemove(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	src := filepath.Join(t.TempDir(), "r.rule.yaml")
	if err := os.WriteFile(src, []byte(ruleWithID("removable")), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallFiles(true, src, false); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := Remove(true, "removable"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := Remove(true, "removable"); err == nil {
		t.Fatal("removing a missing rule should error")
	}
}

func TestRemoveRejectsPathTraversal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// A rule file outside the store that a traversal id could resolve to.
	victimDir := t.TempDir()
	victim := filepath.Join(victimDir, "victim.rule.yaml")
	if err := os.WriteFile(victim, []byte("id: victim\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// id crafted so id+suffix == "<victimDir>/victim.rule.yaml" after Join+Clean.
	rel, err := filepath.Rel(StoreDir(true), filepath.Join(victimDir, "victim"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{rel, "../../etc/passwd", "a/b", "."} {
		if _, err := Remove(true, id); err == nil {
			t.Fatalf("expected Remove to reject unsafe id %q", id)
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("victim file must not be deleted: %v", err)
	}
}

func srcOf(l []LoadedRule) []Source {
	s := make([]Source, len(l))
	for i := range l {
		s[i] = l[i].Source
	}
	return s
}
