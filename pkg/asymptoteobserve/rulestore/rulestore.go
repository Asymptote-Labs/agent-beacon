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
// always means what the pack it came from says. The store, the baseline and a pack being
// installed are each read as a pack (threatrules.ReadPackFS): recursively, by the same
// test of what a pack file is.
//
// Unlike the cli/beacon `detect` adapter that wraps it, this package takes an
// explicit store directory rather than resolving one from endpoint config, so it
// can be imported from any module (beacon-hooks, an external policy provider, the
// public CLI) without depending on cli/beacon internals.
package rulestore

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
)

//go:embed baseline/*.yaml
var baselineFS embed.FS

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

// baseline is the embedded pack, loaded once: it never changes, and what is compiled for its
// definitions (the expressions its rules match) is kept with it for every later scan.
var baseline = sync.OnceValues(func() (*threatrules.Pack, error) {
	embedded, err := fs.Sub(baselineFS, "baseline")
	if err != nil {
		return nil, err
	}
	pack, err := threatrules.LoadPackFS(embedded)
	if err != nil {
		return nil, fmt.Errorf("baseline: %w", err)
	}
	if len(pack.Skipped) > 0 {
		return nil, fmt.Errorf("baseline: %s", pack.Skipped[0])
	}
	return pack, nil
})

// Baseline returns the embedded frozen baseline rules, validated against the definitions
// embedded with them. The rules are shared by every caller and must not be changed.
func Baseline() ([]*threatrules.Rule, error) {
	pack, err := baseline()
	if err != nil {
		return nil, err
	}
	return slices.Clone(pack.Rules), nil
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
// Every returned rule has passed validation (via threatrules.LoadPackDir / Baseline). A
// rule that needs a newer spec than this engine supports fails the load; LoadActiveSkipping
// is the variant that sets it aside instead.
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

// LoadActiveSkipping is LoadActive, except that store or override files declaring a spec
// newer than threatrules.SupportedSpec are left out and returned in skipped, with the
// rules that call their predicates, so the rest of the corpus still runs. Callers surface
// skipped as warnings. The embedded baseline is never skipped: it ships with the engine
// that evaluates it.
func LoadActiveSkipping(storeDir, rulesDir string) ([]LoadedRule, []threatrules.SkippedRule, error) {
	if rulesDir != "" {
		pack, err := threatrules.LoadPackDir(rulesDir)
		if err != nil {
			return nil, nil, err
		}
		return tag(pack.Rules, SourceStore), pack.Skipped, nil
	}

	base, err := Baseline()
	if err != nil {
		return nil, nil, err
	}
	stored, err := loadStore(storeDir)
	if err != nil {
		return nil, nil, err
	}
	active := tag(stored.Rules, SourceStore)
	defined := make(map[string]bool, len(stored.Rules))
	for _, r := range stored.Rules {
		defined[r.ID] = true
	}
	for _, r := range base {
		if !defined[r.ID] {
			active = append(active, LoadedRule{Rule: r, Source: SourceBaseline})
		}
	}
	return active, stored.Skipped, nil
}

// loadStore loads the store as a pack; an absent store is an empty one.
func loadStore(storeDir string) (*threatrules.Pack, error) {
	if _, err := os.Stat(storeDir); errors.Is(err, fs.ErrNotExist) {
		return &threatrules.Pack{}, nil
	}
	return threatrules.LoadPackDir(storeDir)
}

// IsBaselineID reports whether id names a rule in the embedded baseline.
func IsBaselineID(id string) bool {
	pack, err := baseline()
	if err != nil {
		return false
	}
	return slices.ContainsFunc(pack.Rules, func(r *threatrules.Rule) bool { return r.ID == id })
}

func tag(rules []*threatrules.Rule, src Source) []LoadedRule {
	out := make([]LoadedRule, len(rules))
	for i, r := range rules {
		out[i] = LoadedRule{Rule: r, Source: src}
	}
	return out
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

// pending is a file an install writes, once everything is validated.
type pending struct {
	id          string
	file        string // its path in the store
	data        []byte
	definitions bool
}

// installFiles implements InstallFiles. When skipped is non-nil, rules and definitions
// needing a newer spec are appended to it rather than aborting the install.
func installFiles(storeDir, src string, force bool, skipped *[]threatrules.SkippedRule) ([]Installed, error) {
	files, err := packFilesAt(src)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no rule or definitions files (*%s, *%s, *%s) found at %s",
			threatrules.RuleFileSuffix, threatrules.CatalogFileSuffix, threatrules.PredicatesFileSuffix, src)
	}

	store, err := EnsureStore(storeDir)
	if err != nil {
		return nil, err
	}
	held, err := readStore(store)
	if err != nil {
		return nil, err
	}

	// Validate everything first; collect the bytes + target names. Abort on any failure
	// so a partial/invalid install never lands.
	var todo []pending

	// The definitions the store will hold: its own, with the source's laid over them. A source
	// file that needs a newer spec is not laid over: the store keeps its version, which is what
	// the rules are validated against. It is still given to the library, which sets it aside and
	// with it the rules that call a predicate only it defines. Two source files that declare the
	// same definitions are both given to it, which refuses them.
	replaced := map[string]bool{}
	var definitions []threatrules.DefinitionFile
	for _, f := range files {
		if !f.Kind.Definitions() {
			continue
		}
		file := threatrules.DefinitionFile{Name: f.Path, Data: f.Data}
		head, err := file.Head()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Path, err)
		}
		name, err := head.StoreName()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Path, err)
		}
		definitions = append(definitions, file)
		if unsupported := head.Unsupported(); unsupported != nil {
			if skipped == nil {
				return nil, fmt.Errorf("%s: %w", f.Path, unsupported)
			}
			*skipped = append(*skipped, threatrules.SkippedRule{Path: f.Path, Err: unsupported})
			continue
		}
		dest := name
		if old, ok := held.definitions[name]; ok {
			replaced[name] = true
			dest = old.Name
			if bytes.Equal(old.Data, f.Data) {
				continue // already installed as it is
			}
			if !force {
				return nil, fmt.Errorf("definitions %q are already installed and differ (use --force to overwrite)", name)
			}
		}
		todo = append(todo, pending{id: head.Name, file: dest, data: f.Data, definitions: true})
	}
	for name, file := range held.definitions {
		if !replaced[name] {
			definitions = append(definitions, file)
		}
	}
	// The files it sets aside are the source's reported above, and the store's own, which every
	// load sets aside the same way.
	lib, _, err := threatrules.NewLibrary(definitions)
	if err != nil {
		return nil, fmt.Errorf("definitions: %w", err)
	}
	for _, res := range lib.Check() {
		if !res.OK() {
			return nil, fmt.Errorf("definitions: %s", res.String())
		}
	}
	changesDefinitions := slices.ContainsFunc(todo, func(p pending) bool { return p.definitions })

	staged := map[string]bool{}
	for _, f := range files {
		if f.Kind != threatrules.RuleFile {
			continue
		}
		rule, err := lib.DecodeRule(f.Data)
		if err == nil {
			_, err = checkRule(rule)
		}
		if err != nil {
			// A rule that needs a newer spec, or calls a predicate set aside for one, is set
			// aside with it.
			if unsupported, ok := threatrules.AsUnsupportedSpec(err); ok && skipped != nil {
				*skipped = append(*skipped, threatrules.SkippedRule{Path: f.Path, Err: unsupported})
				continue
			}
			return nil, fmt.Errorf("%s: %w", f.Path, err)
		}
		// rule.ID passed CheckRule (id-grammar) above; re-assert it explicitly so the
		// store path join below can never be driven outside the store by rule content.
		if !threatrules.ValidID(rule.ID) {
			return nil, fmt.Errorf("%s: invalid rule id %q", f.Path, rule.ID)
		}
		existing, installedAlready := held.rules[rule.ID]
		if (installedAlready || staged[rule.ID]) && !force {
			return nil, fmt.Errorf("rule %q already installed (use --force to overwrite)", rule.ID)
		}
		staged[rule.ID] = true
		dest := rule.ID + threatrules.RuleFileSuffix
		if installedAlready {
			dest = existing
		}
		todo = append(todo, pending{id: rule.ID, file: dest, data: f.Data})
	}

	// New definitions change what the rules already in the store mean. Each one this install
	// does not replace must still be a valid rule under them.
	if changesDefinitions {
		if err := checkStoreRules(lib, store, held, staged); err != nil {
			return nil, fmt.Errorf("these definitions would break %w (install the rules they belong to with them, or remove the rule)", err)
		}
	}
	return write(store, todo)
}

// checkRule runs a rule's fixtures, and fails it when one does not give its verdict.
func checkRule(rule *threatrules.Rule) ([]threatrules.FixtureResult, error) {
	results, err := threatrules.CheckRule(rule)
	if err != nil {
		return nil, err
	}
	for _, res := range results {
		if !res.OK() {
			return nil, fmt.Errorf("fixture %s", res.String())
		}
	}
	return results, nil
}

// checkStoreRules requires every rule the store holds, but the ones in except, to validate with
// lib's definitions. A rule that needs a newer spec than this engine supports is set aside on
// every load, and is not asked.
func checkStoreRules(lib *threatrules.Library, store string, held storeIndex, except map[string]bool) error {
	for id, file := range held.rules {
		if except[id] {
			continue
		}
		_, err := lib.LoadRule(filepath.Join(store, filepath.FromSlash(file)))
		if _, tooNew := threatrules.AsUnsupportedSpec(err); err != nil && !tooNew {
			return fmt.Errorf("the installed rule %q: %w", id, err)
		}
	}
	return nil
}

// write moves each pending file into the store, through a temporary directory inside it, and
// puts back every file it replaced when one cannot be moved.
func write(store string, todo []pending) ([]Installed, error) {
	tmpDir, err := os.MkdirTemp(store, ".install-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	for i, p := range todo {
		if err := os.WriteFile(filepath.Join(tmpDir, fmt.Sprint(i)), p.data, 0o644); err != nil {
			return nil, err
		}
	}

	installed := make([]Installed, 0, len(todo))
	for i, p := range todo {
		dest := filepath.Join(store, filepath.FromSlash(p.file))
		backupPath := filepath.Join(tmpDir, fmt.Sprint(i)+".backup")
		moved, err := moveExistingAside(dest, backupPath)
		if err != nil {
			rollbackInstall(installed, tmpDir)
			return nil, err
		}
		if err := os.Rename(filepath.Join(tmpDir, fmt.Sprint(i)), dest); err != nil {
			if moved {
				_ = os.Rename(backupPath, dest)
			}
			rollbackInstall(installed, tmpDir)
			return nil, err
		}
		installed = append(installed, Installed{ID: p.id, Path: dest, Definitions: p.definitions})
	}
	return installed, nil
}

// moveExistingAside moves the file at dest, if there is one, to backupPath, and reports whether
// it did.
func moveExistingAside(dest, backupPath string) (bool, error) {
	info, err := os.Lstat(dest)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if info.IsDir() {
		return false, fmt.Errorf("%s exists and is a directory", dest)
	}
	if err := os.Rename(dest, backupPath); err != nil {
		return false, err
	}
	return true, nil
}

// rollbackInstall removes the files installed so far, the i-th of which was the i-th pending
// file, and puts back each one it replaced.
func rollbackInstall(installed []Installed, tmpDir string) {
	for i := len(installed) - 1; i >= 0; i-- {
		backupPath := filepath.Join(tmpDir, fmt.Sprint(i)+".backup")
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
	held, err := readStore(storeDir)
	if err != nil {
		return "", err
	}
	file, ok := held.rules[id]
	if !ok {
		return "", fmt.Errorf("rule %q is not installed in the store", id)
	}
	target := filepath.Join(storeDir, filepath.FromSlash(file))
	if err := os.Remove(target); err != nil {
		return "", err
	}
	return target, nil
}

// packFilesAt returns the pack files at src, which may be a single file of any kind or a
// directory, each named by its path (src joined with its path within the directory).
func packFilesAt(src string) ([]threatrules.PackFile, error) {
	info, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		kind, ok := threatrules.PackFileKind(filepath.Base(src))
		if !ok {
			return nil, fmt.Errorf("%s is not a rule or definitions file (*%s, *%s, *%s)",
				src, threatrules.RuleFileSuffix, threatrules.CatalogFileSuffix, threatrules.PredicatesFileSuffix)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return nil, err
		}
		return []threatrules.PackFile{{Path: src, Kind: kind, Data: data}}, nil
	}
	files, err := threatrules.ReadPackFS(os.DirFS(src))
	if err != nil {
		return nil, err
	}
	for i := range files {
		files[i].Path = filepath.Join(src, filepath.FromSlash(files[i].Path))
	}
	return files, nil
}

// storeIndex is what a store holds, read as a load reads it: its rules by id, and its
// definitions by the name a store keeps them under (threatrules.Head.StoreName), each with its
// path in the store.
type storeIndex struct {
	rules map[string]string
	// definitions are named by their paths in the store.
	definitions map[string]threatrules.DefinitionFile
}

// readStore indexes the store. An absent store holds nothing. A file whose head does not read
// is indexed under its file name.
func readStore(store string) (storeIndex, error) {
	held := storeIndex{rules: map[string]string{}, definitions: map[string]threatrules.DefinitionFile{}}
	files, err := threatrules.ReadPackFS(os.DirFS(store))
	if errors.Is(err, fs.ErrNotExist) {
		return held, nil
	}
	if err != nil {
		return held, err
	}
	for _, f := range files {
		head, _ := threatrules.ReadHead(f.Path, f.Data)
		if f.Kind == threatrules.RuleFile {
			id := head.Name
			if !threatrules.ValidID(id) {
				id = strings.TrimSuffix(path.Base(f.Path), threatrules.RuleFileSuffix)
			}
			held.rules[id] = f.Path
			continue
		}
		name, err := head.StoreName()
		if err != nil {
			name = path.Base(f.Path)
		}
		held.definitions[name] = threatrules.DefinitionFile{Name: f.Path, Data: f.Data}
	}
	return held, nil
}
