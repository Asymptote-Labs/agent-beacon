package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/atomicfile"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
)

// StatePath is the telemetry state file relative to the home directory, beside profile.json.
//
// It is its own file rather than fields in profile.json because the onboarding wizard loads the
// profile when it starts and saves that copy when the install finishes: an identity written in
// between, by the sign-in inside the wizard, would be overwritten by the older copy.
const StatePath = ".beacon/telemetry.json"

// stateLockPath is the lock file that serializes updates of StatePath across processes: an
// interactive command (the notice, Identify, the install marker) and the scheduled inventory job
// (the live-trace position) can update it at the same moment.
const stateLockPath = ".beacon/telemetry.lock"

// state is what telemetry remembers between runs.
type state struct {
	// NoticeShownAt is when the first-run notice was printed, so it is printed once.
	NoticeShownAt string `json:"notice_shown_at,omitempty"`
	// PHDistinctID is the identified person's distinct ID (DistinctIDForEmail) once the CLI has
	// learned an email. It outlives `beacon logout`: the machine's history stays with the person.
	PHDistinctID string `json:"ph_distinct_id,omitempty"`
	// Company is the company group of the identified person, "" for a personal mailbox.
	Company string `json:"company,omitempty"`
	// InstalledAt is when `beacon endpoint install` first succeeded for this home. It outlives
	// `beacon endpoint uninstall`, which removes the install manifest, so a reinstall is not
	// counted as the device's first install.
	InstalledAt string `json:"installed_at,omitempty"`
	// LiveTrace is where the scheduled scan for live sessions stopped reading the runtime log,
	// nil before its first run.
	LiveTrace *LiveTracePosition `json:"live_trace,omitempty"`
}

// LiveTracePosition is where the scheduled scan for live sessions stopped reading the runtime log.
type LiveTracePosition struct {
	// Offset is how far into the log the scan read.
	Offset int64 `json:"offset"`
	// LogID identifies the file Offset is into (a hash of its first line, which an append-only log
	// never changes), so a scan can tell the same file from the one that replaced it at rotation.
	// "" when the file had no complete line yet.
	LogID string `json:"log_id,omitempty"`
	// SavedAt is when the scan read up to Offset.
	SavedAt time.Time `json:"saved_at"`
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

// updateState applies change to the state on disk under the cross-process lock, and saves it when
// change returns true. It reports whether the state was saved. When the lock cannot be taken
// nothing is written, rather than a copy read without it overwriting another process's update.
func updateState(home string, change func(*state) bool) bool {
	unlock, ok := lockHomeFile(home, stateLockPath)
	if !ok {
		return false
	}
	defer unlock()
	st := loadState(home)
	if !change(&st) {
		return false
	}
	return saveState(home, st) == nil
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
