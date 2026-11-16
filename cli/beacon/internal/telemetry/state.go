package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/atomicfile"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
)

// StatePath is the telemetry state file relative to the home directory, beside profile.json.
//
// It is its own file rather than fields in profile.json because the onboarding wizard loads the
// profile when it starts and saves that copy when the install finishes: an identity written in
// between, by the sign-in inside the wizard, would be overwritten by the older copy.
const StatePath = ".beacon/telemetry.json"

// state is what telemetry remembers between runs.
type state struct {
	// NoticeShownAt is when the first-run notice was printed, so it is printed once.
	NoticeShownAt string `json:"notice_shown_at,omitempty"`
	// PHDistinctID is the identified person's distinct ID (DistinctIDForEmail) once the CLI has
	// learned an email. It outlives `beacon logout`: the machine's history stays with the person.
	PHDistinctID string `json:"ph_distinct_id,omitempty"`
	// Company is the company group of the identified person, "" for a personal mailbox.
	Company string `json:"company,omitempty"`
}

func statePath(home string) string {
	return filepath.Join(home, StatePath)
}

// loadState reads the state; a missing or unreadable file is an empty state.
func loadState(home string) state {
	data, err := os.ReadFile(statePath(home))
	if err != nil {
		return state{}
	}
	var s state
	if err := json.Unmarshal(data, &s); err != nil {
		return state{}
	}
	return s
}

// saveState writes the state 0600, like profile.json.
func saveState(home string, s state) error {
	path := statePath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(data, '\n'), 0o600)
}

// homeWritable reports whether this process may create files in home. root running with another
// account's HOME (sudo on macOS keeps it) must not, or it would leave root-owned files in that
// person's ~/.beacon that their own runs could no longer update.
func homeWritable(home string) bool {
	if os.Geteuid() != 0 {
		return true
	}
	uid, err := sessionfs.OwnerUID(home)
	return err == nil && uid == 0
}
