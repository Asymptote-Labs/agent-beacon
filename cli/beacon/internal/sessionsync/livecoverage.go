package sessionsync

import "time"

// liveCoverageSkew is how far before a session's first hook event a poll event still counts as
// covered, for the clock difference between the hook process and the runtime's own timestamps.
const liveCoverageSkew = 2 * time.Second

// LiveCoverage is, per session id, the earliest event the runtime's live hooks wrote for it
// (integrations.HookCoverage). A collector whose runtime also has hooks consults it so a poll sweep
// does not record twice what the hooks already recorded.
type LiveCoverage map[string]time.Time

// Suppresses reports whether a poll event with action, mapped from a record at timestampMS in
// sessionID, duplicates one the live hooks recorded. Hook payloads carry the prompt and every tool
// call as it happens, so those actions are suppressed from the session's first hook event on.
// What hooks cannot see -- agent messages, reasoning, token usage, context and compaction,
// approvals -- is never suppressed, and neither is anything from before the hooks started or a
// record with no timestamp, which cannot be placed after it.
func (c LiveCoverage) Suppresses(sessionID, action string, timestampMS int64) bool {
	cutover, ok := c[sessionID]
	if !ok || timestampMS == 0 || timestampMS < cutover.Add(-liveCoverageSkew).UnixMilli() {
		return false
	}
	switch action {
	case "session.started", "prompt.submitted", "session.handoff",
		"tool.invoked", "mcp.tool_invoked",
		"command.executed", "file.read", "file.modified", "tool.completed", "tool.failed":
		return true
	}
	return false
}
