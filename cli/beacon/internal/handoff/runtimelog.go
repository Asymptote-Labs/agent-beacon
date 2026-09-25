package handoff

import (
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

// LogSession reads a session back from Beacon's runtime log, for a session whose runtime no longer
// has it (a deleted transcript, another machine's log). It works for any runtime Beacon captured,
// but the log keeps less of each step than the runtime's own store. found is false when the log has
// no events for id.
func LogSession(logPath, id string) (session Session, events []schema.Event, found bool, err error) {
	detail, found, err := dashboard.ReadSessionDetail(logPath, id)
	if err != nil || !found {
		return Session{}, nil, false, err
	}
	session = Session{
		ID:        id,
		Harness:   detail.Session.Harness,
		Directory: detail.Session.WorkingDir,
		Branch:    detail.Session.Branch,
	}
	if last, err := time.Parse(time.RFC3339Nano, detail.Session.LastEventAt); err == nil {
		session.UpdatedAt = last.UTC()
	}
	events = make([]schema.Event, 0, len(detail.Events))
	for _, record := range detail.Events {
		events = append(events, record.Event)
	}
	return session, events, true, nil
}
