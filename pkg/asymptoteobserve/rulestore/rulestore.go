// Package rulestore manages the open threat-rules corpus shared across Beacon
// components: it ships a small frozen baseline embedded in the binary and loads,
// installs, and removes rules in a local store directory.
//
// The engine ships in the binary; the rule corpus is external data. Only the
// frozen baseline below is embedded, so a growing corpus loaded into the store
// never enlarges the binary.
//
// Unlike the cli/beacon `detect` adapter that wraps it, this package takes an
// explicit store directory rather than resolving one from endpoint config, so it
// can be imported from any module (beacon-hooks, an external policy provider, the
// public CLI) without depending on cli/beacon internals.
package rulestore

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
)

//go:embed baseline/*.rule.yaml
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

// Installed reports a rule written into the store.
type Installed struct {
	ID   string
	Path string
}

// EnsureStore creates the store directory if needed and returns its path.
func EnsureStore(storeDir string) (string, error) {
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return "", fmt.Errorf("create rules store: %w", err)
	}
	return storeDir, nil
}

// Baseline returns the embedded frozen baseline rules, validated.
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

	rules := make([]*threatrules.Rule, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		data, err := baselineFS.ReadFile(baselineRulePath(name))
		if err != nil {
			return nil, err
		}
		rule, err := threatrules.DecodeRule(data)
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
//   - else load from storeDir; if the store is empty (or absent), fall back to the
//     embedded baseline.
//
// Every returned rule has passed validation (via threatrules.LoadDir / Baseline).
func LoadActive(storeDir, rulesDir string) ([]LoadedRule, error) {
	if rulesDir != "" {
		rules, err := threatrules.LoadDir(rulesDir)
		if err != nil {
			return nil, err
		}
		return tag(rules, SourceStore), nil
	}

	if HasRuleFiles(storeDir) {
		rules, err := threatrules.LoadDir(storeDir)
		if err != nil {
			return nil, err
		}
		return tag(rules, SourceStore), nil
	}

	base, err := Baseline()
	if err != nil {
		return nil, err
	}
	return tag(base, SourceBaseline), nil
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

// InstallFiles validates every *.rule.yaml found at src (a file or directory) and
// writes the valid ones into storeDir. Each rule is validated before any file is
// written; an invalid rule aborts the whole install. A rule whose id already exists
// in the store is rejected unless force is set. Returns the rules installed.
func InstallFiles(storeDir, src string, force bool) ([]Installed, error) {
	srcPaths, err := ruleFilesAt(src)
	if err != nil {
		return nil, err
	}
	if len(srcPaths) == 0 {
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
		id   string
		dest string
		data []byte
	}
	var todo []pending
	staged := map[string]bool{}
	for _, p := range srcPaths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		rule, err := threatrules.DecodeRule(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		results, err := threatrules.CheckRule(rule)
		if err != nil {
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
		todo = append(todo, pending{id: rule.ID, dest: filepath.Join(store, rule.ID+RuleFileSuffix), data: data})
	}

	tmpDir, err := os.MkdirTemp(store, ".install-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	for _, p := range todo {
		tmpPath := filepath.Join(tmpDir, p.id+RuleFileSuffix)
		if err := os.WriteFile(tmpPath, p.data, 0o644); err != nil {
			return nil, err
		}
	}

	installed := make([]Installed, 0, len(todo))
	for _, p := range todo {
		tmpPath := filepath.Join(tmpDir, p.id+RuleFileSuffix)
		backupPath, err := moveExistingRuleAside(p.dest, tmpDir, p.id)
		if err != nil {
			rollbackInstall(installed, tmpDir)
			return nil, err
		}
		if err := os.Rename(tmpPath, p.dest); err != nil {
			if backupPath != "" {
				_ = os.Rename(backupPath, p.dest)
			}
			rollbackInstall(installed, tmpDir)
			return nil, err
		}
		installed = append(installed, Installed{ID: p.id, Path: p.dest})
	}
	return installed, nil
}

func moveExistingRuleAside(dest, tmpDir, id string) (string, error) {
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
	backupPath := filepath.Join(tmpDir, id+".backup")
	if err := os.Rename(dest, backupPath); err != nil {
		return "", err
	}
	return backupPath, nil
}

func rollbackInstall(installed []Installed, tmpDir string) {
	for i := len(installed) - 1; i >= 0; i-- {
		backupPath := filepath.Join(tmpDir, installed[i].ID+".backup")
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

// ruleFilesAt returns the *.rule.yaml files at src, which may be a single file or a dir.
func ruleFilesAt(src string) ([]string, error) {
	info, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		if !isRuleFile(src) {
			return nil, fmt.Errorf("%s is not a %s file", src, RuleFileSuffix)
		}
		return []string{src}, nil
	}
	var paths []string
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && isRuleFile(path) {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
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
