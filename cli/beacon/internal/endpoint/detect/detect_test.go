package detect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
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
// cli/beacon adapter: store-dir resolution from endpoint config and delegation.

func TestStoreDirUsesBaseDir(t *testing.T) {
	if got, want := StoreDir(true), filepath.Join(endpointconfig.BaseDir(true), "rules"); got != want {
		t.Fatalf("StoreDir(true) = %q, want %q", got, want)
	}
}

func TestBaselineDelegates(t *testing.T) {
	rules, err := Baseline()
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if len(rules) == 0 {
		t.Fatal("embedded baseline is empty")
	}
	for _, r := range rules {
		if _, err := threatrules.CheckRule(r); err != nil {
			t.Errorf("baseline rule %q fails conformance: %v", r.ID, err)
		}
	}
}

func TestLoadActiveFallsBackToBaseline(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // empty store -> baseline
	loaded, err := LoadActive(true, "")
	if err != nil {
		t.Fatalf("load active: %v", err)
	}
	if len(loaded) == 0 || loaded[0].Source != SourceBaseline {
		t.Fatalf("expected baseline fallback, got %d rules", len(loaded))
	}
}

func TestInstallRemoveRoundTripViaUserMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

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

	if _, err := Remove(true, "custom-rule"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := Remove(true, "custom-rule"); err == nil {
		t.Fatal("removing a missing rule should error")
	}
}
