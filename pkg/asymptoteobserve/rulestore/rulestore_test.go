package rulestore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
)

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
	store := t.TempDir() // empty store -> baseline
	loaded, err := LoadActive(store, "")
	if err != nil {
		t.Fatalf("load active: %v", err)
	}
	if len(loaded) == 0 || loaded[0].Source != SourceBaseline {
		t.Fatalf("expected baseline fallback, got %d rules src=%v", len(loaded), srcOf(loaded))
	}
}

func TestLoadActiveRulesDirOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.rule.yaml"), []byte(ruleWithID("override-rule")), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadActive(t.TempDir(), dir)
	if err != nil {
		t.Fatalf("load active: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Source != SourceStore || loaded[0].Rule.ID != "override-rule" {
		t.Fatalf("expected explicit rulesDir override, got %+v", loaded)
	}
}

// TestLoadActiveKeepsBaselineAlongsideStore guards against one installed rule silently
// switching off the whole embedded baseline: every baseline rule the store does not
// define stays active.
func TestLoadActiveKeepsBaselineAlongsideStore(t *testing.T) {
	base, err := Baseline()
	if err != nil {
		t.Fatal(err)
	}
	store := t.TempDir()
	// Placed through InstallFiles so this also proves an installed rule is what LoadActive reads.
	src := filepath.Join(t.TempDir(), "my.rule.yaml")
	if err := os.WriteFile(src, []byte(ruleWithID("custom-rule")), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallFiles(store, src, false); err != nil {
		t.Fatalf("install: %v", err)
	}
	loaded, err := LoadActive(store, "")
	if err != nil {
		t.Fatalf("load active: %v", err)
	}
	if len(loaded) != len(base)+1 {
		t.Fatalf("want %d rules (store + full baseline), got %d", len(base)+1, len(loaded))
	}
	baseIDs := map[string]bool{}
	for _, r := range bySource(loaded, SourceBaseline) {
		baseIDs[r.ID] = true
	}
	for _, r := range base {
		if !baseIDs[r.ID] {
			t.Errorf("baseline rule %q is missing from the active set", r.ID)
		}
	}
}

// TestLoadActiveStoreOverridesBaselineByID: a store rule with a baseline rule's id
// replaces that one baseline rule (store wins), and only that one.
func TestLoadActiveStoreOverridesBaselineByID(t *testing.T) {
	base, err := Baseline()
	if err != nil {
		t.Fatal(err)
	}
	id := base[0].ID
	if !IsBaselineID(id) || IsBaselineID("custom-rule") {
		t.Fatalf("IsBaselineID disagrees with Baseline()")
	}
	store := t.TempDir()
	if err := os.WriteFile(filepath.Join(store, id+RuleFileSuffix), []byte(ruleWithID(id)), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadActive(store, "")
	if err != nil {
		t.Fatalf("load active: %v", err)
	}
	if len(loaded) != len(base) {
		t.Fatalf("want %d rules, got %d", len(base), len(loaded))
	}
	seen := 0
	for _, lr := range loaded {
		if lr.Rule.ID != id {
			continue
		}
		seen++
		if lr.Source != SourceStore || lr.Rule.Title != "T" {
			t.Errorf("rule %q should come from the store, got source %s title %q", id, lr.Source, lr.Rule.Title)
		}
	}
	if seen != 1 {
		t.Fatalf("rule %q active %d times, want once", id, seen)
	}
}

func bySource(l []LoadedRule, src Source) []*threatrules.Rule {
	var out []*threatrules.Rule
	for _, lr := range l {
		if lr.Source == src {
			out = append(out, lr.Rule)
		}
	}
	return out
}

func TestInstallFilesIgnoresAppleDoubleRuleFiles(t *testing.T) {
	store := t.TempDir()
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "ok.rule.yaml"), []byte(ruleWithID("custom-rule")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "._ok.rule.yaml"), []byte("\x00\x05metadata"), 0o644); err != nil {
		t.Fatal(err)
	}
	installed, err := InstallFiles(store, srcDir, false)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if len(installed) != 1 || installed[0].ID != "custom-rule" {
		t.Fatalf("unexpected install result: %+v", installed)
	}
}

func TestInstallRejectsInvalidRule(t *testing.T) {
	store := t.TempDir()
	src := filepath.Join(t.TempDir(), "bad.rule.yaml")
	// valid YAML, invalid CEL field -> CheckRule fails
	bad := strings.Replace(ruleWithID("bad-rule"), `e.event.action == "file.read"`, `e.nope.field == 1`, 1)
	if err := os.WriteFile(src, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallFiles(store, src, false); err == nil {
		t.Fatal("expected install to reject an invalid rule")
	}
	// Nothing should have been written.
	if HasRuleFiles(store) {
		t.Fatal("invalid rule must not be written to the store")
	}
}

func TestInstallRejectsFailingFixture(t *testing.T) {
	store := t.TempDir()
	src := filepath.Join(t.TempDir(), "mismatch.rule.yaml")
	// Valid, compilable rule whose embedded fixture asserts the wrong verdict:
	// the match condition only fires on file.read, but the fixture feeds a
	// file.write event while expecting a match. CheckRule returns no error here,
	// only a failing FixtureResult.
	bad := strings.Replace(ruleWithID("mismatch-rule"), "action: file.read }", "action: file.write }", 1)
	if err := os.WriteFile(src, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallFiles(store, src, false); err == nil {
		t.Fatal("expected install to reject a rule with a failing fixture")
	}
	if HasRuleFiles(store) {
		t.Fatal("rule with a failing fixture must not be written to the store")
	}
}

func TestInstallRejectsDuplicateWithoutForce(t *testing.T) {
	store := t.TempDir()
	src := filepath.Join(t.TempDir(), "dup.rule.yaml")
	if err := os.WriteFile(src, []byte(ruleWithID("dup-rule")), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallFiles(store, src, false); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if _, err := InstallFiles(store, src, false); err == nil {
		t.Fatal("expected duplicate-id rejection without --force")
	}
	if _, err := InstallFiles(store, src, true); err != nil {
		t.Fatalf("force re-install should succeed: %v", err)
	}
}

func TestInstallRollsBackWhenLaterWriteFails(t *testing.T) {
	store := t.TempDir()
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "a.rule.yaml"), []byte(ruleWithID("a-rule")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "b.rule.yaml"), []byte(ruleWithID("b-rule")), 0o644); err != nil {
		t.Fatal(err)
	}

	blockedDest := filepath.Join(store, "b-rule"+RuleFileSuffix)
	if err := os.Mkdir(blockedDest, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := InstallFiles(store, srcDir, false); err == nil {
		t.Fatal("expected install to fail on blocked destination")
	}
	if _, err := os.Stat(filepath.Join(store, "a-rule"+RuleFileSuffix)); !os.IsNotExist(err) {
		t.Fatalf("first rule should have been rolled back, stat err=%v", err)
	}
	if info, err := os.Stat(blockedDest); err != nil || !info.IsDir() {
		t.Fatalf("blocked destination directory should remain, info=%v err=%v", info, err)
	}
}

func TestForceInstallRestoresOverwrittenRuleWhenLaterWriteFails(t *testing.T) {
	store := t.TempDir()
	initial := strings.Replace(ruleWithID("a-rule"), "title: T", "title: Old", 1)
	initialSrc := filepath.Join(t.TempDir(), "a.rule.yaml")
	if err := os.WriteFile(initialSrc, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallFiles(store, initialSrc, false); err != nil {
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

	blockedDest := filepath.Join(store, "b-rule"+RuleFileSuffix)
	if err := os.Mkdir(blockedDest, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := InstallFiles(store, srcDir, true); err == nil {
		t.Fatal("expected force install to fail on blocked destination")
	}
	got, err := os.ReadFile(filepath.Join(store, "a-rule"+RuleFileSuffix))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != initial {
		t.Fatalf("overwritten rule was not restored:\n%s", got)
	}
}

func TestRemove(t *testing.T) {
	store := t.TempDir()
	src := filepath.Join(t.TempDir(), "r.rule.yaml")
	if err := os.WriteFile(src, []byte(ruleWithID("removable")), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallFiles(store, src, false); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := Remove(store, "removable"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := Remove(store, "removable"); err == nil {
		t.Fatal("removing a missing rule should error")
	}
}

func TestRemoveRejectsPathTraversal(t *testing.T) {
	store := t.TempDir()

	// A rule file outside the store that a traversal id could resolve to.
	victimDir := t.TempDir()
	victim := filepath.Join(victimDir, "victim.rule.yaml")
	if err := os.WriteFile(victim, []byte("id: victim\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// id crafted so id+suffix == "<victimDir>/victim.rule.yaml" after Join+Clean.
	rel, err := filepath.Rel(store, filepath.Join(victimDir, "victim"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{rel, "../../etc/passwd", "a/b", "."} {
		if _, err := Remove(store, id); err == nil {
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

// futureRule declares a spec newer than this engine supports and carries a field that
// version might add, which a strict decode alone would report as unknown.
const futureRule = `
id: future-rule
spec: threat-rules/v1.9
version: 1
title: T
severity: low
status: experimental
posture: detect
match: 'e.event.action == "file.read"'
future_field: true
emit:
  reason: ok
tests:
  - name: p
    verdict: match
    events:
      - event: { action: file.read }
`

func TestInstallFilesSkippingLeavesNewerSpecOut(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.rule.yaml"), []byte(ruleWithID("current-rule")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "b.rule.yaml"), []byte(futureRule), 0o644); err != nil {
		t.Fatal(err)
	}
	store := t.TempDir()

	if _, err := InstallFiles(store, src, false); err == nil || !strings.Contains(err.Error(), "upgrade Beacon") {
		t.Fatalf("strict InstallFiles want the unsupported-spec error, got %v", err)
	}
	if HasRuleFiles(store) {
		t.Fatal("strict InstallFiles must not write anything when it fails")
	}

	installed, skipped, err := InstallFilesSkipping(store, src, false)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if len(installed) != 1 || installed[0].ID != "current-rule" {
		t.Fatalf("want current-rule installed, got %+v", installed)
	}
	if len(skipped) != 1 || skipped[0].Err.RuleID != "future-rule" {
		t.Fatalf("want future-rule skipped, got %+v", skipped)
	}
	if _, err := os.Stat(filepath.Join(store, "future-rule"+RuleFileSuffix)); !os.IsNotExist(err) {
		t.Fatalf("skipped rule must not land in the store (stat err %v)", err)
	}
}

func TestLoadActiveSkippingRunsTheRestOfTheStore(t *testing.T) {
	store := t.TempDir()
	if err := os.WriteFile(filepath.Join(store, "current-rule.rule.yaml"), []byte(ruleWithID("current-rule")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "future-rule.rule.yaml"), []byte(futureRule), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, skipped, err := LoadActiveSkipping(store, "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	fromStore := bySource(loaded, SourceStore)
	if len(fromStore) != 1 || fromStore[0].ID != "current-rule" {
		t.Fatalf("want only current-rule from the store, got %d store rules", len(fromStore))
	}
	if len(skipped) != 1 || skipped[0].Err.RuleID != "future-rule" {
		t.Fatalf("want future-rule skipped, got %+v", skipped)
	}
	if _, err := LoadActive(store, ""); err == nil {
		t.Fatal("strict LoadActive want an error for the newer-spec rule")
	}
}
