// Package detect wires the open threat-rules engine to the local Beacon endpoint: it
// manages a runtime rules store (~/.beacon/endpoint/rules), ships a small frozen baseline
// embedded in the binary, and loads the active rule set for `beacon scan`.
//
// The engine ships in the binary; the rule corpus is external data. Only the frozen
// baseline below is embedded, so a growing corpus loaded into the store never enlarges
// the binary.
package detect

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
)

//go:embed baseline/*.rule.yaml
var baselineFS embed.FS

const ruleFileSuffix = ".rule.yaml"

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

// StoreDir returns the local rules store directory for the given mode.
func StoreDir(userMode bool) string {
	return filepath.Join(endpointconfig.BaseDir(userMode), "rules")
}

// EnsureStore creates the rules store directory if needed and returns its path.
func EnsureStore(userMode bool) (string, error) {
	dir := StoreDir(userMode)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create rules store: %w", err)
	}
	return dir, nil
}

// Baseline returns the embedded frozen baseline rules, validated.
func Baseline() ([]*threatrules.Rule, error) {
	entries, err := baselineFS.ReadDir("baseline")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ruleFileSuffix) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	rules := make([]*threatrules.Rule, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		data, err := baselineFS.ReadFile(filepath.Join("baseline", name))
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

// LoadActive resolves the active rule set:
//
//   - if rulesDir is non-empty, load only from that directory (explicit override);
//   - else load from the store; if the store is empty (or absent), fall back to the
//     embedded baseline.
//
// Every returned rule has passed validation (via threatrules.LoadDir / Baseline).
func LoadActive(userMode bool, rulesDir string) ([]LoadedRule, error) {
	if rulesDir != "" {
		rules, err := threatrules.LoadDir(rulesDir)
		if err != nil {
			return nil, err
		}
		return tag(rules, SourceStore), nil
	}

	store := StoreDir(userMode)
	if hasRuleFiles(store) {
		rules, err := threatrules.LoadDir(store)
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

func hasRuleFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ruleFileSuffix) {
			return true
		}
	}
	return false
}

// Installed reports a rule written into the store.
type Installed struct {
	ID   string
	Path string
}

// InstallFiles validates every *.rule.yaml found at src (a file or directory) and writes
// the valid ones into the store. Each rule is validated before any file is written; an
// invalid rule aborts the whole install. A rule whose id already exists in the store is
// rejected unless force is set. Returns the rules installed.
func InstallFiles(userMode bool, src string, force bool) ([]Installed, error) {
	srcPaths, err := ruleFilesAt(src)
	if err != nil {
		return nil, err
	}
	if len(srcPaths) == 0 {
		return nil, fmt.Errorf("no %s files found at %s", ruleFileSuffix, src)
	}

	store, err := EnsureStore(userMode)
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
		if _, err := threatrules.CheckRule(rule); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if (existing[rule.ID] || staged[rule.ID]) && !force {
			return nil, fmt.Errorf("rule %q already installed (use --force to overwrite)", rule.ID)
		}
		staged[rule.ID] = true
		todo = append(todo, pending{id: rule.ID, dest: filepath.Join(store, rule.ID+ruleFileSuffix), data: data})
	}

	installed := make([]Installed, 0, len(todo))
	for _, p := range todo {
		if err := os.WriteFile(p.dest, p.data, 0o644); err != nil {
			return nil, err
		}
		installed = append(installed, Installed{ID: p.id, Path: p.dest})
	}
	return installed, nil
}

// Remove deletes a rule by id from the store. Returns the removed path.
func Remove(userMode bool, id string) (string, error) {
	path := filepath.Join(StoreDir(userMode), id+ruleFileSuffix)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("rule %q is not installed in the store", id)
		}
		return "", err
	}
	return path, nil
}

// ruleFilesAt returns the *.rule.yaml files at src, which may be a single file or a dir.
func ruleFilesAt(src string) ([]string, error) {
	info, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		if !strings.HasSuffix(src, ruleFileSuffix) {
			return nil, fmt.Errorf("%s is not a %s file", src, ruleFileSuffix)
		}
		return []string{src}, nil
	}
	var paths []string
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ruleFileSuffix) {
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
		if e.IsDir() || !strings.HasSuffix(e.Name(), ruleFileSuffix) {
			continue
		}
		ids[strings.TrimSuffix(e.Name(), ruleFileSuffix)] = true
	}
	return ids, nil
}
