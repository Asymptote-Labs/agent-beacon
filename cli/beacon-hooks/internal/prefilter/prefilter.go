// Package prefilter is tier 1 of the policy hook: a local pattern match that
// decides, in about a millisecond, whether a tool call touches a secret source
// at all. Only calls it routes are sent to the cloud judge, which decides
// whether executing them would actually expose a value.
//
// Routing is deliberately broad. A match is not a verdict: `grep -l TOKEN .env`
// is routed and then allowed by the judge. What must never happen is a
// read-out that is not routed, so the rules name secret sources (credential
// files, secret-manager CLIs, environment dumps, secret literals) rather than
// guessing at intent.
//
// Rules ship embedded (rules.json) and can be replaced without a rebuild by a
// file at ~/.beacon/endpoint/policy/prefilter.json with the same shape.
package prefilter

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/secretscan"
)

//go:embed rules.json
var embeddedRules []byte

// OverrideEnv names a rules file that replaces the embedded set (tests, tuning).
const OverrideEnv = "BEACON_POLICY_PREFILTER"

// Hit is one rule that routed the call.
type Hit struct {
	RuleID   string `json:"rule_id"`
	Category string `json:"category"`
}

type ruleFile struct {
	Version      string     `json:"version"`
	Rules        []ruleSpec `json:"rules"`
	ExcludePaths []string   `json:"exclude_paths"`
}

type ruleSpec struct {
	ID       string   `json:"id"`
	Category string   `json:"category"`
	Tools    []string `json:"tools"`
	Field    string   `json:"field"`
	Pattern  string   `json:"pattern"`
}

type rule struct {
	ruleSpec
	re *regexp.Regexp
}

// Set is a compiled rule set.
type Set struct {
	Version  string
	Hash     string
	Source   string
	rules    []rule
	excludes []*regexp.Regexp
}

// Load returns the override rule set when one is present and valid, and the
// embedded set otherwise. A broken override never disables the prefilter.
func Load() *Set {
	for _, path := range overridePaths() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if set, err := compile(data, path); err == nil {
			return set
		}
	}
	set, err := compile(embeddedRules, "embedded")
	if err != nil {
		panic("prefilter: embedded rules do not compile: " + err.Error())
	}
	return set
}

func overridePaths() []string {
	var paths []string
	if p := strings.TrimSpace(os.Getenv(OverrideEnv)); p != "" {
		paths = append(paths, p)
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".beacon", "endpoint", "policy", "prefilter.json"))
	}
	return paths
}

func compile(data []byte, source string) (*Set, error) {
	var file ruleFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	set := &Set{Version: file.Version, Hash: hex.EncodeToString(sum[:])[:12], Source: source}
	for _, spec := range file.Rules {
		re, err := regexp.Compile(spec.Pattern)
		if err != nil {
			return nil, err
		}
		set.rules = append(set.rules, rule{ruleSpec: spec, re: re})
	}
	for _, pattern := range file.ExcludePaths {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}
		set.excludes = append(set.excludes, re)
	}
	return set, nil
}

// Match returns the rules that route this tool call, in rule order, one hit per
// rule id. Tools the prefilter does not cover return nothing.
func (s *Set) Match(toolName string, input map[string]interface{}) []Hit {
	var hits []Hit
	seen := map[string]bool{}
	add := func(id, category string) {
		if !seen[id] {
			seen[id] = true
			hits = append(hits, Hit{RuleID: id, Category: category})
		}
	}

	command := str(input, "command")
	paths := pathsFor(toolName, input)
	for _, r := range s.rules {
		if !appliesTo(r.Tools, toolName) {
			continue
		}
		var subjects []string
		if r.Field == "path" {
			subjects = paths
		} else {
			subjects = []string{command}
		}
		for _, subject := range subjects {
			if subject == "" {
				continue
			}
			if r.Category == "credential-file" {
				subject = s.stripExcluded(subject)
			}
			if r.re.MatchString(subject) {
				add(r.ID, r.Category)
				break
			}
		}
	}

	// Secret literals anywhere the agent is about to send data: a command line,
	// a fetched URL, or an MCP tool's arguments.
	var scanText string
	switch {
	case toolName == "Bash":
		scanText = command
	case toolName == "WebFetch":
		scanText = str(input, "url")
	case strings.HasPrefix(toolName, "mcp__"):
		if raw, err := json.Marshal(input); err == nil {
			scanText = string(raw)
		}
	}
	for _, f := range secretscan.Scan(scanText) {
		add("secret.shape."+f.Detector, "secret-shape")
	}
	return hits
}

func (s *Set) stripExcluded(text string) string {
	for _, re := range s.excludes {
		text = re.ReplaceAllStringFunc(text, func(m string) string { return strings.ReplaceAll(m, ".", "_") })
	}
	return text
}

func pathsFor(toolName string, input map[string]interface{}) []string {
	switch toolName {
	case "Read":
		return []string{str(input, "file_path")}
	case "Grep":
		return []string{str(input, "path"), str(input, "glob")}
	case "Glob":
		pattern := str(input, "pattern")
		if base := str(input, "path"); base != "" && pattern != "" {
			return []string{pattern, strings.TrimRight(base, "/") + "/" + pattern}
		}
		return []string{pattern}
	}
	return nil
}

func appliesTo(tools []string, toolName string) bool {
	for _, t := range tools {
		if t == toolName {
			return true
		}
	}
	return false
}

func str(input map[string]interface{}, key string) string {
	if input == nil {
		return ""
	}
	if v, ok := input[key].(string); ok {
		return v
	}
	return ""
}

// IDs returns the rule ids of hits.
func IDs(hits []Hit) []string {
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.RuleID)
	}
	return ids
}
