package threatrules

import (
	"bytes"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// DecodeRule strictly decodes a single rule document. Unknown fields are rejected so a
// typo in a rule key is a load-time error rather than a silently ignored field. It does
// not Validate; callers run Validate (or Compile) separately.
//
// The spec version is checked first, from the rule's head (ReadHead), so a rule written for
// a newer spec returns an *UnsupportedSpecError naming the version it needs rather than an
// unknown-field error about whatever that version added.
func DecodeRule(data []byte) (*Rule, error) {
	if head, err := readHead(RuleFile, data); err == nil {
		if err := checkSpecSupported(head.Name, head.Spec); err != nil {
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

// LoadRule reads, decodes, and validates a single rule file on its own: without the
// definitions of a pack, so a rule that calls a predicate does not validate. LoadPackDir loads a
// pack's rules with its definitions, and Library.LoadRule one rule with a library.
func LoadRule(path string) (*Rule, error) {
	return (*Library)(nil).LoadRule(path)
}

// SkippedRule is a file a loader left out because it needs a newer spec than this engine
// supports: a rule, a rule that calls a predicate of definitions left out, or a definitions
// file (Err.Definitions is set for one).
type SkippedRule struct {
	Path string
	Err  *UnsupportedSpecError
}

func (s SkippedRule) String() string { return s.Path + ": " + s.Err.Error() }

// LoadDir loads the pack under root (LoadPackDir) and returns its rules, in the order of their
// paths. A rule that fails to load fails it, naming the file, and so do a duplicate id and a
// file that needs a newer spec than this engine supports.
func LoadDir(root string) ([]*Rule, error) {
	pack, err := LoadPackDir(root)
	if err != nil {
		return nil, err
	}
	if len(pack.Skipped) > 0 {
		return nil, fmt.Errorf("%s: %w", pack.Skipped[0].Path, pack.Skipped[0].Err)
	}
	return pack.Rules, nil
}

// AsUnsupportedSpec reports whether err is, or wraps, an *UnsupportedSpecError.
func AsUnsupportedSpec(err error) (*UnsupportedSpecError, bool) {
	var u *UnsupportedSpecError
	if errors.As(err, &u) {
		return u, true
	}
	return nil, false
}
