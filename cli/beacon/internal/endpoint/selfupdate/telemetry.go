package selfupdate

import (
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
)

// emit appends an update.applied / update.failed event to Beacon's local-only
// system log. Failures to write telemetry are non-fatal: they must not block or
// fail an update/rollback attempt.
func (a *Applier) emit(success bool, r ApplyResult, message string) {
	action := EventFailed
	severity := schema.SeverityMedium
	// Report the version the agent is on after this event: the newly installed
	// version on success, the unchanged current version on failure.
	agentVersion := a.CurrentVersion
	if success {
		action = EventApplied
		severity = schema.SeverityInfo
		if r.ToVersion != "" {
			agentVersion = r.ToVersion
		}
	}
	event := schema.NewEvent(schema.NewEventOptions{
		Action:       action,
		Category:     "system",
		Severity:     severity,
		AgentVersion: agentVersion,
		Harness:      schema.HarnessInfo{Name: "beacon"},
		Message:      message,
	})
	event.Event.Kind = "beacon_system"
	event.Raw = map[string]interface{}{
		"component":    "selfupdate",
		"from_version": r.FromVersion,
		"to_version":   r.ToVersion,
		"rolled_back":  r.RolledBack,
		"reason":       message,
	}
	path := a.LogPath
	if path == "" {
		path = SystemLogPath(writer.DefaultPath(false), false)
	}
	_, _ = writer.AppendEvent(event, writer.Options{Path: path, UserMode: false})
}
