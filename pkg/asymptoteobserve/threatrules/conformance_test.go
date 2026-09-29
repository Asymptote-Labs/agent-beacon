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
