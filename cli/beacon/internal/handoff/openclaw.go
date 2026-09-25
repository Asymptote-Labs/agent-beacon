package handoff

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/openclawsession"
)

// OpenClaw Gateway keeps one transcript store per agent profile, agents/<profile>/sessions/ under
// its state directory, indexed by a sessions.json that files each transcript under the Gateway
// session key the conversation is routed by. Transcript ids are unique only within a profile.

type openClawSource struct{ dir string }

func (s *openClawSource) Harness() string { return HarnessOpenClaw }

// store reads dir as OpenClaw's state directory, as `beacon endpoint openclaw sessions sync
// --openclaw-dir` does. Empty means the default, honouring OPENCLAW_STATE_DIR.
func (s *openClawSource) store() (*openclawsession.Store, error) {
	return openclawsession.NewStore(s.dir)
}

func (s *openClawSource) List() ([]Session, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	if !store.Exists() {
		return nil, nil
	}
	refs, err := store.List()
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, 0, len(refs))
	for _, ref := range refs {
		sessions = append(sessions, Session{
			Harness:    HarnessOpenClaw,
			ID:         ref.ID,
			Title:      openClawTitle(ref),
			Directory:  ref.Directory,
			SourcePath: ref.SourcePath,
			Key:        openClawSessionKey(ref),
			UpdatedAt:  unixMS(ref.UpdatedAtUnixMS),
		})
	}
	return sessions, nil
}

func (s *openClawSource) Events(session Session) ([]schema.Event, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	refs, err := store.List()
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		if ref.ID != session.ID || ref.SourcePath != session.SourcePath {
			continue
		}
		records, err := store.Read(ref)
		if err != nil {
			return nil, err
		}
		mapped := openclawsession.MapTrace(ref, records, openclawsession.MapOptions{})
		events := make([]schema.Event, 0, len(mapped))
		for _, m := range mapped {
			events = append(events, m.Event)
		}
		return events, nil
	}
	return nil, errSessionGone(session)
}

// openClawNoPreview is the title the store gives a transcript with no prompt.
const openClawNoPreview = "(No preview)"

func openClawTitle(ref openclawsession.TraceRef) string {
	for _, title := range []string{ref.Title, ref.Preview} {
		// The store cuts titles at a byte count, which can split a character.
		if title = oneLine(strings.ToValidUTF8(title, "")); title != "" && title != openClawNoPreview {
			return title
		}
	}
	return ""
}

// openClawSessionKey is the Gateway session key that reopens ref, or "" when there is none Beacon
// can pass on safely. The Gateway qualifies a key with its agent ("agent:<agent>:<rest>"), and an
// unqualified one is resolved against whichever agent the TUI settles on, so only a key qualified
// with the profile the transcript is stored under names this conversation.
func openClawSessionKey(ref openclawsession.TraceRef) string {
	key := strings.TrimSpace(ref.SessionKey)
	parts := strings.SplitN(key, ":", 3)
	if len(parts) != 3 || !strings.EqualFold(parts[0], "agent") || parts[2] == "" {
		return ""
	}
	if !strings.EqualFold(parts[1], ref.Profile) {
		return ""
	}
	return key
}

// openClawNewSessionKey names the Gateway session a brief starts. Without --session the TUI posts
// into the agent's shared main session, so every handoff gets a key of its own: the prompt names
// the brief, whose file name is never reused.
func openClawNewSessionKey(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return "beacon-handoff-" + hex.EncodeToString(sum[:8])
}
