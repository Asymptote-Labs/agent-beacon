package concepts_test

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	c, err := concepts.Load(filepath.Join(conceptsDir(t), "credential-stores.catalog.yaml"))
	if err == nil {
		err = c.Check()
	}
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestCredentialStoresInTheRuleEngine runs the generated expressions in the threat-rules engine,
// composed the way a rule reads a credential: a file read by path, a command that reads such a
// file, or a command that gets one out of a store. Every example of a store the tier takes in
// must match, as a file event and as a command, and no counterexample may.
func TestCredentialStoresInTheRuleEngine(t *testing.T) {
	c := credentialStores(t)
	for _, tier := range []concepts.Tier{concepts.TierDefinite, concepts.TierLikely} {
		selection, err := c.Select(tier)
		if err != nil {
			t.Fatal(err)
		}
		prog, err := threatrules.CompileMatch(
			`(e.event.action == "file.read" && ` + selection.PathCEL("e.file.path") + `) || ` +
				`(e.event.action == "command.executed" && e.command.parsed_commands.exists(c, ` +
				`c.file_reads.exists(f, ` + selection.PathCEL("f.path") + `) || ` + selection.CommandCEL("c") + `))`)
		if err != nil {
			t.Fatalf("%s: the generated expressions do not compile as a rule match: %v", tier, err)
		}
		check := func(s concepts.Store, what string, event asymptoteobserve.Event, want bool) {
			t.Helper()
			if got, err := threatrules.EvalMatch(prog, event); err != nil || got != want {
				t.Errorf("%s stores, %s of %s (%s): the engine says %v, %v; want %v", tier, what, s.ID, s.Tier, got, err, want)
			}
		}
		command := func(line string) asymptoteobserve.Event {
			return asymptoteobserve.Event{
				Event:   asymptoteobserve.EventInfo{Action: "command.executed"},
				Command: &asymptoteobserve.CommandInfo{Command: line},
			}
		}
		for _, s := range c.Stores {
			for i, example := range append(append([]string(nil), s.Examples...), s.Counterexamples...) {
				// An example is in the selection exactly when its store's tier is, and a
				// counterexample never is.
				want := i < len(s.Examples) && (tier == concepts.TierLikely || s.Tier == concepts.TierDefinite)
				if len(s.Commands) > 0 {
					check(s, fmt.Sprintf("command %q", example), command(example), want)
					continue
				}
				check(s, fmt.Sprintf("file %q", example), asymptoteobserve.Event{
					Event: asymptoteobserve.EventInfo{Action: "file.read"},
					File:  &asymptoteobserve.FileInfo{Path: example},
				}, want)
				check(s, fmt.Sprintf("cat of %q", example), command("cat '"+example+"'"), want)
			}
		}
	}
}

// TestCredentialStoresReferenceInSync asserts the committed reference is the one generated from
// the catalog, so a change to a store shows up in review as a change to the expressions rules use.
func TestCredentialStoresReferenceInSync(t *testing.T) {
	want, err := credentialStores(t).Markdown("credential-stores.catalog.yaml", regenerate)
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
	if string(got) != want {
		t.Fatalf("rules/concepts/credential-stores.md is stale; regenerate with: %s", regenerate)
	}
}
