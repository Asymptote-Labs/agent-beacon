package harness

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/groksession"
)

const GrokName = groksession.Harness

// DiscoverGrok reports whether Grok Build is present and which of its two collection paths is
// carrying telemetry.
//
// Grok is the first runtime here with both. Its capability is "hooks+session_log": Beacon owns a
// hook file that Grok calls live, *and* Grok commits session records that `beacon endpoint grok
// sync` can read afterwards. Either one alone is telemetry, so discovery has to look at both --
// reporting only the sync cursor would mark a machine with working hooks as disabled until someone
// ran a sweep it does not need, and reporting only the hook file would hide a backfill-only setup.
//
// The hooks are the stronger signal and are checked first: they are live, they can participate in
// the policy seam, and they cover sessions that are still open. Session sync is a poll path that
// sees records after the fact, so it is what discovery falls back to.
func DiscoverGrok() Harness {
	h := Harness{Name: GrokName, DisplayName: "Grok Build", Capability: "hooks+session_log"}
	detectExecutable(&h, "grok")

	home, _ := os.UserHomeDir()
	if home == "" {
		h.TelemetryStatus = TelemetryMissing
		h.Message = "Grok Build directory could not be resolved: no home directory"
		return h
	}
	sessionsDir := filepath.Join(home, ".grok", "sessions")

	// Grok installed through a version manager, a shell alias or a per-user bin directory may not be
	// on the PATH this process inherited while ~/.grok is still there. Treating that directory as
	// evidence keeps discovery from reporting "not detected" for a runtime the user is actively
	// running -- the same fallback the opencode, Cursor, Hermes, Pi and fx probes make.
	if !h.Detected && dirExists(filepath.Join(home, ".grok")) {
		h.Detected = true
	}

	// User level only. A project-level hook file lives at ./.grok/hooks/beacon-endpoint.json, so
	// resolving it here would read whatever repository the command happened to run from and report
	// it as the machine's state. DiscoverQwen, DiscoverCline and DiscoverPi guard the same way.
	hookStatus := hooks.GrokHookStatus(hooks.GrokOptions{Level: hooks.LevelUser})
	if hookStatus.Installed {
		h.ConfigPath = hookStatus.HooksPath
	} else {
		h.ConfigPath = sessionsDir
	}

	h.TelemetryStatus, h.Message = grokCollectionStatus(hookStatus.Installed, sessionsDir)
	return h
}

// grokCollectionStatus classifies what is collecting Grok Build telemetry on this machine.
//
// Installed hooks settle it: telemetry is flowing live, and whether a backfill sweep has also run
// is a detail rather than a fault. Without hooks the question becomes what `grok sync` has read,
// and the answer follows the same four cases as fx -- no sessions to collect, sessions with no
// cursor, a cursor behind them, a cursor level with them -- because the remedy differs for each and
// a single "not configured" would send someone to fix the wrong thing.
func grokCollectionStatus(hooksInstalled bool, sessionsDir string) (TelemetryStatus, string) {
	collected, total, err := grokSessionProgress(sessionsDir)

	if hooksInstalled {
		msg := "Beacon Grok Build hooks are configured"
		if err == nil && total > collected {
			msg += fmt.Sprintf("; %d of %d committed session(s) not backfilled, run `beacon endpoint grok sync` to collect them", total-collected, total)
		}
		return TelemetryEnabled, msg
	}

	if err != nil {
		return TelemetryMisconfigured, "Grok Build hooks are not installed and the session collector state could not be read: " + err.Error()
	}
	switch {
	case total == 0:
		return TelemetryDisabled, "Grok Build endpoint hooks are not installed; run `beacon endpoint hooks install --harness grok`"
	case collected == 0:
		return TelemetryDisabled, fmt.Sprintf("Grok Build endpoint hooks are not installed and none of %d committed session(s) have been collected; run `beacon endpoint hooks install --harness grok`", total)
	case collected < total:
		return TelemetryEnabled, fmt.Sprintf("Grok Build session telemetry collected for %d of %d session(s); run `beacon endpoint grok sync` to catch up", collected, total)
	default:
		return TelemetryEnabled, fmt.Sprintf("Grok Build session telemetry collected for all %d session(s)", total)
	}
}

// grokSessionProgress reports how many of the committed sessions the sync cursor has read.
//
// A missing session directory is not an error: Grok may simply not have run here, or may run only
// under hooks. It reports zero sessions, which the caller reads as "nothing to backfill".
func grokSessionProgress(sessionsDir string) (collected, total int, err error) {
	store := &groksession.Store{Dir: sessionsDir}
	if !store.Exists() {
		return 0, 0, nil
	}
	refs, err := store.List()
	if err != nil {
		return 0, 0, err
	}
	if len(refs) == 0 {
		return 0, 0, nil
	}
	state, err := groksession.LoadState(groksession.DefaultStatePath())
	if err != nil {
		return 0, len(refs), err
	}
	for _, ref := range refs {
		cursor := state.Sessions[ref.ID]
		if cursor != nil && cursor.UpdatedAtMS >= ref.ModTimeUnixMS && cursor.Events > 0 {
			collected++
		}
	}
	return collected, len(refs), nil
}
