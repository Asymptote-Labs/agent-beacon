package rulestore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
)

const tokenCatalog = `
catalog: tokens
version: 1
predicates: {command: token_command}
kinds:
  - {id: cli, title: CLI tokens}
stores:
  - id: gh-token
    title: GitHub CLI token
    kind: cli
    tier: definite
    commands: [{name: gh, words: [auth, token]}]
    examples: ["gh auth token"]
`

const tokenPredicates = `
library: tokens
version: 1
definitions:
  - name: prints_token
    params: [e]
    description: The event is a command that prints a token.
    body: e.event.action == "command.executed" && e.command.parsed_commands.exists(c, token_command(c))
    tests:
      - name: gh
        verdict: match
        events: [{event: {action: command.executed}, command: {command: "gh auth token"}}]
      - name: status
        verdict: no_match
        events: [{event: {action: command.executed}, command: {command: "gh auth status"}}]
`

// tokenRule calls a predicate, so it is a rule only with the definitions above.
const tokenRule = `
id: token-printed
spec: ` + threatrules.SupportedSpec + `
version: 1
title: T
severity: low
status: experimental
posture: detect
match: prints_token(e)
emit:
  reason: ok
tests:
  - name: p
    verdict: match
    events:
      - event: { action: command.executed }
        command: { command: "gh auth token" }
`

// pack writes a pack directory: rules at its top, definitions under concepts/.
func pack(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func tokenPack(t *testing.T) string {
	return pack(t, map[string]string{
		"token.rule.yaml":                   tokenRule,
		"concepts/tokens.catalog.yaml":      tokenCatalog,
		"concepts/anything.predicates.yaml": tokenPredicates,
	})
}

func storeFiles(t *testing.T, store string) string {
	t.Helper()
	entries, err := os.ReadDir(store)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return strings.Join(names, " ")
}

// A pack is installed with its definitions, under the names they declare, and the installed rule
// then means what the pack says: it is loaded from the store with them and fires.
func TestInstallCarriesThePacksDefinitions(t *testing.T) {
	store := t.TempDir()
	installed, err := InstallFiles(store, tokenPack(t), false)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, in := range installed {
		kind := "rule"
		if in.Definitions {
			kind = "definitions"
		}
		got = append(got, kind+" "+in.ID+" "+filepath.Base(in.Path))
	}
	if want := "definitions tokens tokens.predicates.yaml, definitions tokens tokens.catalog.yaml, rule token-printed token-printed.rule.yaml"; strings.Join(got, ", ") != want {
		t.Errorf("installed %q, want %q", strings.Join(got, ", "), want)
	}
	if got, want := storeFiles(t, store), "token-printed.rule.yaml tokens.catalog.yaml tokens.predicates.yaml"; got != want {
		t.Errorf("the store holds %q, want %q", got, want)
	}

	loaded, err := LoadActive(store, "")
	if err != nil {
		t.Fatal(err)
	}
	fromStore := bySource(loaded, SourceStore)
	if len(fromStore) != 1 {
		t.Fatalf("%d store rules, want 1", len(fromStore))
	}
	compiled, err := threatrules.Compile(fromStore[0])
	if err != nil {
		t.Fatal(err)
	}
	verdict, err := compiled.Evaluate([]asymptoteobserve.Event{{
		Event:   asymptoteobserve.EventInfo{Action: "command.executed"},
		Command: &asymptoteobserve.CommandInfo{Command: "sudo gh auth token"},
	}})
	if err != nil || verdict != threatrules.VerdictMatch {
		t.Errorf("the installed rule = %s, %v on `sudo gh auth token`; want match", verdict, err)
	}

	// A rule added on its own afterwards is one of the store's rules: it has the store's definitions.
	second := filepath.Join(t.TempDir(), "second.rule.yaml")
	if err := os.WriteFile(second, []byte(strings.Replace(tokenRule, "id: token-printed", "id: token-printed-again", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallFiles(store, second, false); err != nil {
		t.Errorf("a rule that calls the store's predicates, added alone: %v", err)
	}
	// And the same pack again changes nothing about the definitions, which are as installed.
	again, err := InstallFiles(store, tokenPack(t), true)
	if err != nil || len(again) != 1 || again[0].Definitions {
		t.Errorf("reinstalling the pack = %+v, %v; want the rule alone", again, err)
	}
}

// A rule that calls a predicate is not installed without the definitions that give it.
func TestInstallRejectsARuleWithoutItsDefinitions(t *testing.T) {
	store := t.TempDir()
	_, err := InstallFiles(store, pack(t, map[string]string{"token.rule.yaml": tokenRule}), false)
	if err == nil || !strings.Contains(err.Error(), "prints_token is not a predicate of the definitions this rule was loaded with") {
		t.Fatalf("want the missing-definitions error, got %v", err)
	}
	if got := storeFiles(t, store); got != "" {
		t.Errorf("the store holds %q after a refused install", got)
	}
}

// Definitions that are wrong, or that the store already has in another version, stop the install
// before anything is written.
func TestInstallRejectsDefinitions(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(files map[string]string)
		force  bool
		want   string
	}{
		"a catalog whose example fails": {func(f map[string]string) {
			f["concepts/tokens.catalog.yaml"] = strings.Replace(tokenCatalog, `examples: ["gh auth token"]`, `examples: ["gh auth status"]`, 1)
		}, true, `definitions: catalog tokens (tokens.catalog.yaml): store gh-token: example "gh auth status": not matched`},
		"a predicate whose fixture fails": {func(f map[string]string) {
			f["concepts/anything.predicates.yaml"] = strings.Replace(tokenPredicates, "      - name: status\n        verdict: no_match", "      - name: status\n        verdict: match", 1)
		}, true, "definitions: predicate prints_token/status: expected match, got no_match"},
		"the same definitions twice": {func(f map[string]string) {
			f["other/tokens.catalog.yaml"] = tokenCatalog
		}, true, "are both the definitions tokens.catalog.yaml"},
		"definitions without a name": {func(f map[string]string) {
			f["concepts/tokens.catalog.yaml"] = strings.Replace(tokenCatalog, "catalog: tokens", "catalog: ../../etc", 1)
		}, true, "the definitions have no name"},
		"another version without force": {func(f map[string]string) {
			f["concepts/tokens.catalog.yaml"] = strings.Replace(tokenCatalog, "version: 1", "version: 2", 1)
		}, false, `definitions "tokens.catalog.yaml" are already installed and differ (use --force to overwrite)`},
		"a version that breaks an installed rule": {func(f map[string]string) {
			delete(f, "token.rule.yaml")
			f["concepts/anything.predicates.yaml"] = strings.ReplaceAll(tokenPredicates, "prints_token", "shows_token")
		}, true, `these definitions would break the installed rule "token-printed"`},
	} {
		store := t.TempDir()
		if _, err := InstallFiles(store, tokenPack(t), false); err != nil {
			t.Fatal(err)
		}
		before := storeFiles(t, store)
		catalog, _ := os.ReadFile(filepath.Join(store, "tokens.catalog.yaml"))

		files := map[string]string{
			"token.rule.yaml":                   tokenRule,
			"concepts/tokens.catalog.yaml":      tokenCatalog,
			"concepts/anything.predicates.yaml": tokenPredicates,
		}
		tc.change(files)
		_, err := InstallFiles(store, pack(t, files), tc.force)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: InstallFiles = %v, want an error about %q", name, err, tc.want)
		}
		after, _ := os.ReadFile(filepath.Join(store, "tokens.catalog.yaml"))
		if storeFiles(t, store) != before || string(after) != string(catalog) {
			t.Errorf("%s: the store changed though the install was refused", name)
		}
	}
}

// With force, a newer version of the definitions replaces the installed one, and the rules
// installed earlier then mean what it says.
func TestForceInstallReplacesDefinitions(t *testing.T) {
	store := t.TempDir()
	if _, err := InstallFiles(store, tokenPack(t), false); err != nil {
		t.Fatal(err)
	}
	wider := strings.Replace(tokenCatalog, `commands: [{name: gh, words: [auth, token]}]`, `commands: [{name: "{gh,glab}", words: [auth, token]}]`, 1)
	installed, err := InstallFiles(store, pack(t, map[string]string{"tokens.catalog.yaml": wider}), true)
	if err != nil || len(installed) != 1 || !installed[0].Definitions || installed[0].ID != "tokens" {
		t.Fatalf("InstallFiles = %+v, %v; want the catalog alone", installed, err)
	}
	loaded, err := LoadActive(store, "")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := threatrules.Compile(bySource(loaded, SourceStore)[0])
	if err != nil {
		t.Fatal(err)
	}
	verdict, err := compiled.Evaluate([]asymptoteobserve.Event{{
		Event:   asymptoteobserve.EventInfo{Action: "command.executed"},
		Command: &asymptoteobserve.CommandInfo{Command: "glab auth token"},
	}})
	if err != nil || verdict != threatrules.VerdictMatch {
		t.Errorf("the rule under the newer catalog = %s, %v on `glab auth token`; want match", verdict, err)
	}
}

// Definitions written for a newer engine are left out with the rules that need them, and the
// rest of the pack installs.
func TestInstallFilesSkippingLeavesNewerDefinitionsOut(t *testing.T) {
	store := t.TempDir()
	src := pack(t, map[string]string{
		"a.rule.yaml":                  ruleWithID("current-rule"),
		"future.rule.yaml":             futureRule,
		"concepts/tokens.catalog.yaml": "spec: threat-rules/v1.9\ncatalog: tokens\nversion: 1\nshape: unknown to this engine\n",
	})
	installed, skipped, err := InstallFilesSkipping(store, src, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(installed) != 1 || installed[0].ID != "current-rule" {
		t.Errorf("installed %+v, want current-rule alone", installed)
	}
	if len(skipped) != 2 || !strings.HasSuffix(skipped[0].Path, "tokens.catalog.yaml") || skipped[1].Err.RuleID != "future-rule" {
		t.Errorf("skipped %+v, want the catalog and the rule that need v1.9", skipped)
	}
	if got := storeFiles(t, store); got != "current-rule.rule.yaml" {
		t.Errorf("the store holds %q", got)
	}
	if _, err := InstallFiles(t.TempDir(), src, false); err == nil {
		t.Error("strict InstallFiles: want an error for the newer-spec definitions")
	}
}

// A newer version of definitions the store holds is left out, and the store's own version stays
// what the incoming rules are validated against: the rule that calls its predicates installs. A
// rule that calls a predicate only the newer file defines is left out with it.
func TestInstallFilesSkippingKeepsTheStoresVersionOfNewerDefinitions(t *testing.T) {
	store := t.TempDir()
	if _, err := InstallFiles(store, tokenPack(t), false); err != nil {
		t.Fatal(err)
	}
	newer := "spec: threat-rules/v1.9\ncatalog: tokens\nversion: 2\nshape: unknown to this engine\npredicates: {command: token_command, path: token_file}\n"
	src := pack(t, map[string]string{
		"again.rule.yaml":              strings.Replace(tokenRule, "id: token-printed", "id: token-printed-again", 1),
		"file.rule.yaml":               strings.NewReplacer("id: token-printed", "id: token-file-read", "match: prints_token(e)", "match: token_file(e.file.path)").Replace(tokenRule),
		"concepts/tokens.catalog.yaml": newer,
	})
	installed, skipped, err := InstallFilesSkipping(store, src, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(installed) != 1 || installed[0].ID != "token-printed-again" {
		t.Errorf("installed %+v, want token-printed-again alone", installed)
	}
	var got []string
	for _, s := range skipped {
		got = append(got, filepath.Base(s.Path)+" "+s.Err.Required)
	}
	if want := "tokens.catalog.yaml threat-rules/v1.9, file.rule.yaml threat-rules/v1.9"; strings.Join(got, ", ") != want {
		t.Errorf("skipped %q, want %q", strings.Join(got, ", "), want)
	}
	if catalog, _ := os.ReadFile(filepath.Join(store, "tokens.catalog.yaml")); string(catalog) != tokenCatalog {
		t.Error("the store's catalog changed")
	}
}
