package threatrules

import (
	"bytes"
	"errors"
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
//
// The spec version is checked first, from a lenient read of just id and spec, so a rule
// written for a newer spec returns an *UnsupportedSpecError naming the version it needs
// rather than an unknown-field error about whatever that version added.
func DecodeRule(data []byte) (*Rule, error) {
	var head struct {
		ID   string `yaml:"id"`
		Spec string `yaml:"spec"`
	}
	if yaml.Unmarshal(data, &head) == nil {
		if err := checkSpecSupported(head.ID, head.Spec); err != nil {
			return nil, err
		}
	}
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

// SkippedRule is a rule file a loader left out because it needs a newer spec than this
// engine supports.
type SkippedRule struct {
	Path string
	Err  *UnsupportedSpecError
}

func (s SkippedRule) String() string { return s.Path + ": " + s.Err.Error() }

// LoadDir discovers every *.rule.yaml under root (recursively), decodes and validates
// each, and returns them sorted by id. A duplicate id across files is a hard error, as is
// any decode/validate failure (the offending path is named), including a rule that needs
// a newer spec. LoadDirSkipping is the variant that sets such rules aside instead.
func LoadDir(root string) ([]*Rule, error) {
	rules, skipped, err := LoadDirSkipping(root)
	if err != nil {
		return nil, err
	}
	if len(skipped) > 0 {
		return nil, fmt.Errorf("%s: %w", skipped[0].Path, skipped[0].Err)
	}
	return rules, nil
}

// LoadDirSkipping is LoadDir, except that a rule declaring a spec newer than
// SupportedSpec is left out and returned in skipped rather than failing the load, so an
// older engine keeps running the rest of a newer corpus. Every other failure still fails
// the whole load.
func LoadDirSkipping(root string) (rules []*Rule, skipped []SkippedRule, err error) {
	var paths []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
		return nil, nil, err
	}
	sort.Strings(paths)

	rules = make([]*Rule, 0, len(paths))
	seen := make(map[string]string, len(paths)) // id -> path
	for _, path := range paths {
		rule, err := LoadRule(path)
		if unsupported, ok := AsUnsupportedSpec(err); ok {
			skipped = append(skipped, SkippedRule{Path: path, Err: unsupported})
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if prev, dup := seen[rule.ID]; dup {
			return nil, nil, fmt.Errorf("duplicate rule id %q in %s and %s", rule.ID, prev, path)
		}
		seen[rule.ID] = path
		rules = append(rules, rule)
	}
	return rules, skipped, nil
}

// AsUnsupportedSpec reports whether err is, or wraps, an *UnsupportedSpecError.
func AsUnsupportedSpec(err error) (*UnsupportedSpecError, bool) {
	var u *UnsupportedSpecError
	if errors.As(err, &u) {
		return u, true
	}
	return nil, false
}

func isRuleFile(path string) bool {
	return strings.HasSuffix(path, ruleFileSuffix) && !strings.HasPrefix(filepath.Base(path), "._")
}
