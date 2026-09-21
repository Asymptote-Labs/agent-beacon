package harness

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/dshsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
)

func DiscoverDsh() Harness {
	h := Harness{Name: dshsession.Harness, DisplayName: "DeepSeek Harness", Capability: "hooks+session_log"}
	detectExecutable(&h, "dsh")
	store, err := dshsession.NewStore("")
	if err != nil {
		h.TelemetryStatus = TelemetryMissing
		h.Message = "DeepSeek Harness home could not be resolved: " + err.Error()
		return h
	}
	if !h.Detected && dirExists(store.DSHHome) {
		h.Detected = true
	}
	hookStatus := hooks.DshHookStatus(hooks.DshOptions{Level: hooks.LevelUser})
	if hookStatus.Installed {
		h.ConfigPath = hookStatus.HooksPath
	} else {
		h.ConfigPath = store.SessionsDir
	}
	h.TelemetryStatus, h.Message = dshCollectionStatus(hookStatus.Installed, store.SessionsDir)
	return h
}

func dshCollectionStatus(hooksInstalled bool, sessionsDir string) (TelemetryStatus, string) {
	collected, total, err := dshSessionProgress(sessionsDir)
	if hooksInstalled {
		msg := "Beacon DeepSeek Harness hooks are configured"
		if err == nil && total > collected {
			msg += fmt.Sprintf("; %d of %d native session(s) not backfilled, run `beacon endpoint dsh sync` to collect them", total-collected, total)
		}
		return TelemetryEnabled, msg
	}
	if err != nil {
		return TelemetryMisconfigured, "DeepSeek Harness hooks are not installed and the session collector state could not be read: " + err.Error()
	}
	switch {
	case total == 0:
		return TelemetryDisabled, "DeepSeek Harness endpoint hooks are not installed; run `beacon endpoint hooks install --harness dsh`"
	case collected == 0:
		return TelemetryDisabled, fmt.Sprintf("DeepSeek Harness endpoint hooks are not installed and none of %d native session(s) have been collected; run `beacon endpoint hooks install --harness dsh`", total)
	case collected < total:
		return TelemetryEnabled, fmt.Sprintf("DeepSeek Harness session telemetry collected for %d of %d session(s); run `beacon endpoint dsh sync` to catch up", collected, total)
	default:
		return TelemetryEnabled, fmt.Sprintf("DeepSeek Harness session telemetry collected for all %d session(s)", total)
	}
}

func dshSessionProgress(sessionsDir string) (collected, total int, err error) {
	if info, statErr := os.Stat(sessionsDir); statErr != nil || !info.IsDir() {
		return 0, 0, nil
	}
	store, err := dshsession.NewStore(filepath.Dir(sessionsDir))
	if err != nil {
		return 0, 0, err
	}
	refs, err := store.List()
	if err != nil {
		return 0, 0, err
	}
	if len(refs) == 0 {
		return 0, 0, nil
	}
	state, err := dshsession.LoadState(dshsession.DefaultStatePath())
	if err != nil {
		return 0, len(refs), err
	}
	for _, ref := range refs {
		cursor := state.Sources[ref.Path]
		if cursor != nil && cursor.ModTimeMS >= ref.ModTimeUnixMS && cursor.SizeBytes == ref.SizeBytes && cursor.Events > 0 && !cursor.PartialTail && !cursor.PartialFrame {
			collected++
		}
	}
	return collected, len(refs), nil
}
