package asymptoteobserve

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// A lens is a single self-contained HTML file that renders one trace, run by the local dashboard in
// a sandboxed frame. The frame cannot reach the network or the dashboard; everything it knows
// arrives once, as a LensDataV1, through window.beacon.getTrace(). The full contract is
// spec/lenses/SPEC.md. The types here are that contract's Go side, shared by the dashboard that
// builds the data and the CLI that installs and lints lenses.
const (
	// LensSpecVersion names the spec revision in spec/lenses/VERSION.
	LensSpecVersion = "lenses/v1"
	// LensAPIVersion is the data contract a lens declares and the dashboard serves. A lens whose
	// manifest names another value is not run.
	LensAPIVersion = "beacon.lens.v1"
	// LensMaxBytes caps one lens file. Everything a lens uses is inline, so this bounds what the
	// dashboard serves and what the store keeps.
	LensMaxBytes = 16 << 20
	// LensDataTimeout is how long getTrace() may take before it rejects. The host falls back to the
	// full trace view when a lens has not rendered by then.
	LensDataTimeout = 10 * time.Second
	// LensManifestScriptType is the type of the one <script> element that carries a lens's
	// manifest. Browsers do not execute a script of an unknown type, so the manifest is inert data.
	LensManifestScriptType = "application/beacon-lens+json"
)

// LensManifestV1 describes a lens. It lives inside the lens file, so the file is the whole unit of
// installation, review, and sharing.
type LensManifestV1 struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
	Version     int    `json:"version"`
	API         string `json:"api"`
	Author      string `json:"author,omitempty"`
}

// LensDataV1 is everything a lens receives. It is computed once per frame load, from the same local
// log the dashboard's trace view reads, and never refreshed.
type LensDataV1 struct {
	APIVersion string `json:"api_version"`
	// Trace is the existing versioned trace bundle (beacon.trace.v1), so a lens reads the same
	// schema as every other trace consumer rather than a lens-only projection.
	Trace TraceBundleV1 `json:"trace"`
	// Findings is nil when the trace was not scanned (the rule engine failed or no rules loaded),
	// which a lens must tell apart from a scan that found nothing.
	Findings *LensFindingsV1 `json:"findings,omitempty"`
	// TokenCoverage says whether this trace's runtime can report token usage at all, so a lens can
	// distinguish "used no tokens" from "this runtime never reports them". Nil when unknown.
	TokenCoverage *LensTokenCoverageV1 `json:"token_coverage,omitempty"`
	// Truncated is true when Trace holds fewer events than the trace has, because the bundle hit
	// the size cap. Trace.Range then carries the counts.
	Truncated bool `json:"truncated"`
}

// LensFindingsV1 is the result of running the active threat rules over this trace's session.
type LensFindingsV1 struct {
	RulesEvaluated int             `json:"rules_evaluated"`
	Items          []LensFindingV1 `json:"items"`
	// Skipped names rules that were not evaluated because they need a newer spec.
	Skipped []string `json:"skipped,omitempty"`
}

// LensFindingV1 is one rule match. Evidence is referenced by trace event ID rather than copied, so
// the bundle carries each event once. An ID may not resolve when the bundle is truncated.
type LensFindingV1 struct {
	RuleID   string            `json:"rule_id"`
	Title    string            `json:"title"`
	Severity Severity          `json:"severity"`
	Posture  string            `json:"posture"`
	Reason   string            `json:"reason,omitempty"`
	Taxonomy map[string]string `json:"taxonomy,omitempty"`
	EventIDs []string          `json:"event_ids"`
}

// LensTokenCoverageV1 is this trace's runtime's line from the token-usage coverage report. Status
// and Expectation carry the same values `beacon token-usage --coverage` prints.
type LensTokenCoverageV1 struct {
	Harness     string `json:"harness"`
	Status      string `json:"status"`
	Expectation string `json:"expectation"`
	Reason      string `json:"reason,omitempty"`
}

var (
	lensIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	lensIconPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	// The manifest element is matched by its exact opening tag. The spec fixes the spelling so that
	// a reader needs no HTML parser and two readers cannot disagree about which element it is.
	lensManifestOpen  = []byte(`<script type="` + LensManifestScriptType + `">`)
	lensManifestClose = []byte(`</script>`)
)

const (
	lensTitleMaxRunes       = 60
	lensDescriptionMaxRunes = 280
	lensAuthorMaxRunes      = 100
)

// ErrLensManifestMissing reports a file with no manifest element.
var ErrLensManifestMissing = errors.New("lens has no <script type=\"" + LensManifestScriptType + "\"> manifest")

// Validate reports every way the manifest breaks the spec, joined, or nil.
func (m LensManifestV1) Validate() error {
	var problems []error
	if !lensIDPattern.MatchString(m.ID) {
		problems = append(problems, fmt.Errorf("id %q must be 1-63 lowercase letters, digits or hyphens, starting with a letter or digit", m.ID))
	}
	problems = append(problems, lensText("title", m.Title, lensTitleMaxRunes, true)...)
	problems = append(problems, lensText("description", m.Description, lensDescriptionMaxRunes, false)...)
	problems = append(problems, lensText("author", m.Author, lensAuthorMaxRunes, false)...)
	if m.Icon != "" && !lensIconPattern.MatchString(m.Icon) {
		problems = append(problems, fmt.Errorf("icon %q must be one lowercase word (letters, digits, hyphens)", m.Icon))
	}
	if m.Version < 1 {
		problems = append(problems, fmt.Errorf("version must be a positive integer"))
	}
	if m.API != LensAPIVersion {
		problems = append(problems, fmt.Errorf("api %q is not supported (want %q)", m.API, LensAPIVersion))
	}
	return errors.Join(problems...)
}

// lensText checks one display string: present when required, one line, bounded, and free of
// control characters, because the dashboard prints it in its own chrome.
func lensText(field, value string, maxRunes int, required bool) []error {
	if strings.TrimSpace(value) == "" {
		if required {
			return []error{fmt.Errorf("%s is required", field)}
		}
		return nil
	}
	var problems []error
	if !utf8.ValidString(value) {
		problems = append(problems, fmt.Errorf("%s is not valid UTF-8", field))
	}
	if utf8.RuneCountInString(value) > maxRunes {
		problems = append(problems, fmt.Errorf("%s is longer than %d characters", field, maxRunes))
	}
	if strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		problems = append(problems, fmt.Errorf("%s must be one line without control characters", field))
	}
	return problems
}

// ParseLensManifest reads the manifest out of a lens file. It requires exactly one manifest
// element and decodes it strictly: an unknown field is an error, so a misspelled key is caught when
// the lens is linted rather than silently ignored. The result is not validated; call Validate.
func ParseLensManifest(html []byte) (LensManifestV1, error) {
	start := bytes.Index(html, lensManifestOpen)
	if start < 0 {
		return LensManifestV1{}, ErrLensManifestMissing
	}
	body := html[start+len(lensManifestOpen):]
	if bytes.Contains(body, lensManifestOpen) {
		return LensManifestV1{}, errors.New("lens has more than one manifest element")
	}
	end := bytes.Index(body, lensManifestClose)
	if end < 0 {
		return LensManifestV1{}, errors.New("lens manifest element is not closed")
	}
	decoder := json.NewDecoder(bytes.NewReader(body[:end]))
	decoder.DisallowUnknownFields()
	var manifest LensManifestV1
	if err := decoder.Decode(&manifest); err != nil {
		return LensManifestV1{}, fmt.Errorf("lens manifest: %w", err)
	}
	if decoder.More() {
		return LensManifestV1{}, errors.New("lens manifest holds more than one JSON value")
	}
	return manifest, nil
}
