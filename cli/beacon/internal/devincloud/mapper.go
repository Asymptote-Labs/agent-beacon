package devincloud

import (
	"fmt"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

// Provider is the BEACON_RUN_PROVIDER value for Devin Cloud telemetry. It
// partitions GCS objects and tags events, matching the value used by the
// in-sandbox hook providers for consistency.
const Provider = "devin_cloud"

// Harness is the harness name recorded on every Devin Cloud event.
const Harness = "devin"

// terminalStatuses mean the session is no longer actively working and a
// session.ended event should be emitted. Devin suspends sessions on inactivity
// once they finish a turn, so "suspended" is the common end state. "blocked"
// (awaiting input) and "working" are active and are not terminal.
var terminalStatuses = map[string]bool{
	"finished":  true,
	"expired":   true,
	"suspended": true,
}

// finalStatuses mean the session can never resume, so the connector can stop
// polling it. A "suspended" session may be resumed, so it is terminal (ended is
// emitted) but not final (keep polling cheaply via the updated_at check).
var finalStatuses = map[string]bool{
	"finished": true,
	"expired":  true,
}

// MappedEvent pairs a Beacon event with a stable dedup id. Lifecycle events use
// synthetic ids (e.g. "<session>:started"); message events use the Devin
// event_id so re-polling never double-emits.
type MappedEvent struct {
	DedupID string
	Event   schema.Event
}

// IsTerminal reports whether a session status means the session has ended (so
// a session.ended event should be emitted).
func IsTerminal(status string) bool {
	return terminalStatuses[strings.ToLower(strings.TrimSpace(status))]
}

// IsFinal reports whether a session can never resume, so the connector can stop
// polling it entirely.
func IsFinal(status string) bool {
	return finalStatuses[strings.ToLower(strings.TrimSpace(status))]
}

// MapSession converts a session and its messages into ordered stream events:
// session.started plus one event per message (prompt.submitted / agent.message),
// each with a stable dedup id (synthetic for started, event_id for messages).
//
// session.ended is intentionally NOT produced here. Whether to emit an end
// depends on observed status transitions (a suspended session may resume, and
// its updated_at can bump without a resume), which only the orchestrator knows.
// See EndedEvent and the transition handling in PullOnce.
func MapSession(s Session, msgs []Message) []MappedEvent {
	out := []MappedEvent{{
		DedupID: s.SessionID + ":started",
		Event:   sessionLifecycleEvent(s, "session.started", "Devin Cloud session started", s.CreatedAt),
	}}

	seen := map[string]bool{}
	for i, m := range msgs {
		dedupID := messageDedupID(s.SessionID, m, i, seen)
		switch strings.ToLower(m.Source) {
		case "user":
			ev := baseEvent(s, "prompt.submitted", "prompt", schema.SeverityInfo, m.CreatedAt)
			ev.Prompt = &schema.PromptInfo{Text: m.Message}
			ev.Content = &schema.ContentInfo{Retention: schema.ContentRetentionFull, Included: true}
			ev.Message = "Devin Cloud user prompt"
			out = append(out, MappedEvent{DedupID: dedupID, Event: ev})
		default: // "devin" (assistant) and any future agent-side source
			ev := baseEvent(s, "agent.message", "session", schema.SeverityInfo, m.CreatedAt)
			ev.Content = &schema.ContentInfo{Retention: schema.ContentRetentionFull, Included: true}
			ev.Message = m.Message
			out = append(out, MappedEvent{DedupID: dedupID, Event: ev})
		}
	}
	return out
}

// messageDedupID returns a stable, unique dedup id for a message. It prefers the
// Devin event_id, but synthesizes one from the session, index, and created_at
// when event_id is empty or duplicated — otherwise messages sharing (or missing)
// an event_id would be silently dropped by the dedup set. Messages are
// append-only and ordered, so the synthesized index is stable across re-polls.
func messageDedupID(sessionID string, m Message, index int, seen map[string]bool) string {
	id := strings.TrimSpace(m.EventID)
	if id == "" || seen[id] {
		id = fmt.Sprintf("%s:msg:%d:%d", sessionID, index, m.CreatedAt)
	}
	seen[id] = true
	return id
}

// EndedEvent builds the session.ended event for a terminal session, carrying
// final status/PR/ACU/duration metadata.
func EndedEvent(s Session) schema.Event {
	return sessionLifecycleEvent(s, "session.ended", "Devin Cloud session ended", s.UpdatedAt)
}

// sessionLifecycleEvent builds a started/ended event carrying session metadata.
func sessionLifecycleEvent(s Session, action, message string, tsUnix int64) schema.Event {
	ev := baseEvent(s, action, "session", schema.SeverityInfo, tsUnix)
	ev.Message = message
	meta := map[string]interface{}{
		"status":        s.Status,
		"status_detail": s.StatusDetail,
		"acus_consumed": s.AcusConsumed,
		"url":           s.URL,
	}
	if s.Title != "" {
		meta["title"] = s.Title
	}
	if len(s.Tags) > 0 {
		meta["tags"] = s.Tags
	}
	if s.ParentSessionID != nil && *s.ParentSessionID != "" {
		meta["parent_session_id"] = *s.ParentSessionID
	}
	if len(s.ChildSessionIDs) > 0 {
		meta["child_session_ids"] = s.ChildSessionIDs
	}
	if action == "session.ended" && s.CreatedAt > 0 && s.UpdatedAt >= s.CreatedAt {
		meta["duration_seconds"] = s.UpdatedAt - s.CreatedAt
	}
	if len(s.PullRequests) > 0 {
		prs := make([]map[string]interface{}, 0, len(s.PullRequests))
		for _, pr := range s.PullRequests {
			prs = append(prs, map[string]interface{}{"url": pr.URL, "number": pr.Number})
		}
		meta["pull_requests"] = prs
	}
	ev.Raw = map[string]interface{}{"devin": meta}
	return ev
}

// baseEvent constructs an event with the fields common to every Devin Cloud
// record: cloud origin, devin harness, run provider/id/actor, and the session
// id. ACUs are Devin compute units (not tokens or USD), so they are recorded in
// the devin-specific raw block rather than gen_ai.usage.
func baseEvent(s Session, action, category string, severity schema.Severity, tsUnix int64) schema.Event {
	repo := repoFromPullRequests(s.PullRequests)
	ev := schema.NewEvent(schema.NewEventOptions{
		Action:   action,
		Category: category,
		Severity: severity,
		Harness:  schema.HarnessInfo{Name: Harness},
		Origin:   schema.OriginCloud,
		Run: &schema.RunInfo{
			Provider:   Provider,
			RunID:      s.SessionID,
			Actor:      s.UserID,
			Repository: repo,
		},
	})
	if tsUnix > 0 {
		ev.Timestamp = time.Unix(tsUnix, 0).UTC().Format(time.RFC3339)
	}
	ev.Session = &schema.SessionInfo{ID: s.SessionID}
	if repo != "" {
		ev.Repository = repo
	}
	return ev
}

// repoFromPullRequests extracts "owner/repo" from the first GitHub-style PR URL.
func repoFromPullRequests(prs []PullRequest) string {
	for _, pr := range prs {
		if owner, repo, ok := parseRepoFromPRURL(pr.URL); ok {
			return owner + "/" + repo
		}
	}
	return ""
}

func parseRepoFromPRURL(prURL string) (owner, repo string, ok bool) {
	prURL = strings.TrimSpace(prURL)
	if prURL == "" {
		return "", "", false
	}
	idx := strings.Index(prURL, "://")
	if idx >= 0 {
		prURL = prURL[idx+3:]
	}
	parts := strings.Split(prURL, "/")
	// host/owner/repo/pull/<n>
	if len(parts) >= 3 && parts[1] != "" && parts[2] != "" {
		return parts[1], parts[2], true
	}
	return "", "", false
}
