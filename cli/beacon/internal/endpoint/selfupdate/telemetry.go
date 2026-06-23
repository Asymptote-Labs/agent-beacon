package selfupdate

import (
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
)

// emit appends an update.applied / update.failed event to the runtime log so
// updates are visible in the dashboard and to `beacon scan`. Failures to write
// telemetry are non-fatal: they must not block or fail an update.
func (a *Applier) emit(success bool, r ApplyResult, message string) {
	action := "update.failed"
	severity := schema.SeverityMedium
	// Report the version the agent is on after this event: the newly installed
	// version on success, the unchanged current version on failure.
	agentVersion := a.CurrentVersion
	if success {
		action = "update.applied"
		severity = schema.SeverityInfo
		if r.ToVersion != "" {
			agentVersion = r.ToVersion
		}
	}
	event := schema.NewEvent(schema.NewEventOptions{
		Action:       action,
		Category:     "update",
		Severity:     severity,
		AgentVersion: agentVersion,
		Harness:      schema.HarnessInfo{Name: "beacon-self-update"},
		Message:      message,
	})
	event.Raw = map[string]interface{}{
		"from_version": r.FromVersion,
		"to_version":   r.ToVersion,
		"rolled_back":  r.RolledBack,
	}
	_, _ = writer.AppendEvent(event, writer.Options{Path: a.LogPath, UserMode: false})
}
