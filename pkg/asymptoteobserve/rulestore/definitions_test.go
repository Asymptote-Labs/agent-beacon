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
		}, true, `concepts/tokens.catalog.yaml): store gh-token: example "gh auth status": not matched`},
		"a predicate whose fixture fails": {func(f map[string]string) {
			f["concepts/anything.predicates.yaml"] = strings.Replace(tokenPredicates, "      - name: status\n        verdict: no_match", "      - name: status\n        verdict: match", 1)
		}, true, "definitions: predicate prints_token/status: expected match, got no_match"},
		"the same definitions twice": {func(f map[string]string) {
			f["other/tokens.catalog.yaml"] = tokenCatalog
		}, true, "catalog tokens is already declared by"},
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
		if err == nil || !strings.Contains(filepath.ToSlash(err.Error()), tc.want) {
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
		"concepts/tokens.catalog.yaml": "spec: threat-rules/v2.9\ncatalog: tokens\nversion: 1\nshape: unknown to this engine\n",
	})
	installed, skipped, err := InstallFilesSkipping(store, src, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(installed) != 1 || installed[0].ID != "current-rule" {
		t.Errorf("installed %+v, want current-rule alone", installed)
	}
	if len(skipped) != 2 || !strings.HasSuffix(skipped[0].Path, "tokens.catalog.yaml") || skipped[1].Err.RuleID != "future-rule" {
		t.Errorf("skipped %+v, want the catalog and the rule that need v2.9", skipped)
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
	newer := "spec: threat-rules/v2.9\ncatalog: tokens\nversion: 2\nshape: unknown to this engine\npredicates: {command: token_command, path: token_file}\n"
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
	if want := "tokens.catalog.yaml threat-rules/v2.9, file.rule.yaml threat-rules/v2.9"; strings.Join(got, ", ") != want {
		t.Errorf("skipped %q, want %q", strings.Join(got, ", "), want)
	}
	if catalog, _ := os.ReadFile(filepath.Join(store, "tokens.catalog.yaml")); string(catalog) != tokenCatalog {
		t.Error("the store's catalog changed")
	}
}

// The store is read for an install or a removal as every load reads it: recursively. A pack
// someone copied into a directory of the store is what an install compares with and replaces in
// place, so that the store never ends up with a second copy of a rule or of definitions, which
// would fail every later load.
func TestTheStoreIsReadAsALoadReadsIt(t *testing.T) {
	store := pack(t, map[string]string{
		"copied/token.rule.yaml":              tokenRule,
		"copied/tokens.catalog.yaml":          tokenCatalog,
		"copied/concepts/x.predicates.yaml":   tokenPredicates,
		"copied/__MACOSX/._token.rule.yaml":   "\x00\x05\x16\x07",
		"copied/concepts/._x.predicates.yaml": "\x00\x05\x16\x07",
	})
	if _, err := InstallFiles(store, tokenPack(t), false); err == nil || !strings.Contains(err.Error(), `rule "token-printed" already installed`) {
		t.Fatalf("install over the copied pack without force: %v", err)
	}
	installed, err := InstallFiles(store, tokenPack(t), true)
	if err != nil || len(installed) != 1 || installed[0].Path != filepath.Join(store, "copied", "token.rule.yaml") {
		t.Fatalf("install over the copied pack with force = %+v, %v; want the copied rule replaced in place", installed, err)
	}
	if _, err := LoadActive(store, ""); err != nil {
		t.Fatalf("the store does not load after the install: %v", err)
	}
	removed, err := Remove(store, "token-printed")
	if err != nil || removed != filepath.Join(store, "copied", "token.rule.yaml") {
		t.Errorf("Remove = %q, %v; want the copied rule", removed, err)
	}
}

// The store's definitions are listed by the name a removal takes, with the predicates each
// provides, and removed by it once nothing calls them: no rule, directly or through another
// predicate, and no definitions left behind.
func TestDefinitionsAreListedAndRemovedOnceNothingCallsThem(t *testing.T) {
	store := t.TempDir()
	if _, err := InstallFiles(store, tokenPack(t), false); err != nil {
		t.Fatal(err)
	}
	listed, err := ListDefinitions(store)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range listed {
		got = append(got, d.File+" "+d.Head.String()+" "+strings.Join(d.Predicates, ",")+" "+filepath.Base(d.Path))
	}
	if want := "tokens.catalog.yaml catalog tokens token_command tokens.catalog.yaml; tokens.predicates.yaml library tokens prints_token tokens.predicates.yaml"; strings.Join(got, "; ") != want {
		t.Errorf("ListDefinitions = %q, want %q", strings.Join(got, "; "), want)
	}

	refuse := func(name, want string) {
		t.Helper()
		if _, err := Remove(store, name); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Remove(%s) = %v, want an error saying %q", name, err, want)
		}
	}
	// The rule calls prints_token, whose body calls token_command.
	refuse("tokens.catalog.yaml", `the rule "token-printed" calls token_command`)
	refuse("tokens.predicates.yaml", `the rule "token-printed" calls prints_token`)
	refuse("missing.catalog.yaml", `definitions "missing.catalog.yaml" are not installed`)
	refuse("../tokens.catalog.yaml", `are not installed`)
	if _, err := Remove(store, "token-printed"); err != nil {
		t.Fatal(err)
	}
	// No rule left, but the predicates left would call into the catalog.
	refuse("tokens.catalog.yaml", "the definitions left would fail predicate prints_token")
	for _, name := range []string{"tokens.predicates.yaml", "tokens.catalog.yaml"} {
		if removed, err := Remove(store, name); err != nil || removed != filepath.Join(store, name) {
			t.Errorf("Remove(%s) = %q, %v", name, removed, err)
		}
	}
	if got := storeFiles(t, store); got != "" {
		t.Errorf("the store holds %q, want nothing", got)
	}
}

// A hidden folder in the store, such as the staging folder an earlier Beacon's interrupted
// install left, holds no part of it: its copies of the store's files are not second rules.
func TestTheStoresHiddenFoldersAreNotRead(t *testing.T) {
	store := t.TempDir()
	if _, err := InstallFiles(store, tokenPack(t), false); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{".install-1/token-printed.rule.yaml": tokenRule, ".install-1/tokens.catalog.yaml": tokenCatalog} {
		path := filepath.Join(store, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if loaded, err := LoadActive(store, ""); err != nil || len(bySource(loaded, SourceStore)) != 1 {
		t.Errorf("LoadActive = %d store rules, %v; want the one rule", len(bySource(loaded, SourceStore)), err)
	}
}

// A definitions file the store cannot load is removed, which is what lets the store load again;
// one at odds with another over a predicate too, though a rule calls the predicate, since the
// other still provides it.
func TestDefinitionsThatDoNotLoadAreRemoved(t *testing.T) {
	for _, tc := range []struct{ name, file, body string }{
		{"broken", "broken.catalog.yaml", "catalog: [not a name\n"},
		{"at odds", "again.predicates.yaml", strings.Replace(tokenPredicates, "library: tokens", "library: again", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := t.TempDir()
			if _, err := InstallFiles(store, tokenPack(t), false); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(store, tc.file), []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadActive(store, ""); err == nil {
				t.Fatal("the store loads with the file; want it not to")
			}
			if _, err := Remove(store, tc.file); err != nil {
				t.Fatalf("Remove(%s) = %v", tc.file, err)
			}
			if _, err := LoadActive(store, ""); err != nil {
				t.Errorf("the store does not load after the removal: %v", err)
			}
		})
	}
}

// A rule set aside for a newer spec still calls what it calls, and definitions set aside for one
// still provide what they declare: either keeps the definitions it calls from being removed,
// which would leave the rule calling a predicate nothing defines once Beacon reads its spec.
func TestDefinitionsANewerSpecCallsAreNotRemoved(t *testing.T) {
	newerRule := strings.NewReplacer("id: token-printed", "id: token-later", "spec: "+threatrules.SupportedSpec, "spec: threat-rules/v2.9").Replace(tokenRule)
	newerPredicates := "spec: threat-rules/v2.9\n" + strings.Replace(tokenPredicates, "library: tokens", "library: later", 1)
	newerPredicates = strings.ReplaceAll(newerPredicates, "prints_token", "prints_token_later")
	callsLater := strings.NewReplacer("id: token-printed", "id: token-calls-later", "match: prints_token(e)", "match: prints_token_later(e)").Replace(tokenRule)
	for _, tc := range []struct {
		name   string
		files  map[string]string
		remove string
		want   string
	}{
		{"a newer rule", map[string]string{"token-later.rule.yaml": newerRule}, "tokens.predicates.yaml", `the rule "token-later" calls prints_token`},
		{"newer definitions", map[string]string{"later.predicates.yaml": newerPredicates, "token-calls-later.rule.yaml": callsLater}, "later.predicates.yaml", `the rule "token-calls-later" calls prints_token_later`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := t.TempDir()
			if _, err := InstallFiles(store, tokenPack(t), false); err != nil {
				t.Fatal(err)
			}
			if _, err := Remove(store, "token-printed"); err != nil {
				t.Fatal(err)
			}
			for name, body := range tc.files {
				if err := os.WriteFile(filepath.Join(store, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, skipped, err := LoadActiveSkipping(store, ""); err != nil || len(skipped) == 0 {
				t.Fatalf("LoadActiveSkipping = %d set aside, %v; want the newer file set aside", len(skipped), err)
			}
			if _, err := Remove(store, tc.remove); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Remove(%s) = %v, want an error saying %q", tc.remove, err, tc.want)
			}
		})
	}
}
