// Package onboarding implements Beacon's one-time, install-time account signal:
// a prompt for the operator's email and how they are using Beacon, submitted once
// to an Asymptote-run endpoint.
//
// Everything here is best-effort. Onboarding never fails an install, never runs
// outside an interactive terminal, and never runs twice on the same machine.
package onboarding

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// ProfilePath is the profile location relative to the user's home directory.
//
// This is a deliberate sibling of ~/.beacon/endpoint/ rather than a file inside it.
// lifecycle.Uninstall removes the endpoint config and every manifest-listed file, so a
// profile stored under ~/.beacon/endpoint/ would be destroyed by an uninstall and the
// user would be prompted again on their next install. The endpoint config is also
// mode-specific (--user vs --system) and rewritten wholesale by every install and
// repair, which would lose the record just as reliably.
const ProfilePath = ".beacon/profile.json"

// SchemaVersion is the profile format version. Bump it only for a breaking change;
// readers tolerate unknown fields, so additive changes do not need a bump.
const SchemaVersion = 1

// Outcome records how the one-time submission ended.
const (
	// OutcomeSubmitted means the endpoint accepted the signup.
	OutcomeSubmitted = "submitted"
	// OutcomePending means the submission failed in a way worth retrying (network
	// error, timeout, 5xx). The payload is kept so a later command can resend it.
	OutcomePending = "pending"
	// OutcomeRejected means the endpoint refused the submission (4xx, rate limit).
	// Terminal: there is nothing useful to retry.
	OutcomeRejected = "rejected"
	// OutcomeSkipped means the user opted out through a documented escape hatch.
	OutcomeSkipped = "skipped"
)

// Profile is the persisted per-machine Beacon profile.
type Profile struct {
	SchemaVersion int         `json:"schema_version"`
	InstallID     string      `json:"install_id"`
	Onboarding    Onboarding  `json:"onboarding"`
	Pending       *Submission `json:"pending_submission,omitempty"`
}

// Onboarding records the outcome of the one-time prompt.
type Onboarding struct {
	CompletedAt   string `json:"completed_at,omitempty"`
	Outcome       string `json:"outcome,omitempty"`
	Email         string `json:"email,omitempty"`
	Usage         string `json:"usage,omitempty"`
	BeaconVersion string `json:"beacon_version,omitempty"`
	// Destination records where the user chose to send this machine's telemetry:
	// local, own_infra or asymptote. Empty means the question has not been answered on
	// this machine, so a later interactive install may ask once. asymptote is written
	// only after the connect succeeds, so a failed connect is asked again.
	Destination string `json:"destination,omitempty"`
}

// Answers to the telemetry destination question.
const (
	DestinationLocal     = "local"
	DestinationOwnInfra  = "own_infra"
	DestinationAsymptote = "asymptote"
)

// Prompted reports whether this machine has already been through onboarding.
// It is the single gate that makes the prompt fire once and only once.
func (p Profile) Prompted() bool {
	return p.Onboarding.CompletedAt != ""
}

// Path returns the absolute profile path, honoring HOME so tests can redirect it.
func Path() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ProfilePath)
	}
	return filepath.Join(home, ProfilePath)
}

// Load reads the profile.
//
// A missing file and a corrupt file are both reported as an empty profile with no
// error. Onboarding is a sales signal, not a correctness requirement: refusing to
// install because a JSON file got truncated would trade a real user's install for a
// lead we were never going to get anyway. An unreadable profile simply reads as
// "not yet prompted".
func Load() Profile {
	data, err := os.ReadFile(Path())
	if err != nil {
		return Profile{}
	}
	var p Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return Profile{}
	}
	return p
}

// Save writes the profile, minting an install ID and stamping the schema version if
// they are not set yet. The file is 0600 because it holds the operator's email.
func Save(p Profile) error {
	if p.SchemaVersion == 0 {
		p.SchemaVersion = SchemaVersion
	}
	if p.InstallID == "" {
		id, err := NewInstallID()
		if err != nil {
			return err
		}
		p.InstallID = id
	}
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	// WriteFile only applies the mode when it creates the file, so an existing
	// profile written before this code shipped would keep its old permissions.
	return os.Chmod(path, 0o600)
}

// NewInstallID returns a random 128-bit hex identifier.
//
// This is the dedupe key for the signup endpoint (so reinstalling updates one row
// instead of creating another) and the handle a user quotes to request deletion. It
// is unrelated to any hardware or account identifier and carries no user data.
func NewInstallID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// EnsureInstallID returns the profile's install ID, minting and persisting one if the
// profile does not have it yet.
func EnsureInstallID(p *Profile) (string, error) {
	if p.InstallID != "" {
		return p.InstallID, nil
	}
	id, err := NewInstallID()
	if err != nil {
		return "", err
	}
	p.InstallID = id
	return id, nil
}
