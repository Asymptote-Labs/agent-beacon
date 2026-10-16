package asymptoteobserve

import "strings"

// Actions only Beacon itself writes to the runtime log: its bookkeeping about a session or a
// repository rather than anything a runtime did.
const (
	// ActionSessionShared records that a session was shared or a share refreshed.
	ActionSessionShared = "session.shared"
	// ActionShareRequested is the event the hook adapter writes once per session when auto-share
	// applies to it. Beacon Cloud publishes a trace on it.
	ActionShareRequested = "session.share_requested"
	// ActionCommitLinked records that a commit was linked to a session.
	ActionCommitLinked = "session.commit_linked"
	// ActionGitHookBudgetExceeded records that one of Beacon's git hooks ran out of its time budget.
	ActionGitHookBudgetExceeded = "git.hook_budget_exceeded"
)

// IsBeaconOnlyAction reports whether action is one only Beacon itself writes. Telemetry that
// arrives from outside -- anything that can reach the loopback OTLP receiver -- must not be
// believed when it states one, and a session's own activity is told apart from Beacon's
// bookkeeping about it with the same test.
func IsBeaconOnlyAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case ActionSessionShared, ActionShareRequested, ActionCommitLinked, ActionGitHookBudgetExceeded:
		return true
	}
	return false
}
