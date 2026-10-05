package threatrules

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot locates the repository root by walking up from this test file until it finds
// the spec sentinel (spec/threat-rules/VERSION). This keeps tests hermetic without
// hardcoded absolute paths.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "spec", "threat-rules", "VERSION")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repo root (spec/threat-rules/VERSION sentinel)")
		}
		dir = parent
	}
}

func rulesDir(t *testing.T) string { return filepath.Join(repoRoot(t), "rules") }

func specDir(t *testing.T) string { return filepath.Join(repoRoot(t), "spec", "threat-rules") }

// TestPackConformance is the keystone: it loads the real rule pack and, for every rule,
// validates it, enforces its maturity gate, and runs every embedded fixture against the
// reference evaluator. Adding a rule file automatically extends coverage.
func TestPackConformance(t *testing.T) {
	dir := rulesDir(t)
	rules, err := LoadDir(dir) // also asserts no duplicate ids and that every rule validates
	if err != nil {
		t.Fatalf("load rule pack: %v", err)
	}
	if len(rules) == 0 {
		t.Fatal("rule pack is empty")
	}

	for _, rule := range rules {
		rule := rule
		t.Run(rule.ID, func(t *testing.T) {
			results, err := CheckRule(rule)
			if err != nil {
				t.Fatalf("rule-level failure: %v", err)
			}
			for _, res := range results {
				res := res
				t.Run(res.Fixture, func(t *testing.T) {
					if !res.OK() {
						t.Fatalf("%s", res.String())
					}
				})
			}
		})
	}
}

// TestPackSharedCredentialRead pins correlation read steps that are meant to reuse
// credential-file-read's definition of a secret. The format has no include mechanism, so
// the expression is copied; this fails when one copy changes without the other.
func TestPackSharedCredentialRead(t *testing.T) {
	rules, err := LoadDir(rulesDir(t))
	if err != nil {
		t.Fatalf("load rule pack: %v", err)
	}
	byID := make(map[string]*Rule, len(rules))
	for _, r := range rules {
		byID[r.ID] = r
	}
	base := byID["credential-file-read"]
	if base == nil {
		t.Fatal("credential-file-read missing from the pack")
	}
	for _, ref := range []struct{ rule, step string }{
		{"secret-read-then-destructive", "read_secret"},
	} {
		r := byID[ref.rule]
		if r == nil || r.Correlation == nil {
			t.Fatalf("%s missing or not a correlation rule", ref.rule)
		}
		var got string
		for _, s := range r.Correlation.Steps {
			if s.ID == ref.step {
				got = s.Match
			}
		}
		if normalizeCEL(got) != normalizeCEL(base.Match) {
			t.Errorf("%s step %s drifted from credential-file-read's match:\n  got:  %s\n  want: %s",
				ref.rule, ref.step, normalizeCEL(got), normalizeCEL(base.Match))
		}
	}
}

// normalizeCEL collapses whitespace so YAML indentation differences do not count.
func normalizeCEL(expr string) string { return strings.Join(strings.Fields(expr), " ") }

// TestPackCategoriesMatchDirectories pins taxonomy.beacon_category to the directory each rule
// lives in. The rule store keeps rules flat, so the category travels inside the rule: it is what
// groups findings by kind of risk (the Security Review lens, for one) once a rule is installed.
func TestPackCategoriesMatchDirectories(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(rulesDir(t), "*", "*.rule.yaml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("rule files: %v, %v", paths, err)
	}
	for _, path := range paths {
		rule, err := LoadRule(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		want := filepath.Base(filepath.Dir(path))
		if got := rule.Taxonomy["beacon_category"]; got != want {
			t.Errorf("%s: taxonomy.beacon_category = %q, want its directory %q", path, got, want)
		}
	}
}

// TestBaselineRulesAreCorpusCopies keeps the embedded baseline byte-identical to the rules it was
// taken from, so a fix to a corpus rule cannot leave a stale copy in every binary.
func TestBaselineRulesAreCorpusCopies(t *testing.T) {
	baseline, err := filepath.Glob(filepath.Join(repoRoot(t), "pkg", "asymptoteobserve", "rulestore", "baseline", "*.rule.yaml"))
	if err != nil || len(baseline) == 0 {
		t.Fatalf("baseline rules: %v, %v", baseline, err)
	}
	for _, path := range baseline {
		matches, _ := filepath.Glob(filepath.Join(rulesDir(t), "*", filepath.Base(path)))
		if len(matches) != 1 {
			t.Errorf("%s: want exactly one corpus copy, found %v", filepath.Base(path), matches)
			continue
		}
		got, _ := os.ReadFile(path)
		want, _ := os.ReadFile(matches[0])
		if string(got) != string(want) {
			t.Errorf("%s differs from %s; copy the corpus rule over", path, matches[0])
		}
	}
}
