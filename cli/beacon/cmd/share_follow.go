package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/account"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/handoff"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// `beacon share --follow` keeps a shared trace current while its session runs: it re-reads the
// session every --interval and uploads what changed, until the session records its end or the
// command is interrupted. Each poll is local; a request is made only when the session's events
// changed since the last upload, and it goes through shareUpload, the same align-and-resume path
// as `beacon share refresh`.

const (
	shareFollowDefaultInterval = 10 * time.Second
	shareFollowMinInterval     = time.Second
	// shareFollowFinalRetries bounds how many more times --follow tries to upload a session's last
	// events after the session ended, one interval apart, before it gives up with an error.
	shareFollowFinalRetries = 3
	// shareFollowMaxReadFailures is how many polls in a row may fail to read the session, after
	// the first upload, before --follow gives up with an error.
	shareFollowMaxReadFailures = 5
)

var (
	// shareFollowWait sleeps between polls; tests replace it to drive the loop without a clock.
	shareFollowWait = func(ctx context.Context, d time.Duration) error {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	// shareFollowSignals wires Ctrl+C and SIGTERM to the follow loop's context.
	shareFollowSignals = func(ctx context.Context) (context.Context, context.CancelFunc) {
		return signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	}
)

// shareFollowUpdate is one upload --follow made; --json prints one per line.
type shareFollowUpdate struct {
	shareResult
	Final bool `json:"final,omitempty"`
	// Ended is true when the session recorded its end.
	Ended bool `json:"ended,omitempty"`
}

func runShareFollow(cmd *cobra.Command, client *account.ShareClient, subject shareSubject, visibility string) error {
	interval := shareOpts.interval
	if interval < shareFollowMinInterval {
		interval = shareFollowMinInterval
	}
	ctx, stop := shareFollowSignals(commandContext(cmd))
	defer stop()
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	if !shareOpts.jsonOutput {
		fmt.Fprintf(errOut, "Following %s session %s, uploading new events every %s. Ctrl+C stops.\n", subject.session.Harness, subject.session.ID, interval)
	}
	var (
		lastPrint     string
		last          shareResult
		uploads       int
		ended         bool
		lastErr       error
		finalFailures int
		finalErr      error
	)
	var (
		readFailures int
		lastReadWarn string
	)
	for {
		var readErr error
		if subject.fromLog {
			// A session read from the runtime log is re-read each time; one from a store is read
			// fresh by buildSharePayload anyway.
			if fresh, err := resolveShareLog(subject.session); err == nil {
				fresh.selectedBy = subject.selectedBy
				subject = fresh
			} else {
				readErr = err
			}
		}
		fingerprint, end, err := shareFollowState(subject)
		ended = end
		// Before the first upload a session with nothing to share yet is waited for; after it, a
		// session that has nothing to share any more (its store lost it, and the log holds only
		// this command's own session.shared records) is a read failure, not a reason to wait forever.
		if readErr == nil && err != nil && (uploads > 0 || !errors.Is(err, errShareNothingYet)) {
			readErr = err
		}
		if uploads > 0 {
			// After the first upload a read failure is not fatal at once (a store or log being
			// rewritten under us), but it is never silent, and it is not retried forever.
			if readErr != nil {
				readFailures++
				if readFailures >= shareFollowMaxReadFailures {
					return fmt.Errorf("stopped following: the session could not be read %d times in a row (%s holds %d events): %w", readFailures, last.SharedURL, last.EventCount, readErr)
				}
				if msg := readErr.Error(); msg != lastReadWarn {
					fmt.Fprintf(errOut, "warning: could not read the session, retrying in %s: %v\n", interval, readErr)
					lastReadWarn = msg
				}
			} else {
				readFailures, lastReadWarn = 0, ""
			}
		}
		if err == nil && fingerprint != lastPrint {
			// Every upload that sends events records a session.shared refresh, so a session
			// being followed never looks out of date locally; it is at most one per interval.
			opts := shareUploadOptions{Record: true}
			if uploads == 0 {
				// An explicit --visibility or --title applies once, to the trace as it is now
				// (created or existing). Later uploads leave both alone, so a rename on beacon.sh
				// while following is kept.
				opts.Visibility = visibility
				opts.ExplicitVisibility = visibility != ""
				if f := cmd.Flags().Lookup("title"); f != nil && f.Changed {
					opts.ExplicitTitle = true
				}
			}
			result, aligned, upErr := shareUpload(ctx, client, subject, opts)
			var recordErr *shareRecordError
			if errors.As(upErr, &recordErr) {
				// The trace is up; only the local session.shared event is missing. Say so and keep
				// following, as a later upload's record failure does.
				fmt.Fprintf(errOut, "warning: the session was shared, but the runtime log was not updated: %v\n", recordErr.err)
				upErr = nil
			}
			switch {
			case upErr == nil:
				subject, last, lastPrint = aligned, result, fingerprint
				uploads++
				if err := printShareFollow(out, errOut, result, uploads == 1, false, ended); err != nil {
					return err
				}
			case ctx.Err() != nil:
				// Interrupted mid-upload; the loop ends below.
			case uploads == 0:
				return upErr
			default:
				// beacon.sh or the network failed after the first upload: say so and retry on the
				// next poll, since the next upload resends whatever this one lacked.
				lastErr = upErr
				fmt.Fprintf(errOut, "warning: upload failed, retrying in %s: %v\n", interval, upErr)
			}
		} else if err != nil && uploads == 0 && !errors.Is(err, errShareNothingYet) {
			return err
		}
		if ctx.Err() != nil {
			break
		}
		if ended {
			if err != nil || fingerprint == lastPrint {
				break
			}
			// The session ended with events beacon.sh does not have yet. There is no next poll
			// to catch up on, so retry a few times and then fail rather than report a trace that
			// is missing its end.
			finalFailures++
			if finalFailures > shareFollowFinalRetries {
				finalErr = fmt.Errorf("the session ended, but its last events were not uploaded after %d attempts (%s holds %d events): %w", finalFailures, last.SharedURL, last.EventCount, lastErr)
				break
			}
		}
		if shareFollowWait(ctx, interval) != nil {
			break
		}
	}
	if uploads == 0 {
		if ctx.Err() != nil {
			return errors.New("stopped before the session had anything to share")
		}
		return fmt.Errorf("%s session %s has nothing to share", subject.session.Harness, subject.session.ID)
	}
	if finalErr != nil {
		return finalErr
	}
	return printShareFollow(out, errOut, last, false, true, ended)
}

// shareFollowState is a fingerprint of the session's trace events as they would be uploaded now, and
// whether the session recorded its end.
func shareFollowState(subject shareSubject) (string, bool, error) {
	_, events, err := shareEvents(subject)
	if err != nil {
		return "", false, err
	}
	// A resumed session keeps its old session.ended, so only an end that nothing but Beacon's own
	// bookkeeping follows counts.
	ended, latest := shareSessionEnd(events)
	if !ended && !subject.fromLog {
		ended = shareLogRecordsEnd(subject.session, latest)
	}
	payload, err := buildSharePayload(subject)
	if err != nil {
		return "", ended, err
	}
	// The events alone: the trace body carries the time it was built, which differs on every poll,
	// and everything else in it is folded from these events.
	data, err := json.Marshal(payload.Events)
	if err != nil {
		return "", ended, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), ended, nil
}

func printShareFollow(out, errOut io.Writer, result shareResult, first, final, ended bool) error {
	if shareOpts.jsonOutput {
		return json.NewEncoder(out).Encode(map[string]any{"ok": true, "data": shareFollowUpdate{shareResult: result, Final: final, Ended: ended}})
	}
	switch {
	case first:
		fmt.Fprintln(out, result.SharedURL)
		fmt.Fprintf(errOut, "%s, %d events. External id: %s\n", result.Visibility, result.EventCount, result.ExternalID)
	case final && ended:
		fmt.Fprintf(errOut, "The session ended; %s holds %d events.\n", result.SharedURL, result.EventCount)
	case final:
		fmt.Fprintf(errOut, "Stopped following; %s holds %d events.\n", result.SharedURL, result.EventCount)
	default:
		fmt.Fprintf(errOut, "Uploaded %d event(s); %d in total.\n", result.Uploaded, result.EventCount)
	}
	return nil
}

// shareLogRecordsEnd reports whether the runtime log records the end of session, which most store
// mappers do not emit: the runtime's hooks write session.ended to the log, not to its own store.
// The end counts only when it is the log's newest activity for the session and is no older than
// storeLatest, the newest activity in the store (a resumed session has an old end in both). A log
// that cannot be read says nothing.
func shareLogRecordsEnd(session handoff.Session, storeLatest time.Time) bool {
	found, events, ok, err := handoff.LogSession(shareLogPath(), session.ID, session.Harness)
	if err != nil || !ok || found.ID != session.ID || found.Harness != session.Harness {
		return false
	}
	ended, endAt := shareSessionEnd(events)
	return ended && !endAt.Before(storeLatest)
}

// shareSessionEnd reports whether events, in order, end with session.ended followed by nothing but
// Beacon's bookkeeping (asymptoteobserve.IsBeaconOnlyAction), and the newest timestamp among the
// activity it considered (the end's, when it ended).
func shareSessionEnd(events []schema.Event) (bool, time.Time) {
	ended := false
	var latest time.Time
	for _, e := range events {
		if asymptoteobserve.IsBeaconOnlyAction(e.Event.Action) {
			continue
		}
		ended = e.Event.Action == "session.ended"
		if ts, err := schema.ParseTimestamp(e.Timestamp); err == nil && ts.After(latest) {
			latest = ts
		}
	}
	return ended, latest
}
