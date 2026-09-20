package harness

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/groksession"
)

const GrokName = groksession.Harness

func DiscoverGrok() Harness {
	h := Harness{Name: GrokName, DisplayName: "Grok Build"}
	detectExecutable(&h, "grok")
	home, _ := os.UserHomeDir()
	sessionsDir := filepath.Join(home, ".grok", "sessions")
	h.ConfigPath = sessionsDir
	if !h.Detected && dirExists(filepath.Join(home, ".grok")) {
		h.Detected = true
	}
	if hooks.IsGrokInstalled(hooks.GrokOptions{}) {
		h.Capability = "hooks"
		h.TelemetryStatus = TelemetryEnabled
		h.Message = "Beacon Grok Build hooks are configured"
		return h
	}
	h.Capability = "session_log"
	h.TelemetryStatus, h.Message = grokCollectionStatus(sessionsDir)
	return h
}

func grokCollectionStatus(sessionsDir string) (TelemetryStatus, string) {
	store := &groksession.Store{Dir: sessionsDir}
	if !store.Exists() {
		return TelemetryMissing, "Grok Build has written no sessions on this machine"
	}
	refs, err := store.List()
	if err != nil {
		return TelemetryMissing, "Grok Build sessions could not be read: " + err.Error()
	}
	if len(refs) == 0 {
		return TelemetryMissing, "Grok Build has written no sessions on this machine"
	}
	state, err := groksession.LoadState(groksession.DefaultStatePath())
	if err != nil {
		return TelemetryMisconfigured, "Grok Build collector state could not be read: " + err.Error()
	}
	collected := 0
	for _, ref := range refs {
		cursor := state.Sessions[ref.ID]
		if cursor != nil && cursor.UpdatedAtMS >= ref.ModTimeUnixMS && cursor.Events > 0 {
			collected++
		}
	}
	switch {
	case collected == 0:
		return TelemetryDisabled, fmt.Sprintf("%d Grok Build session(s) present and none collected; run `beacon endpoint grok sync`", len(refs))
	case collected < len(refs):
		return TelemetryEnabled, fmt.Sprintf("Grok Build session telemetry collected for %d of %d session(s); run `beacon endpoint grok sync` to catch up", collected, len(refs))
	default:
		return TelemetryEnabled, fmt.Sprintf("Grok Build session telemetry collected for all %d session(s)", len(refs))
	}
}
