package threatrules

import (
	"fmt"
	"regexp"
	"strconv"
)

// SupportedSpec is the newest Threat Rules spec version this engine implements. It must
// equal spec/threat-rules/VERSION (a test pins the two together).
const SupportedSpec = "threat-rules/v1.1"

// specV1_1 is the version that added correlation.order.
var specV1_1 = specVersion{major: 1, minor: 1}

// specPattern is the grammar of a spec version: threat-rules/v<major>[.<minor>].
var specPattern = regexp.MustCompile(`^threat-rules/v([0-9]+)(?:\.([0-9]+))?$`)

// specVersion is a parsed spec version. An omitted minor is 0, so v1 == v1.0.
type specVersion struct{ major, minor int }

func (v specVersion) newerThan(o specVersion) bool {
	if v.major != o.major {
		return v.major > o.major
	}
	return v.minor > o.minor
}

// parseSpec parses a rule's spec field. An empty value is the original spec, v1.
func parseSpec(s string) (specVersion, error) {
	if s == "" {
		return specVersion{major: 1}, nil
	}
	m := specPattern.FindStringSubmatch(s)
	if m == nil {
		return specVersion{}, fmt.Errorf("spec %q must look like %q", s, SupportedSpec)
	}
	major, err := strconv.Atoi(m[1])
	if err != nil {
		return specVersion{}, fmt.Errorf("spec %q: %w", s, err)
	}
	minor := 0
	if m[2] != "" {
		if minor, err = strconv.Atoi(m[2]); err != nil {
			return specVersion{}, fmt.Errorf("spec %q: %w", s, err)
		}
	}
	return specVersion{major: major, minor: minor}, nil
}

var supportedSpec = func() specVersion {
	v, err := parseSpec(SupportedSpec)
	if err != nil {
		panic(err)
	}
	return v
}()

// UnsupportedSpecError reports a rule that declares a spec version newer than this
// engine implements. Loaders that can continue without the rule (LoadDirSkipping,
// rulestore) skip it and surface this error as a warning.
type UnsupportedSpecError struct {
	RuleID   string // from a lenient read of the file; may be empty
	Required string // the rule's spec field
}

func (e *UnsupportedSpecError) Error() string {
	id := e.RuleID
	if id == "" {
		id = "(no id)"
	}
	return fmt.Sprintf("rule %q requires %s, but this Beacon supports up to %s; upgrade Beacon to load it",
		id, e.Required, SupportedSpec)
}

// checkSpecSupported returns an *UnsupportedSpecError when spec is newer than
// SupportedSpec, and a plain error when it is malformed.
func checkSpecSupported(id, spec string) error {
	v, err := parseSpec(spec)
	if err != nil {
		return err
	}
	if v.newerThan(supportedSpec) {
		return &UnsupportedSpecError{RuleID: id, Required: spec}
	}
	return nil
}
