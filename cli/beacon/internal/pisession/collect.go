package pisession

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
)

type CollectOptions struct {
	SessionsDir string
	StatePath   string
	Write       bool
	LogPath     string
	UserMode    bool
	Print       bool
	Out         io.Writer
}

type Summary struct {
	Sessions        int `json:"sessions"`
	SessionsChanged int `json:"sessions_changed"`
	EventsEmitted   int `json:"events_emitted"`
	Errors          int `json:"errors"`
	MalformedLines  int `json:"malformed_lines"`
}

func CollectOnce(opts CollectOptions) (summary Summary, err error) {
	store, err := NewStore(opts.SessionsDir)
	if err != nil {
		return summary, err
	}
	refs, err := store.List()
	if err != nil {
		return summary, err
	}
	summary.Sessions = len(refs)
	if len(refs) == 0 {
		return summary, nil
	}
	state, err := LoadState(opts.StatePath)
	if err != nil {
		return summary, err
	}
	defer func() {
		if saveErr := state.Save(opts.StatePath); saveErr != nil && err == nil {
			err = fmt.Errorf("save Pi collector state: %w", saveErr)
		}
	}()

	var errs []error
	for _, ref := range refs {
		changed, collectErr := collectFile(store, ref, state, opts, &summary)
		if collectErr != nil {
			summary.Errors++
			errs = append(errs, fmt.Errorf("Pi session %s: %w", ref.Path, collectErr))
			continue
		}
		if changed {
			summary.SessionsChanged++
		}
	}
	if len(errs) > 0 {
		return summary, errors.Join(errs...)
	}
	return summary, nil
}

func collectFile(store *Store, ref SessionRef, state *State, opts CollectOptions, summary *Summary) (bool, error) {
	cursor := state.cursor(ref.Path)
	if cursor.LastLine > 0 && cursor.ModTimeMS == ref.ModTimeMS && cursor.SizeBytes == ref.SizeBytes {
		return false, nil
	}
	entries, stats, err := store.Read(ref)
	summary.MalformedLines += stats.Malformed
	if err != nil {
		return false, err
	}
	mapped := MapSession(ref, entries, MapOptions{
		MinLine:     cursor.LastLine,
		SkipStarted: cursor.Started,
	})
	if len(mapped) == 0 {
		advance(cursor, ref, stats.MaxLine)
		return false, nil
	}
	for i, item := range mapped {
		if err := emit(item.Event, opts); err != nil {
			advancePartial(cursor, mapped, i)
			return true, err
		}
		summary.EventsEmitted++
		if item.Event.Event.Action == "session.started" {
			cursor.Started = true
		}
	}
	advance(cursor, ref, stats.MaxLine)
	return true, nil
}

func advance(cursor *Cursor, ref SessionRef, line int) {
	if line > cursor.LastLine {
		cursor.LastLine = line
	}
	cursor.ModTimeMS = ref.ModTimeMS
	cursor.SizeBytes = ref.SizeBytes
}

// advancePartial moves the cursor past source lines whose mapped events were
// all emitted without stamping the file's size/mtime, so the next sweep
// re-reads the file and retries from the failed source line.
func advancePartial(cursor *Cursor, mapped []MappedEvent, failedIdx int) {
	var lastCompleteLine int
	found := false
	for i := range mapped {
		if i == failedIdx {
			break
		}
		if i+1 < len(mapped) && mapped[i+1].SourceLine != mapped[i].SourceLine {
			lastCompleteLine = mapped[i].SourceLine
			found = true
		}
	}
	if !found {
		return
	}
	if lastCompleteLine > cursor.LastLine {
		cursor.LastLine = lastCompleteLine
	}
}

func emit(event schema.Event, opts CollectOptions) error {
	if opts.Print && opts.Out != nil {
		data, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if _, err := opts.Out.Write(append(data, '\n')); err != nil {
			return err
		}
	}
	if opts.Write {
		if _, err := writer.AppendEvent(event, writer.Options{Path: opts.LogPath, UserMode: opts.UserMode}); err != nil {
			return err
		}
	}
	return nil
}
