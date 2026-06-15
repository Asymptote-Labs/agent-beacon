package devincloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
)

// Uploader uploads one session's full JSONL snapshot to a destination (GCS).
type Uploader interface {
	Upload(ctx context.Context, objectName string, data []byte) error
}

// PullOptions configures a single pull sweep.
type PullOptions struct {
	Print     bool      // print mapped events as JSON to Out (dry run)
	Out       io.Writer // where --print writes (defaults handled by caller)
	Write     bool      // append new events to the local runtime JSONL
	LogPath   string    // runtime JSONL path (resolved by caller)
	UserMode  bool      // writer user/system mode
	StatePath string    // dedup state file (empty = no persistence)

	Upload       Uploader // optional GCS uploader
	UploadPrefix string   // GCS object prefix (e.g. "agent-traces")

	// ForceRefresh fetches messages for every session regardless of the
	// updated_at/status skip (dedup still prevents duplicate emits). Use it for
	// backfills, or if the API is observed to add messages without bumping
	// updated_at — the incremental signal this connector relies on.
	ForceRefresh bool
}

// Summary reports what a sweep did.
type Summary struct {
	Sessions        int
	SessionsChanged int
	EventsEmitted   int
	Uploaded        int
	Errors          int
}

// PullOnce performs one sweep: list sessions, fetch messages for changed
// sessions, map to Beacon events, emit new ones (print/write), and upload each
// changed session's full snapshot. State makes it idempotent across runs.
func PullOnce(ctx context.Context, client *Client, opts PullOptions) (sum Summary, err error) {
	state, err := LoadState(opts.StatePath)
	if err != nil {
		return sum, fmt.Errorf("load state: %w", err)
	}

	// Persist dedup progress no matter how we return. Events are appended to the
	// runtime log as each session is processed, so if a later session errors we
	// must still save the progress made — otherwise the next sweep re-emits
	// already-written events and duplicates log lines.
	defer func() {
		if saveErr := state.Save(opts.StatePath); saveErr != nil && err == nil {
			err = fmt.Errorf("save state: %w", saveErr)
		}
	}()

	sessions, err := client.ListSessions(ctx)
	if err != nil {
		return sum, err
	}
	sum.Sessions = len(sessions)

	var errs []error
	for _, s := range sessions {
		ss := state.get(s.SessionID)
		// Skip sessions already finalized, or unchanged since the last sweep.
		// "Unchanged" relies on updated_at as the session's last-modified signal
		// (new messages bump it); --full-resync (ForceRefresh) bypasses both
		// skips when that assumption can't be trusted or for a backfill. A session
		// whose snapshot still needs uploading (e.g. upload was disabled on an
		// earlier run and is now enabled) is never skipped on the upload account.
		if !opts.ForceRefresh {
			uploadSatisfied := opts.Upload == nil || ss.UploadedAt == s.UpdatedAt
			if ss.Done && uploadSatisfied {
				continue
			}
			if _, seen := state.Sessions[s.SessionID]; seen && ss.UpdatedAt == s.UpdatedAt && ss.Status == s.Status && len(ss.Emitted) > 0 && uploadSatisfied {
				continue
			}
		}

		if perr := processSession(ctx, client, s, ss, opts, &sum); perr != nil {
			// One bad session must not block the rest of the org. Record it and
			// move on; its change cursor is left unadvanced so it is retried next
			// sweep (dedup prevents re-emitting already-written events).
			errs = append(errs, fmt.Errorf("session %s: %w", s.SessionID, perr))
			sum.Errors++
		}
	}

	if len(errs) > 0 {
		return sum, errors.Join(errs...)
	}
	return sum, nil
}

// processSession fetches and emits one session's events. State (UpdatedAt /
// Status / Done) is advanced only on full success, so a partial failure causes
// a retry on the next sweep.
func processSession(ctx context.Context, client *Client, s Session, ss *SessionState, opts PullOptions, sum *Summary) error {
	msgs, err := client.SessionMessages(ctx, s.SessionID)
	if err != nil {
		return fmt.Errorf("messages: %w", err)
	}
	sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].CreatedAt < msgs[j].CreatedAt })

	mapped := MapSession(s, msgs)
	sum.SessionsChanged++

	for _, me := range mapped {
		if ss.Emitted[me.DedupID] {
			continue
		}
		if err := emit(me.Event, opts); err != nil {
			return err
		}
		ss.Emitted[me.DedupID] = true
		sum.EventsEmitted++
	}

	// session.ended is emitted once per observed terminal episode: only when the
	// session is terminal and we have not already emitted an end since it was
	// last seen active. Observing a non-terminal status resets the flag so a
	// genuine resume-then-end emits a fresh end. This avoids both missing
	// re-ends after a resume and spurious ends on metadata-only updated_at bumps.
	terminal := IsTerminal(s.Status)
	if terminal {
		if !ss.EndedEmitted {
			if err := emit(EndedEvent(s), opts); err != nil {
				return err
			}
			ss.EndedEmitted = true
			sum.EventsEmitted++
		}
	} else {
		ss.EndedEmitted = false
	}

	if opts.Upload != nil {
		// The GCS object is the full per-session snapshot, so it always includes
		// the ended event while the session is terminal (idempotent overwrite).
		snapshot := make([]MappedEvent, 0, len(mapped)+1)
		snapshot = append(snapshot, mapped...)
		if terminal {
			snapshot = append(snapshot, MappedEvent{Event: EndedEvent(s)})
		}
		data, err := marshalEvents(snapshot)
		if err != nil {
			return err
		}
		obj := ObjectName(opts.UploadPrefix, Provider, s.UserID, s.SessionID)
		if err := opts.Upload.Upload(ctx, obj, data); err != nil {
			return fmt.Errorf("upload: %w", err)
		}
		ss.UploadedAt = s.UpdatedAt
		sum.Uploaded++
	}

	ss.UpdatedAt = s.UpdatedAt
	ss.Status = s.Status
	// A final session (finished/expired) can never resume, so stop polling it.
	// Suspended stays pollable in case it resumes.
	if IsFinal(s.Status) {
		ss.Done = true
	}
	return nil
}

func emit(ev schema.Event, opts PullOptions) error {
	if opts.Print && opts.Out != nil {
		data, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		if _, err := opts.Out.Write(append(data, '\n')); err != nil {
			return err
		}
	}
	if opts.Write {
		if _, err := writer.AppendEvent(ev, writer.Options{Path: opts.LogPath, UserMode: opts.UserMode}); err != nil {
			return err
		}
	}
	return nil
}

// marshalEvents builds the per-session JSONL snapshot for upload. Events are run
// through the same redaction/truncation/size controls as the local writer so
// the GCS snapshot never carries more raw content than the local log.
func marshalEvents(mapped []MappedEvent) ([]byte, error) {
	var buf []byte
	for _, me := range mapped {
		data, err := json.Marshal(writer.SanitizeEvent(me.Event, writer.MaxEventBytes))
		if err != nil {
			return nil, err
		}
		buf = append(buf, data...)
		buf = append(buf, '\n')
	}
	return buf, nil
}

// ObjectName builds the GCS object path, matching the layout used by the
// in-sandbox hook providers: {prefix}/provider=.../user_id=.../run_id=.../runtime.jsonl
func ObjectName(prefix, provider, userID, sessionID string) string {
	parts := []string{}
	for _, p := range splitNonEmpty(prefix) {
		parts = append(parts, p)
	}
	parts = append(parts, "provider="+defaultIfEmpty(provider, "unknown"))
	parts = append(parts, "user_id="+defaultIfEmpty(userID, "unknown"))
	parts = append(parts, "run_id="+defaultIfEmpty(sessionID, "unknown"))
	parts = append(parts, "runtime.jsonl")
	return path.Join(parts...)
}

func splitNonEmpty(prefix string) []string {
	var out []string
	for _, p := range splitSlash(prefix) {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func splitSlash(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '/' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	return append(out, cur)
}

func defaultIfEmpty(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
