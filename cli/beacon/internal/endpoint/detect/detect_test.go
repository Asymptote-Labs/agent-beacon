package detect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

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

// The exhaustive store mechanics (baseline conformance, install/remove, rollback,
// path traversal) live in pkg/asymptoteobserve/rulestore. These tests cover the thin
// cli/beacon adapter: baseline fallback and store delegation under the endpoint home.

func TestLoadActiveFallsBackToBaseline(t *testing.T) {
	testenv.SetHome(t, t.TempDir()) // empty store -> baseline
	loaded, err := LoadActive(true, "")
	if err != nil {
		t.Fatalf("load active: %v", err)
	}
	if len(loaded) == 0 || loaded[0].Source != SourceBaseline {
		t.Fatalf("expected baseline fallback, got %d rules", len(loaded))
	}
}

// Users and earlier releases put rules in the documented store (~/.beacon/endpoint/rules, see
// docs/cli/endpoint-paths.mdx). If the adapter resolved the store anywhere else, `beacon scan`
// would silently drop every rule already there and run the baseline alone.
func TestLoadActiveReadsRulesFromTheDocumentedUserStore(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	store := filepath.Join(home, ".beacon", "endpoint", "rules")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "kept-rule.rule.yaml"), []byte(ruleWithID("kept-rule")), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadActive(true, "")
	if err != nil {
		t.Fatalf("load active: %v", err)
	}
	for _, r := range loaded {
		if r.Rule.ID == "kept-rule" && r.Source == SourceStore {
			return
		}
	}
	t.Fatalf("rule placed in %s was not loaded from the store: %+v", store, loaded)
}

func TestInstallRemoveRoundTripViaUserMode(t *testing.T) {
	testenv.SetHome(t, t.TempDir())

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
	base, err := Baseline()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != len(base)+1 || loaded[0].Source != SourceStore || loaded[0].Rule.ID != "custom-rule" {
		t.Fatalf("expected the store rule plus the baseline, got %+v", loaded)
	}

	if _, err := Remove(true, "custom-rule"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := Remove(true, "custom-rule"); err == nil {
		t.Fatal("removing a missing rule should error")
	}
}
