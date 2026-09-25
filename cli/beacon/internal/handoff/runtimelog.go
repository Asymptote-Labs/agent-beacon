package handoff

import (
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// LogSession reads a session back from Beacon's runtime log, for a session whose runtime no longer
// has it (a deleted transcript, another machine's log). It works for any runtime Beacon captured,
// but the log keeps less of each step than the runtime's own store.
//
// id is the full session id or, as with Find, a unique prefix of at least MinPrefixLength
// characters. harness, when set, limits a prefix to that runtime's sessions. found is false when
// the log has no such session; an id or prefix naming several returns an *AmbiguousError.
func LogSession(logPath, id, harness string) (session Session, events []schema.Event, found bool, err error) {
	detail, found, err := dashboard.ReadSessionDetail(logPath, id)
	if err != nil {
		return Session{}, nil, false, err
	}
	if !found {
		full, err := logSessionForPrefix(logPath, id, harness)
		if err != nil || full == "" {
			return Session{}, nil, false, err
		}
		if detail, found, err = dashboard.ReadSessionDetail(logPath, full); err != nil || !found {
			return Session{}, nil, false, err
		}
	}
	session = Session{
		ID: detail.Session.ID,
		// Older log rows carry a runtime's raw name ("claude", "devin"); every comparison uses the
		// canonical one.
		Harness:   canonicalHarness(asymptoteobserve.NormalizeHarnessName(detail.Session.Harness)),
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

// logSessionForPrefix returns the one session id in the log that starts with prefix, "" when none
// does or the prefix is too short to search by.
func logSessionForPrefix(logPath, prefix, harness string) (string, error) {
	if len(prefix) < MinPrefixLength {
		return "", nil
	}
	matches := map[string]string{}
	var order []string
	err := dashboard.StreamEvents(logPath, func(event schema.Event) error {
		if event.Session == nil || !strings.HasPrefix(event.Session.ID, prefix) {
			return nil
		}
		name := canonicalHarness(asymptoteobserve.NormalizeHarnessName(event.Harness.Name))
		if harness != "" && name != harness {
			return nil
		}
		if _, seen := matches[event.Session.ID]; !seen {
			matches[event.Session.ID] = name
			order = append(order, event.Session.ID)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	switch len(order) {
	case 0:
		return "", nil
	case 1:
		return order[0], nil
	}
	candidates := make([]Session, 0, len(order))
	for _, id := range order {
		candidates = append(candidates, Session{ID: id, Harness: matches[id]})
	}
	return "", &AmbiguousError{ID: prefix, Candidates: candidates}
}
