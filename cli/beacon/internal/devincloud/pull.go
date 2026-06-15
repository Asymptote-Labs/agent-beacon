package devincloud

import (
	"context"
	"encoding/json"
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
}

// Summary reports what a sweep did.
type Summary struct {
	Sessions        int
	SessionsChanged int
	EventsEmitted   int
	Uploaded        int
}

// PullOnce performs one sweep: list sessions, fetch messages for changed
// sessions, map to Beacon events, emit new ones (print/write), and upload each
// changed session's full snapshot. State makes it idempotent across runs.
func PullOnce(ctx context.Context, client *Client, opts PullOptions) (Summary, error) {
	var sum Summary

	state, err := LoadState(opts.StatePath)
	if err != nil {
		return sum, fmt.Errorf("load state: %w", err)
	}

	sessions, err := client.ListSessions(ctx)
	if err != nil {
		return sum, err
	}
	sum.Sessions = len(sessions)

	for _, s := range sessions {
		ss := state.get(s.SessionID)
		// Skip sessions already finalized, or unchanged since last sweep.
		if ss.Done {
			continue
		}
		if _, seen := state.Sessions[s.SessionID]; seen && ss.UpdatedAt == s.UpdatedAt && ss.Status == s.Status && len(ss.Emitted) > 0 {
			continue
		}

		msgs, err := client.SessionMessages(ctx, s.SessionID)
		if err != nil {
			return sum, fmt.Errorf("session %s messages: %w", s.SessionID, err)
		}
		sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].CreatedAt < msgs[j].CreatedAt })

		mapped := MapSession(s, msgs)
		sum.SessionsChanged++

		for _, me := range mapped {
			if ss.Emitted[me.DedupID] {
				continue
			}
			if err := emit(me.Event, opts); err != nil {
				return sum, err
			}
			ss.Emitted[me.DedupID] = true
			sum.EventsEmitted++
		}

		if opts.Upload != nil {
			data, err := marshalEvents(mapped)
			if err != nil {
				return sum, err
			}
			obj := ObjectName(opts.UploadPrefix, Provider, s.UserID, s.SessionID)
			if err := opts.Upload.Upload(ctx, obj, data); err != nil {
				return sum, fmt.Errorf("upload session %s: %w", s.SessionID, err)
			}
			sum.Uploaded++
		}

		ss.UpdatedAt = s.UpdatedAt
		ss.Status = s.Status
		if IsFinal(s.Status) && ss.Emitted[s.SessionID+":ended"] {
			ss.Done = true
		}
	}

	if err := state.Save(opts.StatePath); err != nil {
		return sum, fmt.Errorf("save state: %w", err)
	}
	return sum, nil
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
