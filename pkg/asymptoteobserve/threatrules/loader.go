package threatrules

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ruleFileSuffix is the extension threat-rule files use.
const ruleFileSuffix = ".rule.yaml"

// DecodeRule strictly decodes a single rule document. Unknown fields are rejected so a
// typo in a rule key is a load-time error rather than a silently ignored field. It does
// not Validate; callers run Validate (or Compile) separately.
func DecodeRule(data []byte) (*Rule, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var rule Rule
	if err := dec.Decode(&rule); err != nil {
		return nil, err
	}
	return &rule, nil
}

// LoadRule reads, decodes, and validates a single rule file.
func LoadRule(path string) (*Rule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	rule, err := DecodeRule(data)
	if err != nil {
		return nil, fmt.Errorf("%s: decode: %w", path, err)
	}
	if err := rule.Validate(); err != nil {
		return nil, fmt.Errorf("%s: validate: %w", path, err)
	}
	return rule, nil
}

// LoadDir discovers every *.rule.yaml under root (recursively), decodes and validates
// each, and returns them sorted by id. A duplicate id across files is a hard error, as is
// any decode/validate failure (the offending path is named).
func LoadDir(root string) ([]*Rule, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if isRuleFile(path) {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	rules := make([]*Rule, 0, len(paths))
	seen := make(map[string]string, len(paths)) // id -> path
	for _, path := range paths {
		rule, err := LoadRule(path)
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[rule.ID]; dup {
			return nil, fmt.Errorf("duplicate rule id %q in %s and %s", rule.ID, prev, path)
		}
		seen[rule.ID] = path
		rules = append(rules, rule)
	}
	return rules, nil
}

func isRuleFile(path string) bool {
	return strings.HasSuffix(path, ruleFileSuffix) && !strings.HasPrefix(filepath.Base(path), "._")
}
