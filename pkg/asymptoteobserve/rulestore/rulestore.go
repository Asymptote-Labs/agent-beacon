// Package rulestore manages the open threat-rules corpus shared across Beacon
// components: it ships a small frozen baseline embedded in the binary and loads,
// installs, and removes rules in a local store directory.
//
// The engine ships in the binary; the rule corpus is external data. Only the
// frozen baseline below is embedded, so a growing corpus loaded into the store
// never enlarges the binary.
//
// A pack's definitions (the catalogs and predicate definitions its rules call, see
// threatrules.Library) are part of the corpus: they are installed into the store with
// the rules, kept beside them, and embedded with the baseline. The store's rules compile
// against the store's definitions and the baseline's against the baseline's, so a rule
// always means what the pack it came from says.
//
// Unlike the cli/beacon `detect` adapter that wraps it, this package takes an
// explicit store directory rather than resolving one from endpoint config, so it
// can be imported from any module (beacon-hooks, an external policy provider, the
// public CLI) without depending on cli/beacon internals.
package rulestore

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
)

//go:embed baseline/*.yaml
var baselineFS embed.FS

// RuleFileSuffix is the extension every rule corpus file must use.
const RuleFileSuffix = ".rule.yaml"

// Source identifies where an active rule came from.
type Source string

const (
	SourceBaseline Source = "baseline"
	SourceStore    Source = "store"
)

// LoadedRule is a validated rule paired with its origin.
type LoadedRule struct {
	Rule   *threatrules.Rule
	Source Source
}

// Installed reports a file written into the store: a rule, or one of a pack's definitions.
type Installed struct {
	// ID is the rule's id, or the name the definitions declare.
	ID   string
	Path string
	// Definitions is set for a definitions file rather than a rule.
	Definitions bool
}

// EnsureStore creates the store directory if needed and returns its path.
func EnsureStore(storeDir string) (string, error) {
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return "", fmt.Errorf("create rules store: %w", err)
	}
	return storeDir, nil
}

// Baseline returns the embedded frozen baseline rules, validated against the definitions
// embedded with them.
func Baseline() ([]*threatrules.Rule, error) {
	entries, err := baselineFS.ReadDir("baseline")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), RuleFileSuffix) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	embedded, err := fs.Sub(baselineFS, "baseline")
	if err != nil {
		return nil, err
	}
	lib, skipped, err := threatrules.LoadLibraryFS(embedded)
	if err != nil {
		return nil, fmt.Errorf("baseline definitions: %w", err)
	}
	if len(skipped) > 0 {
		return nil, fmt.Errorf("baseline definitions: %s", skipped[0])
	}

	rules := make([]*threatrules.Rule, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		data, err := baselineFS.ReadFile(baselineRulePath(name))
		if err != nil {
			return nil, err
		}
		rule, err := lib.DecodeRule(data)
		if err != nil {
			return nil, fmt.Errorf("baseline %s: %w", name, err)
		}
		if err := rule.Validate(); err != nil {
			return nil, fmt.Errorf("baseline %s: %w", name, err)
		}
		if seen[rule.ID] {
			return nil, fmt.Errorf("baseline duplicate rule id %q", rule.ID)
		}
		seen[rule.ID] = true
		rules = append(rules, rule)
	}
	return rules, nil
}

func baselineRulePath(name string) string {
	return path.Join("baseline", name)
}

// LoadActive resolves the active rule set:
//
//   - if rulesDir is non-empty, load only from that directory (explicit override);
//   - else load the store rules from storeDir plus every embedded baseline rule whose
//     id the store does not define (a store rule replaces the baseline rule with the
//     same id). An empty or absent store therefore yields exactly the baseline.
//
// The baseline stays active alongside the store so that installing one rule can never
// silently switch the baseline off; a store rule overrides a baseline rule only by id.
//
// Every returned rule has passed validation (via threatrules.LoadDir / Baseline). A rule
// that needs a newer spec than this engine supports fails the load; LoadActiveSkipping is
// the variant that sets it aside instead.
func LoadActive(storeDir, rulesDir string) ([]LoadedRule, error) {
	loaded, skipped, err := LoadActiveSkipping(storeDir, rulesDir)
	if err != nil {
		return nil, err
	}
	if len(skipped) > 0 {
		return nil, fmt.Errorf("%s: %w", skipped[0].Path, skipped[0].Err)
	}
	return loaded, nil
}

// LoadActiveSkipping is LoadActive, except that store or override rules declaring a spec
// newer than threatrules.SupportedSpec are left out and returned in skipped, so the rest
// of the corpus still runs. Callers surface skipped as warnings. The embedded baseline is
// never skipped: it ships with the engine that evaluates it.
func LoadActiveSkipping(storeDir, rulesDir string) ([]LoadedRule, []threatrules.SkippedRule, error) {
	if rulesDir != "" {
		rules, skipped, err := threatrules.LoadDirSkipping(rulesDir)
		if err != nil {
			return nil, nil, err
		}
		return tag(rules, SourceStore), skipped, nil
	}

	base, err := Baseline()
	if err != nil {
		return nil, nil, err
	}
	if !HasRuleFiles(storeDir) {
		return tag(base, SourceBaseline), nil, nil
	}
	stored, skipped, err := threatrules.LoadDirSkipping(storeDir)
	if err != nil {
		return nil, nil, err
	}
	active := tag(stored, SourceStore)
	defined := make(map[string]bool, len(stored))
	for _, r := range stored {
		defined[r.ID] = true
	}
	for _, r := range base {
		if !defined[r.ID] {
			active = append(active, LoadedRule{Rule: r, Source: SourceBaseline})
		}
	}
	return active, skipped, nil
}

// IsBaselineID reports whether id names a rule in the embedded baseline.
func IsBaselineID(id string) bool {
	base, err := Baseline()
	if err != nil {
		return false
	}
	for _, r := range base {
		if r.ID == id {
			return true
		}
	}
	return false
}

func tag(rules []*threatrules.Rule, src Source) []LoadedRule {
	out := make([]LoadedRule, len(rules))
	for i, r := range rules {
		out[i] = LoadedRule{Rule: r, Source: src}
	}
	return out
}

// HasRuleFiles reports whether dir contains at least one rule corpus file.
func HasRuleFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), RuleFileSuffix) {
			return true
		}
	}
	return false
}

// InstallFiles validates the pack found at src (a file or directory) and writes it into
// storeDir: every *.rule.yaml, and the definitions beside them. Everything is validated
// before any file is written, and an invalid rule or definition aborts the whole install.
// A rule whose id already exists in the store is rejected unless force is set, and so are
// definitions that differ from the ones the store has under the same name. Returns what
// was installed. A rule that needs a newer spec than this engine supports aborts the
// install like any other invalid rule; InstallFilesSkipping is the variant that leaves it
// out instead.
//
// The rules are validated against the definitions the store will hold afterwards: its
// own, with the source's laid over them. When the source brings definitions, the rules
// already in the store must still validate against the result, so that updating a catalog
// cannot leave behind a rule that no longer compiles. It can change what such a rule
// matches: that is what updating a definition is for.
func InstallFiles(storeDir, src string, force bool) ([]Installed, error) {
	return installFiles(storeDir, src, force, nil)
}

// InstallFilesSkipping is InstallFiles, except that a rule declaring a spec newer than
// threatrules.SupportedSpec is not installed and is returned in skipped, while the rest
// of the source installs. The store only ever holds rules this engine validated, so a
// skipped rule is picked up by re-running the install after upgrading Beacon.
func InstallFilesSkipping(storeDir, src string, force bool) ([]Installed, []threatrules.SkippedRule, error) {
	var skipped []threatrules.SkippedRule
	installed, err := installFiles(storeDir, src, force, &skipped)
	if err != nil {
		return nil, nil, err
	}
	return installed, skipped, nil
}

// installFiles implements InstallFiles. When skipped is non-nil, rules and definitions
// needing a newer spec are appended to it rather than aborting the install.
func installFiles(storeDir, src string, force bool, skipped *[]threatrules.SkippedRule) ([]Installed, error) {
	rulePaths, definitionPaths, err := packFilesAt(src)
	if err != nil {
		return nil, err
	}
	if len(rulePaths)+len(definitionPaths) == 0 {
		return nil, fmt.Errorf("no %s files found at %s", RuleFileSuffix, src)
	}

	store, err := EnsureStore(storeDir)
	if err != nil {
		return nil, err
	}
	existing, err := storeIDs(store)
	if err != nil {
		return nil, err
	}

	// Validate everything first; collect the bytes + target names. Abort on any failure
	// so a partial/invalid install never lands.
	type pending struct {
		id          string
		file        string // the name in the store
		data        []byte
		definitions bool
	}
	var todo []pending

	// The definitions the store will hold: its own, with the source's laid over them. A source
	// file that needs a newer spec is not laid over: the store keeps its version, which is what
	// the rules are validated against. It is still given to the library, which sets it aside and
	// with it the rules that call a predicate only it defines.
	definitions, err := storeDefinitions(store)
	if err != nil {
		return nil, err
	}
	var files []threatrules.DefinitionFile
	incoming := map[string]string{} // name in the store -> source path
	for _, p := range definitionPaths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		file := threatrules.DefinitionFile{Name: p, Data: data}
		name, err := file.StoreName()
		if err != nil {
			return nil, err
		}
		if prev, dup := incoming[name]; dup {
			return nil, fmt.Errorf("%s and %s are both the definitions %s", prev, p, name)
		}
		incoming[name] = p
		if unsupported := file.Unsupported(); unsupported != nil {
			if skipped == nil {
				return nil, fmt.Errorf("%s: %w", p, unsupported)
			}
			*skipped = append(*skipped, threatrules.SkippedRule{Path: p, Err: unsupported})
			files = append(files, file)
			continue
		}
		if old, ok := definitions[name]; ok {
			if bytes.Equal(old, data) {
				continue // already installed as it is
			}
			if !force {
				return nil, fmt.Errorf("definitions %q are already installed and differ (use --force to overwrite)", name)
			}
		}
		definitions[name] = data
		todo = append(todo, pending{id: definitionID(name), file: name, data: data, definitions: true})
	}
	for name, data := range definitions {
		files = append(files, threatrules.DefinitionFile{Name: name, Data: data})
	}
	// The files it sets aside are the source's reported above, and the store's own, which every
	// load sets aside the same way.
	lib, _, err := threatrules.NewLibrary(files)
	if err != nil {
		return nil, fmt.Errorf("definitions: %w", err)
	}
	for _, res := range lib.Check() {
		if !res.OK() {
			return nil, fmt.Errorf("definitions: fixture %s", res.String())
		}
	}
	changesDefinitions := slices.ContainsFunc(todo, func(p pending) bool { return p.definitions })

	staged := map[string]bool{}
	for _, p := range rulePaths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		rule, err := lib.DecodeRule(data)
		if err != nil {
			if unsupported, ok := threatrules.AsUnsupportedSpec(err); ok && skipped != nil {
				*skipped = append(*skipped, threatrules.SkippedRule{Path: p, Err: unsupported})
				continue
			}
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		results, err := threatrules.CheckRule(rule)
		if err != nil {
			// A rule that calls a predicate set aside for a newer spec is set aside with it.
			if unsupported, ok := threatrules.AsUnsupportedSpec(err); ok && skipped != nil {
				*skipped = append(*skipped, threatrules.SkippedRule{Path: p, Err: unsupported})
				continue
			}
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		for _, res := range results {
			if !res.OK() {
				return nil, fmt.Errorf("%s: fixture %s", p, res.String())
			}
		}
		// rule.ID passed CheckRule (id-grammar) above; re-assert it explicitly so the
		// store path join below can never be driven outside the store by rule content.
		if !threatrules.ValidID(rule.ID) {
			return nil, fmt.Errorf("%s: invalid rule id %q", p, rule.ID)
		}
		if (existing[rule.ID] || staged[rule.ID]) && !force {
			return nil, fmt.Errorf("rule %q already installed (use --force to overwrite)", rule.ID)
		}
		staged[rule.ID] = true
		todo = append(todo, pending{id: rule.ID, file: rule.ID + RuleFileSuffix, data: data})
	}

	// New definitions change what the rules already in the store mean. Each one this install
	// does not replace must still be a valid rule under them.
	if changesDefinitions {
		for id := range existing {
			if staged[id] {
				continue
			}
			_, err := lib.LoadRule(filepath.Join(store, id+RuleFileSuffix))
			if _, tooNew := threatrules.AsUnsupportedSpec(err); err != nil && !tooNew {
				return nil, fmt.Errorf("these definitions would break the installed rule %q (install the rules they belong to with them, or remove the rule): %w", id, err)
			}
		}
	}

	tmpDir, err := os.MkdirTemp(store, ".install-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	for _, p := range todo {
		if err := os.WriteFile(filepath.Join(tmpDir, p.file), p.data, 0o644); err != nil {
			return nil, err
		}
	}

	installed := make([]Installed, 0, len(todo))
	for _, p := range todo {
		dest := filepath.Join(store, p.file)
		backupPath, err := moveExistingAside(dest, tmpDir)
		if err != nil {
			rollbackInstall(installed, tmpDir)
			return nil, err
		}
		if err := os.Rename(filepath.Join(tmpDir, p.file), dest); err != nil {
			if backupPath != "" {
				_ = os.Rename(backupPath, dest)
			}
			rollbackInstall(installed, tmpDir)
			return nil, err
		}
		installed = append(installed, Installed{ID: p.id, Path: dest, Definitions: p.definitions})
	}
	return installed, nil
}

// definitionID is the name a definitions file declares, from its name in the store.
func definitionID(storeName string) string {
	for _, suffix := range []string{threatrules.CatalogFileSuffix, threatrules.PredicatesFileSuffix} {
		storeName = strings.TrimSuffix(storeName, suffix)
	}
	return storeName
}

func moveExistingAside(dest, tmpDir string) (string, error) {
	info, err := os.Lstat(dest)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s exists and is a directory", dest)
	}
	backupPath := filepath.Join(tmpDir, filepath.Base(dest)+".backup")
	if err := os.Rename(dest, backupPath); err != nil {
		return "", err
	}
	return backupPath, nil
}

func rollbackInstall(installed []Installed, tmpDir string) {
	for i := len(installed) - 1; i >= 0; i-- {
		backupPath := filepath.Join(tmpDir, filepath.Base(installed[i].Path)+".backup")
		_ = os.Remove(installed[i].Path)
		if _, err := os.Stat(backupPath); err == nil {
			_ = os.Rename(backupPath, installed[i].Path)
		}
	}
}

// Remove deletes a rule by id from storeDir. Returns the removed path. The id is
// validated against the rule-id grammar before use so it can never contain path
// separators or ".." segments that would resolve outside the store.
func Remove(storeDir, id string) (string, error) {
	if !threatrules.ValidID(id) {
		return "", fmt.Errorf("invalid rule id %q", id)
	}
	target := filepath.Join(storeDir, id+RuleFileSuffix)
	if err := os.Remove(target); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("rule %q is not installed in the store", id)
		}
		return "", err
	}
	return target, nil
}

// packFilesAt returns the *.rule.yaml files and the definitions files at src, which may
// be a single file of either kind or a directory.
func packFilesAt(src string) (rules, definitions []string, err error) {
	info, err := os.Stat(src)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() {
		switch {
		case isRuleFile(src):
			return []string{src}, nil, nil
		case threatrules.IsDefinitionFile(src):
			return nil, []string{src}, nil
		}
		return nil, nil, fmt.Errorf("%s is not a %s file", src, RuleFileSuffix)
	}
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
		case isRuleFile(path):
			rules = append(rules, path)
		case threatrules.IsDefinitionFile(path):
			definitions = append(definitions, path)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(rules)
	sort.Strings(definitions)
	return rules, definitions, nil
}

// storeDefinitions reads the definitions files of a store, by name. A store is flat.
func storeDefinitions(store string) (map[string][]byte, error) {
	definitions := map[string][]byte{}
	entries, err := os.ReadDir(store)
	if err != nil {
		if os.IsNotExist(err) {
			return definitions, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !threatrules.IsDefinitionFile(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(store, e.Name()))
		if err != nil {
			return nil, err
		}
		definitions[e.Name()] = data
	}
	return definitions, nil
}

func isRuleFile(path string) bool {
	return strings.HasSuffix(path, RuleFileSuffix) && !strings.HasPrefix(filepath.Base(path), "._")
}

func storeIDs(store string) (map[string]bool, error) {
	ids := map[string]bool{}
	entries, err := os.ReadDir(store)
	if err != nil {
		if os.IsNotExist(err) {
			return ids, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), RuleFileSuffix) {
			continue
		}
		ids[strings.TrimSuffix(e.Name(), RuleFileSuffix)] = true
	}
	return ids, nil
}
