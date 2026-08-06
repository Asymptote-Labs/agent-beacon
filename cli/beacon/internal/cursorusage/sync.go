package cursorusage

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
)

// Options configures a single sync sweep. The sweep is entirely offline: it
// reads a snapshot of the local store and appends to the local runtime log.
type Options struct {
	DBPath    string    // Cursor state.vscdb path (resolved by caller)
	StatePath string    // dedup state file (empty = no persistence)
	LogPath   string    // runtime JSONL path (resolved by caller)
	UserMode  bool      // writer user/system mode
	Print     bool      // print events as JSON to Out instead of writing (dry run, no state)
	Out       io.Writer // where --print writes
	Since     time.Time // when set, only generations at or after this instant are emitted
}

// Summary reports what a sweep did.
type Summary struct {
	Bubbles       int // bubble rows examined
	Emitted       int // events appended (or printed)
	SkippedZero   int // bubbles with no recorded token counts
	SkippedDedup  int // generations already synced on a prior run
	SkippedBefore int // generations older than --since
	ParseErrors   int // rows skipped as unparseable
}

// SyncOnce performs one sweep: snapshot the store, extract generations with
// recorded usage, and emit the ones not already synced, oldest first so the
// runtime log stays roughly chronological. Dry runs (Print) neither read nor
// write dedup state.
func SyncOnce(opts Options) (sum Summary, err error) {
	statePath := opts.StatePath
	if opts.Print {
		statePath = ""
	}
	state, err := LoadState(statePath)
	if err != nil {
		return sum, fmt.Errorf("load state: %w", err)
	}
	// Persist dedup progress no matter how we return: events are appended as we
	// go, so a partial failure must not cause the next run to duplicate them.
	defer func() {
		if saveErr := state.Save(statePath); saveErr != nil && err == nil {
			err = fmt.Errorf("save state: %w", saveErr)
		}
	}()
	// A missing state file next to an existing log means the state was lost;
	// reseed from the log so already-synced events are not re-emitted.
	if statePath != "" && len(state.Composers) == 0 && opts.LogPath != "" {
		if err := state.RebuildFromLog(opts.LogPath); err != nil {
			return sum, fmt.Errorf("rebuild state from log: %w", err)
		}
	}

	db, cleanup, err := OpenSnapshot(opts.DBPath)
	defer cleanup()
	if err != nil {
		return sum, err
	}

	generations, stats, err := ExtractGenerations(db)
	if err != nil {
		return sum, err
	}
	sum.Bubbles = stats.Bubbles
	sum.SkippedZero = stats.SkippedZero
	sum.ParseErrors = stats.ParseErrors

	sort.SliceStable(generations, func(i, j int) bool {
		return generations[i].Timestamp.Before(generations[j].Timestamp)
	})

	now := time.Now().UTC()
	for _, g := range generations {
		if state.emitted(g.ComposerID, g.BubbleID) {
			sum.SkippedDedup++
			continue
		}
		if !opts.Since.IsZero() && !g.Timestamp.IsZero() && g.Timestamp.Before(opts.Since) {
			sum.SkippedBefore++
			continue
		}
		if g.Timestamp.IsZero() {
			g.Timestamp = now
		}
		if err := emit(EventFromGeneration(g), opts); err != nil {
			return sum, err
		}
		state.markEmitted(g.ComposerID, g.BubbleID)
		sum.Emitted++
	}
	return sum, nil
}

func emit(ev schema.Event, opts Options) error {
	if opts.Print {
		out := opts.Out
		if out == nil {
			return fmt.Errorf("print requested with no output writer")
		}
		data, err := json.Marshal(writer.SanitizeEvent(ev, writer.MaxEventBytes))
		if err != nil {
			return err
		}
		_, err = out.Write(append(data, '\n'))
		return err
	}
	_, err := writer.AppendEvent(ev, writer.Options{Path: opts.LogPath, UserMode: opts.UserMode})
	return err
}
