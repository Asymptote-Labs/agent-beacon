package harness

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/fxsession"
)

// FxName is the canonical harness name for fx (vercel-labs/fx), matching what
// asymptoteobserve.NormalizeHarnessName resolves every fx spelling to.
const FxName = fxsession.Harness

// DiscoverFx reports whether fx is present and how much of its session history Beacon has read.
//
// Its capability is "session_log", which no other runtime here has. The existing four all describe
// something Beacon writes into the runtime -- a hooks file, a plugin, an OTel environment, an
// admin-managed config -- and then the runtime calls Beacon. fx offers none of those: its lifecycle
// hooks are compiled into the binary, it has no OpenTelemetry export, and MCP describes the tools
// it calls rather than what it did. So Beacon reads the session records fx commits, and there is
// nothing on disk to install or to check for a marker.
//
// That changes what "telemetry enabled" can honestly mean here. For a hook runtime the config file
// answers it: the hook is installed or it is not. For fx the only evidence that telemetry is
// flowing is that a sweep has actually run and knows about the sessions on this machine, so that is
// what this reports -- not the mere presence of fx, which would claim coverage nobody has collected.
func DiscoverFx() Harness {
	h := Harness{Name: FxName, DisplayName: "fx (Vercel Labs)", Capability: "session_log"}
	detectExecutable(&h, "fx")

	home, _ := os.UserHomeDir()
	if home == "" {
		h.TelemetryStatus = TelemetryMissing
		h.Message = "fx session directory could not be resolved: no home directory"
		return h
	}
	sessionsDir := filepath.Join(home, ".fx", "sessions")
	h.ConfigPath = sessionsDir

	// fx installed through its setup script may not be on the PATH this process inherited -- a
	// shell alias, a per-user bin directory, a version manager -- while its profile directory is
	// still there. Treating that directory as evidence keeps discovery from reporting "not
	// detected" for a runtime the user is actively running, the same fallback the opencode,
	// Cursor, Hermes and Pi probes make for the same reason.
	if !h.Detected && dirExists(filepath.Join(home, ".fx")) {
		h.Detected = true
	}

	h.TelemetryStatus, h.Message = fxCollectionStatus(sessionsDir)
	return h
}

// fxCollectionStatus classifies what Beacon has collected from fx's session store.
//
// The four cases are deliberately distinct, because the remedy differs and a single "not
// configured" would send someone to fix the wrong thing:
//
//   - no session directory: fx has not run here, and there is nothing to collect yet.
//   - sessions present, no cursor: fx has been used and `beacon endpoint fx sync` has not run.
//   - cursor behind the sessions: collection is set up but stale; sweep again.
//   - cursor level with every session: everything fx has committed has been read.
func fxCollectionStatus(sessionsDir string) (TelemetryStatus, string) {
	store := &fxsession.Store{Dir: sessionsDir}
	if !store.Exists() {
		return TelemetryMissing, "fx has written no sessions on this machine"
	}
	refs, err := store.List()
	if err != nil {
		return TelemetryMissing, "fx sessions could not be read: " + err.Error()
	}
	if len(refs) == 0 {
		return TelemetryMissing, "fx has written no sessions on this machine"
	}

	state, err := fxsession.LoadState(fxsession.DefaultStatePath())
	if err != nil {
		return TelemetryMisconfigured, "fx collector state could not be read: " + err.Error()
	}

	collected := 0
	for _, ref := range refs {
		cursor := state.Sessions[ref.ID]
		if cursor == nil || cursor.LastSeq == 0 {
			continue
		}
		// A session with no readable manifest cannot say how far it has committed, so "collected"
		// is claimed only where fx's own numbers confirm it rather than assumed from having read
		// something.
		if ref.Manifest != nil && cursor.Generation == ref.Manifest.LogGeneration &&
			cursor.LastSeq >= ref.Manifest.LastEventSeq {
			collected++
		}
	}

	switch {
	case collected == 0:
		return TelemetryDisabled, fmt.Sprintf("%d fx session(s) present and none collected; run `beacon endpoint fx sync`", len(refs))
	case collected < len(refs):
		return TelemetryEnabled, fmt.Sprintf("fx session telemetry collected for %d of %d session(s); run `beacon endpoint fx sync` to catch up", collected, len(refs))
	default:
		return TelemetryEnabled, fmt.Sprintf("fx session telemetry collected for all %d session(s)", len(refs))
	}
}
