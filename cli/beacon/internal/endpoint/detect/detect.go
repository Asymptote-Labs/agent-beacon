// Package detect wires the open threat-rules engine to the local Beacon endpoint: it
// resolves the runtime rules store (~/.beacon/endpoint/rules) from endpoint config and
// loads the active rule set for `beacon scan`.
//
// The shared store mechanics (embedded baseline, load/install/remove) live in
// pkg/asymptoteobserve/rulestore so they can be reused across modules. This package is a
// thin cli/beacon adapter that resolves the store directory from endpoint config and
// delegates, preserving its existing (userMode bool) API for the CLI, scan, and dashboard.
package detect

import (
	"path/filepath"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/rulestore"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
)

// Source identifies where an active rule came from.
type Source = rulestore.Source

const (
	SourceBaseline = rulestore.SourceBaseline
	SourceStore    = rulestore.SourceStore
)

// LoadedRule is a validated rule paired with its origin.
type LoadedRule = rulestore.LoadedRule

// Installed reports a rule written into the store.
type Installed = rulestore.Installed

// StoreDir returns the local rules store directory for the given mode.
func StoreDir(userMode bool) string {
	return filepath.Join(endpointconfig.BaseDir(userMode), "rules")
}

// EnsureStore creates the rules store directory if needed and returns its path.
func EnsureStore(userMode bool) (string, error) {
	return rulestore.EnsureStore(StoreDir(userMode))
}

// Baseline returns the embedded frozen baseline rules, validated.
func Baseline() ([]*threatrules.Rule, error) {
	return rulestore.Baseline()
}

// LoadActive resolves the active rule set for the given mode. See rulestore.LoadActive.
func LoadActive(userMode bool, rulesDir string) ([]LoadedRule, error) {
	return rulestore.LoadActive(StoreDir(userMode), rulesDir)
}

// InstallFiles validates and installs the *.rule.yaml files at src into the store.
func InstallFiles(userMode bool, src string, force bool) ([]Installed, error) {
	return rulestore.InstallFiles(StoreDir(userMode), src, force)
}

// Remove deletes a rule by id from the store and returns the removed path.
func Remove(userMode bool, id string) (string, error) {
	return rulestore.Remove(StoreDir(userMode), id)
}
