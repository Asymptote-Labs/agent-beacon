package sessionsync

import "time"

// liveCoverageSkew is how far before a session's first hook event a poll event still counts as
// covered, for the clock difference between the hook process and the runtime's own timestamps.
const liveCoverageSkew = 2 * time.Second

// LiveCoverage is what a runtime's live hooks already wrote to the runtime log
// (integrations.HookCoverage). A collector whose runtime also has hooks consults it so a poll sweep
// does not record twice what the hooks already recorded.
type LiveCoverage struct {
	// From is, per session id, the earliest event the hooks wrote for it.
	From map[string]time.Time
	// Prompts is, per session id, the content.hash of every prompt the hooks recorded. Both paths
	// hash a prompt with asymptoteobserve.RetainedContent, so the same prompt has the same hash on
	// either, in every retention mode.
	Prompts map[string]map[string]bool
}

// Suppresses reports whether a poll event with action, mapped from a record at timestampMS in
// sessionID, duplicates one the live hooks recorded. Hook payloads carry the prompt and every tool
// call as it happens, so those actions are suppressed from the session's first hook event on.
// What hooks cannot see -- agent messages, reasoning, token usage, context and compaction,
// approvals -- is never suppressed, and neither is anything from before the hooks started or a
// record with no timestamp, which cannot be placed after it.
//
// A runtime whose hooks can miss a prompt matches prompts with RecordedPrompt instead.
func (c LiveCoverage) Suppresses(sessionID, action string, timestampMS int64) bool {
	cutover, ok := c.From[sessionID]
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

// RecordedPrompt reports whether the hooks recorded a prompt with contentHash in sessionID. It
// answers by the prompt itself rather than by time, for a runtime whose hooks record some of a
// session's prompts and not others. Identical prompts in one session share a hash, so a repeat of
// a recorded prompt counts as recorded.
func (c LiveCoverage) RecordedPrompt(sessionID, contentHash string) bool {
	return contentHash != "" && c.Prompts[sessionID][contentHash]
}
