package concepts_test

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/concepts"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
)

var update = flag.Bool("update", false, "rewrite the generated credential store reference")

const regenerate = "go test ./concepts -run TestCredentialStoresReferenceInSync -update (in pkg/asymptoteobserve)"

func conceptsDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "rules", "concepts")
}

func credentialStores(t *testing.T) *concepts.Catalog {
	t.Helper()
	c, err := concepts.Load(filepath.Join(conceptsDir(t), "credential-stores.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestCredentialStoresInTheRuleEngine runs the generated CEL expression in the threat-rules
// engine, as a rule would carry it, over every example and counterexample: the engine must say
// what the regular expressions say on their own, for each tier.
func TestCredentialStoresInTheRuleEngine(t *testing.T) {
	c := credentialStores(t)
	for _, tier := range []concepts.Tier{concepts.TierDefinite, concepts.TierLikely} {
		selection, err := c.Select(tier)
		if err != nil {
			t.Fatal(err)
		}
		prog, err := threatrules.CompileMatch(`e.event.action == "file.read" && ` + selection.CEL("e.file.path"))
		if err != nil {
			t.Fatalf("%s: the generated expression does not compile as a rule match: %v", tier, err)
		}
		for _, s := range c.Stores {
			for i, path := range append(append([]string(nil), s.Examples...), s.Counterexamples...) {
				want := false
				for _, term := range selection {
					if regexp.MustCompile(term.Include).MatchString(path) && (term.Exclude == "" || !regexp.MustCompile(term.Exclude).MatchString(path)) {
						want = true
					}
				}
				// An example is in the selection exactly when its store's tier is, and a
				// counterexample never is.
				if expect := i < len(s.Examples) && (tier == concepts.TierLikely || s.Tier == concepts.TierDefinite); want != expect {
					t.Errorf("%s: %q of store %s (%s): the expressions say %v", tier, path, s.ID, s.Tier, want)
				}
				event := asymptoteobserve.Event{
					Event: asymptoteobserve.EventInfo{Action: "file.read"},
					File:  &asymptoteobserve.FileInfo{Path: path},
				}
				if got, err := threatrules.EvalMatch(prog, event); err != nil || got != want {
					t.Errorf("%s: the engine says %v, %v for %q; the expressions say %v", tier, got, err, path, want)
				}
			}
		}
	}
}

// TestCredentialStoresReferenceInSync asserts the committed reference is the one generated from
// the catalog, so a change to a store shows up in review as a change to the expressions rules use.
func TestCredentialStoresReferenceInSync(t *testing.T) {
	want, err := credentialStores(t).Markdown("credential-stores.yaml", regenerate)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(conceptsDir(t), "credential-stores.md")
	if *update {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the reference (regenerate: %s): %v", regenerate, err)
	}
	// Compared after normalizing line endings: Git for Windows may check the file out with CRLF.
	if strings.ReplaceAll(string(got), "\r\n", "\n") != want {
		t.Fatalf("rules/concepts/credential-stores.md is stale; regenerate with: %s", regenerate)
	}
}
