package threatrules

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// repoRoot locates the repository root by walking up from this test file until it finds
// the spec sentinel (spec/threat-rules/VERSION). This keeps tests hermetic without
// hardcoded absolute paths.
func repoRoot(t testing.TB) string {
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

func rulesDir(t testing.TB) string { return filepath.Join(repoRoot(t), "rules") }

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

// TestPackMatchesWhatTheLogHolds runs every fixture again after the sanitizer every log writer
// applies, and requires the same verdict. `beacon scan` and the dashboard only ever see the
// sanitized line, so a rule that matches a raw value redaction removes (a token, a key) is dead on
// real logs even though its plain fixtures pass.
func TestPackMatchesWhatTheLogHolds(t *testing.T) {
	rules, err := LoadDir(rulesDir(t))
	if err != nil {
		t.Fatalf("load rule pack: %v", err)
	}
	for _, rule := range rules {
		compiled, err := Compile(rule)
		if err != nil {
			t.Fatalf("rule %q: compile: %v", rule.ID, err)
		}
		for _, fixture := range rule.Tests {
			events := fixture.EventList()
			for i := range events {
				events[i] = asymptoteobserve.SanitizeEvent(events[i], 64*1024)
			}
			got, err := compiled.Evaluate(events)
			if err != nil {
				t.Errorf("%s/%s: evaluate sanitized events: %v", rule.ID, fixture.Name, err)
				continue
			}
			if got != fixture.Verdict {
				t.Errorf("%s/%s: verdict %q on the sanitized events the log holds, want %q", rule.ID, fixture.Name, got, fixture.Verdict)
			}
		}
	}
}

// TestPackCopiesNoCatalog holds the corpus to its own definitions: no rule carries a regular
// expression that spells out the stores of a catalog the pack ships. A rule that did would be
// right until the catalog changed, and then wrong without anything failing. The second half
// shows the check sees a copy: the list a rule used to carry before the catalog.
func TestPackCopiesNoCatalog(t *testing.T) {
	for _, rule := range corpusRules(t) {
		for _, copied := range CatalogCopies(rule) {
			t.Errorf("%s: %s", rule.ID, copied)
		}
	}

	lib, _, err := LoadLibraryDir(rulesDir(t))
	if err != nil {
		t.Fatal(err)
	}
	decode := func(match string) *Rule {
		rule, err := lib.DecodeRule([]byte("id: copy\nversion: 1\ntitle: t\ndescription: d\nseverity: low\nmatch: >\n  " + match + "\n"))
		if err != nil {
			t.Fatal(err)
		}
		return rule
	}
	copies := CatalogCopies(decode(`e.event.action == "file.read" && (e.file.path.matches("(?i)\\.aws/credentials") || e.file.path.matches("(?i)(^|/)(\\.netrc|\\.pgpass)$"))`))
	if len(copies) != 1 || copies[0].Catalog != "credential-stores" || !slices.Equal(copies[0].Stores, []string{"aws-shared-credentials", "netrc", "pgpass"}) || len(copies[0].Patterns) != 2 {
		t.Errorf("a rule with its own list of credential files: got %+v, want one copy of aws-shared-credentials, netrc and pgpass in two patterns", copies)
	}
	for _, match := range []string{
		// One store, named for the rule's own reason.
		`e.event.action == "file.modified" && e.file.path.matches("(?i)(^|/)\\.netrc$")`,
		// The catalog, called.
		`e.event.action == "file.read" && credential_path(e.file.path, "definite")`,
		// Names in a text no predicate is asked about.
		`e.gen_ai.tool.description.matches("(?i)read .{0,60}(\\.aws/credentials|\\.netrc)")`,
	} {
		if copies := CatalogCopies(decode(match)); len(copies) != 0 {
			t.Errorf("%s: reported as a copy: %v", match, copies)
		}
	}
}

// TestPackListsNoSenderOfItsOwn keeps "this command sends data over the network" in one place:
// the egress predicates of rules/concepts/egress.predicates.yaml. A rule that tests a parsed
// command against a list that names netcat has a sender list of its own, and eight such copies
// are what those predicates replaced. A text fallback for a command line that does not parse
// matches e.command.command and is the rule's own.
func TestPackListsNoSenderOfItsOwn(t *testing.T) {
	literal := regexp.MustCompile(`([\w.]+)\.matches\(("(?:[^"\\]|\\.)*")\)`)
	for _, r := range corpusRules(t) {
		for _, m := range literal.FindAllStringSubmatch(strings.Join(ruleExpressions(r), " "), -1) {
			receiver, list := m[1], m[2]
			if receiver != "e.command.command" && (strings.Contains(list, "ncat") || strings.Contains(list, "netcat")) {
				t.Errorf("%s: %s.matches(%s) lists network senders; call egress_sender instead, so that every egress rule means the same senders", r.ID, receiver, list)
			}
		}
	}
}

// TestPackCategoriesMatchDirectories pins taxonomy.beacon_category to the directory each rule
// lives in. The rule store keeps rules flat, so the category travels inside the rule: it is what
// groups findings by kind of risk (the Security Review lens, for one) once a rule is installed.
func TestPackCategoriesMatchDirectories(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(rulesDir(t), "*", "*.rule.yaml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("rule files: %v, %v", paths, err)
	}
	for _, path := range paths {
		rule := loadPackRule(t, path)
		want := filepath.Base(filepath.Dir(path))
		if got := rule.Taxonomy["beacon_category"]; got != want {
			t.Errorf("%s: taxonomy.beacon_category = %q, want its directory %q", path, got, want)
		}
	}
}

// TestBaselineIsCorpusCopies keeps the embedded baseline byte-identical to the corpus files it
// was taken from: its rules, and the definitions embedded for them. A fix to a corpus rule or
// catalog then cannot leave a stale copy in every binary.
func TestBaselineIsCorpusCopies(t *testing.T) {
	baseline, err := filepath.Glob(filepath.Join(repoRoot(t), "pkg", "asymptoteobserve", "rulestore", "baseline", "*.yaml"))
	if err != nil || len(baseline) == 0 {
		t.Fatalf("baseline files: %v, %v", baseline, err)
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
			t.Errorf("%s differs from %s; copy the corpus file over", path, matches[0])
		}
	}
}

// TestPackDefinitionsConformance runs the fixtures of the pack's definitions, as
// TestPackConformance runs its rules': each predicate definition's own, and each catalog's
// examples. Every predicate has to be shown at work by one.
func TestPackDefinitionsConformance(t *testing.T) {
	lib, skipped, err := LoadLibraryDir(rulesDir(t))
	if err != nil || len(skipped) > 0 {
		t.Fatalf("load the pack's definitions: %v, skipped %v", err, skipped)
	}
	tested := map[string]bool{}
	for _, res := range lib.Check() {
		tested[res.RuleID] = true
		if !res.OK() {
			t.Errorf("predicate %s", res.String())
		}
	}
	for _, p := range lib.Predicates() {
		if !tested[p.Name] {
			t.Errorf("predicate %s (%s) has no fixture", p.Name, p.Source)
		}
	}
	if len(lib.Predicates()) == 0 {
		t.Error("the pack defines no predicate")
	}
}
