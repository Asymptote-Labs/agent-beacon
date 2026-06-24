package selfupdate

import (
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
)

const SystemLogFileName = "system.jsonl"

const (
	EventAvailable   = "update.available"
	EventCurrent     = "update.current"
	EventUnsupported = "update.unsupported"
	EventCheckFailed = "update.check_failed"
	EventApplied     = "update.applied"
	EventFailed      = "update.failed"
)

// SystemLogPath returns the local-only Beacon system/debug log that sits beside
// runtime.jsonl and inventory_state.jsonl. It is not a forwarding source.
func SystemLogPath(runtimeLogPath string, userMode bool) string {
	if runtimeLogPath != "" {
		return filepath.Join(filepath.Dir(runtimeLogPath), SystemLogFileName)
	}
	if userMode {
		return filepath.Join(filepath.Dir(writer.DefaultPath(true)), SystemLogFileName)
	}
	return filepath.Join("/var/log", "beacon-agent", SystemLogFileName)
}

type CheckEventOptions struct {
	Result       CheckResult
	Action       string
	Reason       string
	LogPath      string
	AgentVersion string
}

func CheckOutcome(res CheckResult) (string, string) {
	switch {
	case res.CurrentIsDev:
		return EventUnsupported, "dev_build"
	case res.UnsupportedCurrentVersion:
		return EventUnsupported, "unsupported_current_version"
	case res.Install.Kind == InstallHomebrew:
		return EventUnsupported, "homebrew_install"
	case res.Install.Kind != "" && res.Install.Kind != InstallSystemPkg:
		return EventUnsupported, "not_system_package"
	case res.UpdateAvailable && !res.HasArtifact:
		return EventUnsupported, "no_artifact_for_arch"
	case res.UpdateAvailable:
		return EventAvailable, "update_available"
	default:
		return EventCurrent, "already_current"
	}
}

func EmitCheckEvent(opts CheckEventOptions) error {
	action := opts.Action
	if action == "" {
		action, opts.Reason = CheckOutcome(opts.Result)
	}
	event := schema.NewEvent(schema.NewEventOptions{
		Action:       action,
		Category:     "system",
		Severity:     schema.SeverityInfo,
		Harness:      schema.HarnessInfo{Name: "beacon"},
		AgentVersion: opts.AgentVersion,
		Message:      opts.Reason,
	})
	event.Event.Kind = "beacon_system"
	event.Raw = map[string]interface{}{
		"component":       "selfupdate",
		"current_version": opts.Result.CurrentVersion,
		"latest_version":  opts.Result.LatestVersion,
		"artifact_arch":   opts.Result.ArchKey,
		"manifest_url":    resolveManifestURL(),
		"install_kind":    string(opts.Result.Install.Kind),
		"mode":            string(opts.Result.Mode),
		"reason":          opts.Reason,
	}
	if opts.Result.HasArtifact {
		event.Raw["artifact_url"] = opts.Result.Artifact.URL
	}
	_, err := writer.AppendEvent(event, writer.Options{Path: opts.LogPath})
	return err
}
