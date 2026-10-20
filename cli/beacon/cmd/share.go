package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/account"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/handoff"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/managedprivacy"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/tokens"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/activesession"
)

// `beacon share` publishes one local agent session as a shared trace on beacon.sh. It is the
// only part of the CLI that uploads session content on request, so it is explicit about it: the
// session it picked is named before anything is sent, --print shows the exact payload without a
// network call, a public share asks first, and the content goes through the same sanitizer the
// runtime log uses before it is projected into a beacon.trace.v1 trace
// (asymptoteobserve.BuildShareBundle), the projection the local dashboard shows.
//
// After an upload it records one session.shared event in the session's own timeline, so the local
// trace list and dashboard show the session as shared. That event is inferred bookkeeping: no
// harness collected it.

// errShareNothingYet is a session with no events to share yet. --follow waits for some.
var errShareNothingYet = errors.New("has nothing to share yet")

var (
	shareAccountLoad           = account.Load
	shareHTTPClient            = &http.Client{Timeout: 60 * time.Second}
	shareNow                   = time.Now
	shareGetenv                = os.Getenv
	shareIsTTY                 = func() bool { return isTerminal(os.Stdin) }
	shareStdin       io.Reader = os.Stdin
	// shareGitRemote reads the remote of the repository a session ran in, best effort.
	shareGitRemote = gitRemoteForDir
	// shareActiveSessionsPath is the hooks' record of each runtime's current session per directory.
	shareActiveSessionsPath = activesession.DefaultPath
)

// Environment a runtime's shell commands carry to name the session they run in. Beacon's Claude
// Code SessionStart hook writes both into CLAUDE_ENV_FILE; any other launcher may set them too.
const (
	shareSessionEnv = "BEACON_SESSION_ID"
	shareHarnessEnv = "BEACON_HARNESS"
)

// shareActiveMaxAge is how recently a hook must have seen a session for --current to take it from
// the active-session record. An agent that is running reports on every tool call, so a day is
// generous; anything older is more likely a session that has since ended.
const shareActiveMaxAge = 24 * time.Hour

type shareOptions struct {
	session    string
	sourcePath string
	cwd        string
	harness    string
	visibility string
	title      string
	key        string
	authURL    string
	jsonOutput bool
	follow     bool
	interval   time.Duration
	current    bool
	list       bool
	print      bool
	dryRun     bool
	yes        bool
	limit      int
	logPath    string
	storeDirs  []string
}

var shareOpts shareOptions

var shareCmd = &cobra.Command{
	Use:   "share",
	Short: "Share a local agent session as a trace on beacon.sh",
	Long: `Share one local agent session as a trace on beacon.sh, and print its link.

With no selector (or --current) the session is the one this command runs inside: the session
named by BEACON_SESSION_ID (and BEACON_HARNESS), which Beacon's Claude Code hook exports to the
agent's shell; otherwise the session a Beacon hook last saw in this directory; otherwise the most
recent session in this directory. --harness narrows every step to one runtime. --session,
--source-path or --cwd names another session; --list shows the candidates. It is read from its runtime's own session store
when Beacon reads that runtime's store, and from Beacon's runtime log otherwise.

What is uploaded: the session's prompts, assistant text and reasoning, tool calls with their
arguments, commands, file paths and diffs, and tool output, after the same secret redaction and size
limits the runtime log applies. Paths are rewritten relative to the session's directory, and the
home directory becomes ~. When this endpoint forwards to Beacon Cloud in Metadata-only mode, the
share leaves out what forwarding leaves out: prompts, answers, reasoning, tool arguments and results,
command output, diffs and the derived title. Use --print to see exactly what would be sent without
sending it.

Visibility: direct (the default for a new trace) means anyone with the link; private means only
you; public lists the trace in Discover for anyone, and asks for confirmation first. Re-sharing
a session updates the same trace and keeps its visibility unless --visibility is given.

Sharing needs ` + "`beacon login`" + ` (with the traces:write permission) or a Beacon API key in --key or
BEACON_API_KEY. Hooks never upload anything, except the git pre-push hook after
` + "`beacon git setup --share-sessions`" + `; ` + "`beacon share auto`" + ` asks Beacon Cloud to publish new sessions
from the events it already receives.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         func(cmd *cobra.Command, args []string) error { return shareJSONErrors(cmd, runShare(cmd)) },
}

var shareRefreshCmd = &cobra.Command{
	Use:   "refresh [external-id|session-id]",
	Short: "Upload a shared session's new events",
	Long: `Upload the events a session has gained since it was shared.

The argument is the trace's external id (<runtime>:<session-id>, as ` + "`beacon share --json`" + ` prints
it) or a session id or unique prefix; without one, the session it runs inside, found the way
` + "`beacon share --current`" + ` finds it.
beacon.sh reports the last event it has, and only the events after it are sent (with that event and
any tool call still waiting for its result). Re-sending is
harmless, so a refresh that fails part way can simply be run again. The trace's visibility is not
changed.`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return shareJSONErrors(cmd, runShareRefresh(cmd, args))
	},
}

func init() {
	rootCmd.AddCommand(shareCmd)
	shareCmd.AddCommand(shareRefreshCmd)
	for _, c := range []*cobra.Command{shareCmd, shareRefreshCmd} {
		f := c.Flags()
		f.StringVar(&shareOpts.harness, "harness", "auto", "Only sessions of this runtime ("+strings.Join(handoff.RuntimeNames(), ", ")+"), or auto")
		f.StringVar(&shareOpts.key, "key", "", "Beacon API key (bcn_api_...) to share with instead of your sign-in")
		f.StringVar(&shareOpts.authURL, "auth-url", "", "Beacon service an API key is sent to (defaults to "+account.DefaultBaseURL+", or "+account.BaseURLEnv+")")
		f.BoolVar(&shareOpts.jsonOutput, "json", false, "Print the result as JSON")
		f.StringVar(&shareOpts.logPath, "log-path", "", "Runtime JSONL log to read sessions from and record the share in (default the local runtime log)")
		f.StringArrayVar(&shareOpts.storeDirs, "store-dir", nil, "Read a runtime's sessions from this directory, as <runtime>=<path> (repeatable)")
		f.StringVar(&shareOpts.cwd, "cwd", "", "Use the most recent session that ran in this directory (default: the current directory)")
		f.BoolVar(&shareOpts.print, "print", false, "Print the payload that would be uploaded and send nothing")
		f.BoolVar(&shareOpts.dryRun, "dry-run", false, "Same as --print")
		f.StringVar(&shareOpts.title, "title", "", "Title for the shared trace (default for a new trace: the session's title or first prompt, or none in Metadata-only mode; an existing trace keeps its title)")
	}
	f := shareCmd.Flags()
	f.StringVar(&shareOpts.session, "session", "", "Session id, or a unique prefix of at least 6 characters")
	f.StringVar(&shareOpts.sourcePath, "source-path", "", "The session's file in its runtime's store")
	f.StringVar(&shareOpts.visibility, "visibility", asymptoteobserve.ShareVisibilityDirect, "Who can view the trace: public, direct (anyone with the link) or private")
	f.BoolVar(&shareOpts.current, "current", false, "Share the session this command runs inside (the default with no other selector)")
	f.BoolVar(&shareOpts.list, "list", false, "List the sessions that could be shared, newest first, and share nothing")
	f.IntVar(&shareOpts.limit, "limit", 20, "With --list, the most sessions to show (0 for all)")
	f.BoolVar(&shareOpts.yes, "yes", false, "Share publicly without asking")
	f.BoolVar(&shareOpts.follow, "follow", false, "Keep uploading the session's new events until it ends or Ctrl+C")
	f.DurationVar(&shareOpts.interval, "interval", shareFollowDefaultInterval, "With --follow, how often to check the session for new events")
	shareCmd.MarkFlagsMutuallyExclusive("session", "source-path", "cwd", "current")
}

// shareJSONErrors also reports a failure as {"ok":false,...} on stdout under --json, so a script
// reading stdout always gets a document.
func shareJSONErrors(cmd *cobra.Command, err error) error {
	if err != nil && shareOpts.jsonOutput {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"ok": false, "error": err.Error()})
	}
	return err
}

type shareResult struct {
	TraceID    string `json:"trace_id"`
	ExternalID string `json:"external_id"`
	SharedURL  string `json:"shared_url"`
	Visibility string `json:"visibility"`
	Harness    string `json:"harness"`
	EventCount int    `json:"event_count"`
	// Uploaded is how many events this run sent; on a first share it equals the trace's count.
	Uploaded int `json:"uploaded"`
	// SelectedBy says how the session was chosen: session, source_path, env, active or recent.
	SelectedBy string `json:"selected_by,omitempty"`
	// SharedThrough is when the session was read for this upload: an event logged after it was
	// not sent, however soon the upload finished. The session.shared record carries it as
	// shared_through, which is what the dashboard compares later activity against.
	SharedThrough time.Time `json:"-"`
	// sharedAt is when beacon.sh created the trace, for a refresh or re-share to record as the
	// share date (empty when unknown).
	sharedAt string
}

// sharePayload is what --print shows: the external id the requests go to, the body of the PUT
// request (trace) and every event the batch requests would carry (events), in order.
type sharePayload struct {
	ExternalID string                          `json:"external_id"`
	Trace      asymptoteobserve.ShareTraceV1   `json:"trace"`
	Events     []asymptoteobserve.TraceEventV1 `json:"events"`
}

// harness is the normalized runtime name a trace's summary carries.
func (p sharePayload) harness() string { return p.Trace.Summary.Harness.Name }

func (o shareOptions) printOnly() bool { return o.print || o.dryRun }

func runShare(cmd *cobra.Command) error {
	if err := validateShareFlags(cmd); err != nil {
		return err
	}
	harness, err := shareHarness()
	if err != nil {
		return err
	}
	if shareOpts.list {
		return runShareList(cmd, harness)
	}
	subject, err := resolveShareSubject(harness, shareOpts.session)
	if err != nil {
		return err
	}
	readAt := shareNow()
	payload, err := buildSharePayload(subject)
	if err != nil && !(shareOpts.follow && errors.Is(err, errShareNothingYet)) {
		return err
	}
	if cmd.Flags().Changed("visibility") {
		payload.Trace.Visibility = shareOpts.visibility
	}
	errOut := cmd.ErrOrStderr()
	if shareOpts.printOnly() {
		return writeIndentedJSON(cmd.OutOrStdout(), payload)
	}
	if payload.Trace.Visibility == asymptoteobserve.ShareVisibilityPublic && !shareOpts.yes {
		if err := confirmPublicShare(errOut, subject.session, len(payload.Events)); err != nil {
			return err
		}
	}
	client, err := newShareClient()
	if err != nil {
		return err
	}
	if shareOpts.follow {
		visibility := ""
		if cmd.Flags().Changed("visibility") {
			visibility = shareOpts.visibility
		}
		return runShareFollow(cmd, client, subject, visibility)
	}
	if !shareOpts.jsonOutput {
		fmt.Fprintf(errOut, "Sharing %s session %s (%d events)%s.\n", subject.session.Harness, subject.session.ID, len(payload.Events), shareSourceNote(subject))
	}
	ctx := commandContext(cmd)
	state, found, err := client.GetTrace(ctx, payload.ExternalID)
	if err != nil {
		return err
	}
	if found {
		if subject, payload, err = alignShareSource(subject, payload, state); err != nil {
			return err
		}
		if cmd.Flags().Changed("visibility") {
			payload.Trace.Visibility = shareOpts.visibility
		}
		keepSharedTitle(cmd, &payload)
	}
	trace, err := client.PutTrace(ctx, payload.ExternalID, payload.Trace)
	if err != nil {
		return err
	}
	uploaded, err := client.PostEvents(ctx, payload.ExternalID, asymptoteobserve.ShareEventsBatchV1{Events: payload.Events})
	if err != nil {
		return fmt.Errorf("the trace was created at %s, but uploading its events failed (run the same command again to retry): %w", trace.URL, err)
	}
	result := shareResult{
		TraceID:       trace.ID,
		ExternalID:    payload.ExternalID,
		SharedURL:     trace.URL,
		Visibility:    trace.Visibility,
		Harness:       payload.harness(),
		EventCount:    uploaded.EventCount,
		Uploaded:      len(payload.Events),
		SelectedBy:    subject.selectedBy,
		SharedThrough: readAt,
	}
	if uploaded.Batches == 0 {
		result.EventCount = trace.EventCount
	}
	// Sharing a trace that already exists updates it: the share date stays the trace's own.
	if found {
		result.sharedAt = state.CreatedAt
	}
	if err := recordShareEvent(subject.session, result, found); err != nil {
		fmt.Fprintf(errOut, "warning: the session was shared, but the runtime log was not updated: %v\n", err)
	}
	return printShareResult(cmd, result)
}

func runShareRefresh(cmd *cobra.Command, args []string) error {
	harness, err := shareHarness()
	if err != nil {
		return err
	}
	id := ""
	if len(args) == 1 {
		id = strings.TrimSpace(args[0])
		if shareOpts.cwd != "" {
			return errors.New("pass a session or --cwd, not both")
		}
		if h, sessionID, ok := parseShareExternalID(id); ok {
			if harness != "" && h != harness {
				return fmt.Errorf("%s is a %s trace, not %s", id, h, harness)
			}
			harness, id = h, sessionID
		} else if strings.Contains(id, ":sha256-") {
			return fmt.Errorf("%s names a session by a digest of its id; pass the session id instead", id)
		}
	}
	subject, err := resolveShareSubject(harness, id)
	if err != nil {
		return err
	}
	readAt := shareNow()
	payload, err := buildSharePayload(subject)
	if err != nil {
		return err
	}
	if shareOpts.printOnly() {
		return writeIndentedJSON(cmd.OutOrStdout(), payload)
	}
	client, err := newShareClient()
	if err != nil {
		return err
	}
	ctx := commandContext(cmd)
	state, found, err := client.GetTrace(ctx, payload.ExternalID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s is not shared yet: run `beacon share --session %s` first", payload.ExternalID, subject.session.ID)
	}
	if subject, payload, err = alignShareSource(subject, payload, state); err != nil {
		return err
	}
	keepSharedTitle(cmd, &payload)
	pending := asymptoteobserve.ShareEventsAfter(payload.Events, state.LastEventID, state.MaxNumber)
	trace, err := client.PutTrace(ctx, payload.ExternalID, payload.Trace)
	if err != nil {
		return err
	}
	uploaded, err := client.PostEvents(ctx, payload.ExternalID, asymptoteobserve.ShareEventsBatchV1{Events: pending})
	if err != nil {
		return err
	}
	result := shareResult{
		TraceID:       trace.ID,
		ExternalID:    payload.ExternalID,
		SharedURL:     trace.URL,
		Visibility:    trace.Visibility,
		Harness:       payload.harness(),
		EventCount:    state.EventCount,
		Uploaded:      len(pending),
		SelectedBy:    subject.selectedBy,
		SharedThrough: readAt,
	}
	if uploaded.Batches > 0 {
		result.EventCount = uploaded.EventCount
	}
	result.sharedAt = state.CreatedAt
	if err := recordShareEvent(subject.session, result, true); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: the trace was refreshed, but the runtime log was not updated: %v\n", err)
	}
	return printShareResult(cmd, result)
}

func validateShareFlags(cmd *cobra.Command) error {
	if !asymptoteobserve.ValidShareVisibility(shareOpts.visibility) {
		return fmt.Errorf("--visibility must be public, direct or private, not %q", shareOpts.visibility)
	}
	if shareOpts.list {
		switch {
		case shareOpts.session != "" || shareOpts.sourcePath != "" || shareOpts.current:
			return errors.New("--list shows candidates; combine it with --cwd or --harness, not --session, --source-path or --current")
		case shareOpts.printOnly():
			return errors.New("--list and --print cannot be combined")
		case shareOpts.follow:
			return errors.New("--list and --follow cannot be combined")
		}
	}
	if shareOpts.follow && shareOpts.printOnly() {
		return errors.New("--follow uploads; it cannot be combined with --print")
	}
	if shareOpts.limit < 0 {
		return errors.New("--limit cannot be negative")
	}
	return nil
}

func shareHarness() (string, error) {
	value := strings.TrimSpace(shareOpts.harness)
	if value == "" || strings.EqualFold(value, "auto") {
		return "", nil
	}
	return handoff.ParseHarness(value)
}

func shareLogPath() string {
	return lifecycle.ResolveRuntimeLog(true, shareOpts.logPath).EffectiveLogPath
}

func shareStoreDirs() (handoff.StoreDirs, error) {
	dirs := handoff.StoreDirs{}
	for _, entry := range shareOpts.storeDirs {
		name, dir, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(name) == "" || dir == "" {
			return nil, fmt.Errorf("--store-dir %q: want <runtime>=<path>", entry)
		}
		harness, err := handoff.ParseHarness(name)
		if err != nil {
			return nil, fmt.Errorf("--store-dir %q: %w", entry, err)
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		dirs[harness] = abs
	}
	return dirs, nil
}

// shareDirectory is the directory --cwd names, absolute, or the current one.
func shareDirectory() (string, error) {
	dir := strings.TrimSpace(shareOpts.cwd)
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve --cwd %s: %w", dir, err)
	}
	return abs, nil
}

// alignShareSource keeps a re-share or refresh on the source the trace was first built from. The
// same session read from its runtime's store and from Beacon's runtime log has different event ids,
// so uploading the other source would add a second copy of every event. When the server's last
// event is not in this projection, the other source is tried; when neither has it, the upload is
// refused rather than duplicated.
func alignShareSource(subject shareSubject, payload sharePayload, state *asymptoteobserve.ShareTraceStateV1) (shareSubject, sharePayload, error) {
	if state == nil || state.LastEventID == "" || hasShareEvent(payload.Events, state.LastEventID) {
		return subject, payload, nil
	}
	var other shareSubject
	if subject.fromLog {
		// A session found in the log carries no store path, which store readers match on, so the
		// store's own listing has to name it.
		if dirs, err := shareStoreDirs(); err == nil {
			if session, err := handoff.Find(handoffSources(dirs), subject.session.Harness, subject.session.ID); err == nil {
				other = shareSubject{session: session}
			}
		}
	} else if logSubject, err := resolveShareLog(subject.session); err == nil {
		other = logSubject
	}
	if other.session.ID != "" {
		if otherPayload, err := buildSharePayload(other); err == nil && hasShareEvent(otherPayload.Events, state.LastEventID) {
			return other, otherPayload, nil
		}
	}
	return subject, payload, fmt.Errorf("%s on beacon.sh was built from a different copy of this session than Beacon can read now (its store and the runtime log name events differently), so uploading it would duplicate every event; delete the shared trace and share it again to start over", payload.ExternalID)
}

// keepSharedTitle leaves an existing trace's title alone unless --title names a new one: the
// derived title would otherwise replace one set with --title or renamed on beacon.sh. An omitted
// title keeps the stored one, as an omitted visibility does.
func keepSharedTitle(cmd *cobra.Command, payload *sharePayload) {
	if f := cmd.Flags().Lookup("title"); f == nil || !f.Changed {
		payload.Trace.Title = ""
	}
}

func hasShareEvent(events []asymptoteobserve.TraceEventV1, id string) bool {
	for _, e := range events {
		if e.ID == id {
			return true
		}
	}
	return false
}

// shareSubject is a session to share and, when it came from the runtime log, its events.
type shareSubject struct {
	session   handoff.Session
	fromLog   bool
	logEvents []schema.Event
	// selectedBy says how the session was chosen (see shareResult.SelectedBy).
	selectedBy string
}

func shareSourceNote(subject shareSubject) string {
	if subject.fromLog {
		return " from Beacon's runtime log"
	}
	return ""
}

// resolveShareSubject finds the session to share: by id (prefix ok), by its store file, or as the
// most recent session in a directory. Stores come first, then the runtime log.
func resolveShareSubject(harness, id string) (shareSubject, error) {
	dirs, err := shareStoreDirs()
	if err != nil {
		return shareSubject{}, err
	}
	sources := handoffSources(dirs)
	switch {
	case id != "":
		subject, err := resolveShareID(sources, harness, id)
		subject.selectedBy = "session"
		return subject, err
	case strings.TrimSpace(shareOpts.sourcePath) != "":
		subject, err := resolveShareSourcePath(sources, harness, shareOpts.sourcePath)
		subject.selectedBy = "source_path"
		return subject, err
	default:
		return resolveShareCurrent(sources, harness)
	}
}

// resolveShareID finds a session by id or unique prefix: in the runtimes' stores, then in the
// runtime log.
func resolveShareID(sources []handoff.Source, harness, id string) (shareSubject, error) {
	session, findErr := handoff.Find(sources, harness, id)
	if findErr == nil {
		return shareSubject{session: session}, nil
	}
	// A prefix that names several sessions is the person's to narrow: falling back to the log
	// would share whichever of them the log happens to hold.
	var ambiguous *handoff.AmbiguousError
	if errors.As(findErr, &ambiguous) {
		return shareSubject{}, findErr
	}
	// Not in any store, or a store could not be read: the runtime log may have the session.
	var notFound *handoff.NotFoundError
	isNotFound := errors.As(findErr, &notFound)
	logPath := shareLogPath()
	session, events, found, err := handoff.LogSession(logPath, id, harness)
	if err != nil {
		if !isNotFound {
			return shareSubject{}, findErr
		}
		return shareSubject{}, fmt.Errorf("read runtime log %s: %w", logPath, err)
	}
	if !found || (harness != "" && session.Harness != harness) {
		if !isNotFound {
			return shareSubject{}, findErr
		}
		return shareSubject{}, fmt.Errorf("%w; not in the runtime log %s either (`beacon share --list` shows what is available)", notFound, logPath)
	}
	return shareSubject{session: session, fromLog: true, logEvents: events}, nil
}

// resolveShareCurrent finds the session this command runs inside, in order:
//
//  1. BEACON_SESSION_ID (with BEACON_HARNESS), which the Claude Code hook exports to the agent's
//     shell. Skipped under --cwd, which names another directory, and when --harness names another
//     runtime (a Codex started from a Claude Code shell inherits Claude's variables), and refused
//     without --harness when something here contradicts them (shareEnvContradiction). An id that is
//     set but cannot be found, or set with a missing or unknown BEACON_HARNESS, is an error rather
//     than a reason to share some other session.
//  2. The session a Beacon hook last saw in the directory, from the active-session record.
//  3. The most recent session in the directory.
func resolveShareCurrent(sources []handoff.Source, harness string) (shareSubject, error) {
	dir, err := shareDirectory()
	if err != nil {
		return shareSubject{}, err
	}
	if strings.TrimSpace(shareOpts.cwd) == "" {
		if id := strings.TrimSpace(shareGetenv(shareSessionEnv)); id != "" {
			// The two variables name one session together. A runtime name that is missing or
			// unknown is an error, never a reason to share some other session; only --harness naming
			// a different runtime passes over them on purpose.
			rawHarness := strings.TrimSpace(shareGetenv(shareHarnessEnv))
			if rawHarness == "" {
				return shareSubject{}, fmt.Errorf("%s=%s is set but %s is not; set both, or name the session with --session", shareSessionEnv, id, shareHarnessEnv)
			}
			envHarness, herr := handoff.ParseHarness(rawHarness)
			if herr != nil {
				return shareSubject{}, fmt.Errorf("%s=%q: %w", shareHarnessEnv, rawHarness, herr)
			}
			if harness == "" || envHarness == harness {
				subject, err := resolveShareID(sources, envHarness, id)
				if err != nil {
					return shareSubject{}, fmt.Errorf("%s=%s names this session, but it cannot be read yet: %w", shareSessionEnv, id, err)
				}
				// Without --harness the variables are only a claim: a runtime started from a Claude
				// Code shell (Codex, OpenCode, Cursor) inherits them, and sharing them would publish
				// the parent's session. Refuse when anything here contradicts them.
				if harness == "" {
					if err := shareEnvContradiction(sources, dir, subject); err != nil {
						return shareSubject{}, err
					}
				}
				subject.selectedBy = "env"
				return subject, nil
			}
		}
	}
	candidates, err := shareCandidateList(sources, harness, dir, 1)
	if err != nil {
		return shareSubject{}, err
	}
	if entry, ok := shareActiveSession(dir, harness); ok && shareActiveIsNewest(entry, candidates) {
		// A record whose session cannot be read (a runtime Beacon reads only from the log, whose
		// events have rotated away) falls through to the directory's most recent session.
		if subject, err := resolveShareID(sources, entry.Harness, entry.SessionID); err == nil {
			subject.selectedBy = "active"
			return subject, nil
		}
	}
	if len(candidates) == 0 {
		return shareSubject{}, shareNoSessionError(sources, harness, dir)
	}
	// A session listed by its runtime's store is read from that store; one the store has lost or
	// cannot read since falls back to the log when its events are read (shareEvents). A session
	// only the log listed is read from the log: its store may be unreadable.
	candidate := candidates[0]
	if !candidate.fromLog && handoff.ReadsStore(candidate.session.Harness) {
		return shareSubject{session: candidate.session, selectedBy: "recent"}, nil
	}
	subject, err := resolveShareLog(candidate.session)
	subject.selectedBy = "recent"
	return subject, err
}

// shareEnvContradiction is the error when the session BEACON_SESSION_ID names may not be the one
// this command runs inside: it ran in another directory (neither this one nor a parent of it), or
// another runtime has worked in this directory since that session was last active, which is what an
// agent started from that session's shell looks like. Refusing is safer than guessing which
// session to publish; --harness or --session settles it.
func shareEnvContradiction(sources []handoff.Source, dir string, subject shareSubject) error {
	session := subject.session
	advice := "pass --harness <runtime> for the runtime you are in, or --session <id>"
	if session.Directory != "" && !handoff.DirectoryWithin(dir, session.Directory) {
		return fmt.Errorf("%s=%s names %s session %s, which ran in %s, not in %s or a directory above it; a runtime started from that session's shell inherits its variables: %s",
			shareSessionEnv, session.ID, session.Harness, session.ID, session.Directory, dir, advice)
	}
	newer := func(harness, id string, at time.Time) error {
		return fmt.Errorf("%s=%s names %s session %s, but %s session %s worked in %s more recently (%s); a runtime started from a %s shell inherits its variables: %s",
			shareSessionEnv, session.ID, session.Harness, session.ID, harness, id, dir, at.Local().Format(time.RFC3339), session.Harness, advice)
	}
	if entries, err := activesession.Read(shareActiveSessionsPath()); err == nil {
		now := shareNow()
		for _, e := range entries {
			if !handoff.SameDirectory(e.WorkingDirectory, dir) || now.Sub(e.LastSeen) > shareActiveMaxAge {
				continue
			}
			name, err := handoff.ParseHarness(e.Harness)
			if err != nil || name == session.Harness || !e.LastSeen.After(session.UpdatedAt) {
				continue
			}
			return newer(name, e.SessionID, e.LastSeen)
		}
	}
	candidates, _ := shareCandidateList(sources, "", dir, 0)
	for _, c := range candidates {
		if c.session.Harness != session.Harness && c.session.UpdatedAt.After(session.UpdatedAt) {
			return newer(c.session.Harness, c.session.ID, c.session.UpdatedAt)
		}
	}
	return nil
}

// shareActiveIsNewest reports whether the active-session entry may stand for this directory's
// session: only hooks write the record, so a runtime Beacon sees only through OTLP or a store (an
// OTLP-only or poll runtime) never updates it, and an older entry would otherwise win over that
// runtime's newer session. The entry is used when it is the newest candidate itself, or was seen at
// least as recently as the newest candidate was updated.
func shareActiveIsNewest(entry activesession.Entry, candidates []shareCandidate) bool {
	if len(candidates) == 0 {
		return true
	}
	newest := candidates[0].session
	if newest.Harness == entry.Harness && newest.ID == entry.SessionID {
		return true
	}
	return !entry.LastSeen.Before(newest.UpdatedAt)
}

func resolveShareLog(session handoff.Session) (shareSubject, error) {
	logPath := shareLogPath()
	found, events, ok, err := handoff.LogSession(logPath, session.ID, session.Harness)
	if err != nil {
		return shareSubject{}, fmt.Errorf("read runtime log %s: %w", logPath, err)
	}
	if !ok {
		return shareSubject{}, fmt.Errorf("%s session %s is not in the runtime log %s", session.Harness, session.ID, logPath)
	}
	return shareSubject{session: found, fromLog: true, logEvents: events}, nil
}

func resolveShareSourcePath(sources []handoff.Source, harness, path string) (shareSubject, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return shareSubject{}, err
	}
	if _, err := os.Stat(abs); err != nil {
		return shareSubject{}, fmt.Errorf("--source-path %s: %w", path, err)
	}
	resolved, _ := filepath.EvalSymlinks(abs)
	sessions, listErr := handoff.List(sources, handoff.Filter{Harness: harness, IncludeSubagents: true})
	for _, session := range sessions {
		if session.SourcePath == "" {
			continue
		}
		if session.SourcePath == abs || (resolved != "" && evalSymlinks(session.SourcePath) == resolved) {
			return shareSubject{session: session}, nil
		}
	}
	if listErr != nil {
		return shareSubject{}, fmt.Errorf("no session store lists %s (some stores could not be read: %v)", path, listErr)
	}
	return shareSubject{}, fmt.Errorf("no session store Beacon reads lists %s as a session file", path)
}

func evalSymlinks(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}

// shareActiveSession is the session a Beacon hook last saw in dir, limited to harness when it is
// set, from the active-session record. Entries older than shareActiveMaxAge are ignored.
func shareActiveSession(dir, harness string) (activesession.Entry, bool) {
	entries, err := activesession.Read(shareActiveSessionsPath())
	if err != nil {
		return activesession.Entry{}, false
	}
	now := shareNow()
	for _, e := range entries {
		if !handoff.SameDirectory(e.WorkingDirectory, dir) || now.Sub(e.LastSeen) > shareActiveMaxAge {
			continue
		}
		name, err := handoff.ParseHarness(e.Harness)
		if err != nil || (harness != "" && name != harness) {
			continue
		}
		e.Harness = name
		return e, true
	}
	return activesession.Entry{}, false
}

// shareNoSessionError explains that nothing ran in dir and names the most recent sessions anywhere,
// so the next command can name one.
func shareNoSessionError(sources []handoff.Source, harness, dir string) error {
	msg := fmt.Sprintf("no agent session found in %s", dir)
	stored, _ := handoff.List(sources, handoff.Filter{Harness: harness, Limit: 5})
	logged, _ := handoff.LogSessions(shareLogPath(), handoff.Filter{Harness: harness, Limit: 5})
	seen := map[string]bool{}
	var all []handoff.Session
	for _, s := range append(stored, logged...) {
		if key := s.Harness + "\x00" + s.ID; !seen[key] {
			seen[key] = true
			all = append(all, s)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].UpdatedAt.After(all[j].UpdatedAt) })
	if len(all) > 5 {
		all = all[:5]
	}
	if len(all) == 0 {
		return errors.New(msg + "; Beacon has no sessions recorded yet (is `beacon endpoint install` set up for this runtime?)")
	}
	var b strings.Builder
	b.WriteString(msg + "; recent sessions elsewhere (pass one with --session, or use --cwd DIR):")
	for _, s := range all {
		fmt.Fprintf(&b, "\n  %s %s  %s", s.Harness, s.ID, s.Directory)
	}
	return errors.New(b.String())
}

// shareCandidates lists the sessions in dir, newest first: those in the runtimes' own stores, then
// sessions only Beacon's runtime log has. A store that cannot be read is skipped; the log still
// answers for it.
func shareCandidates(sources []handoff.Source, harness, dir string, limit int) ([]handoff.Session, error) {
	candidates, err := shareCandidateList(sources, harness, dir, limit)
	if err != nil {
		return nil, err
	}
	out := make([]handoff.Session, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.session)
	}
	return out, nil
}

// shareCandidate is a listed session and whether only the runtime log listed it.
type shareCandidate struct {
	session handoff.Session
	fromLog bool
}

// shareCandidateList is shareCandidates with each session's origin, so a session only the log has
// (its store lost it, or cannot be read) is read from the log rather than sent back to the store.
func shareCandidateList(sources []handoff.Source, harness, dir string, limit int) ([]shareCandidate, error) {
	stored, _ := handoff.List(sources, handoff.Filter{Harness: harness, Directory: dir})
	logged, logErr := handoff.LogSessions(shareLogPath(), handoff.Filter{Harness: harness, Directory: dir})
	if logErr != nil && !errors.Is(logErr, os.ErrNotExist) && len(stored) == 0 {
		return nil, fmt.Errorf("read runtime log: %w", logErr)
	}
	seen := map[string]bool{}
	var out []shareCandidate
	for _, s := range stored {
		seen[s.Harness+"\x00"+s.ID] = true
		out = append(out, shareCandidate{session: s})
	}
	for _, s := range logged {
		if !seen[s.Harness+"\x00"+s.ID] {
			out = append(out, shareCandidate{session: s, fromLog: true})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].session.UpdatedAt.After(out[j].session.UpdatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func runShareList(cmd *cobra.Command, harness string) error {
	dirs, err := shareStoreDirs()
	if err != nil {
		return err
	}
	dir, err := shareDirectory()
	if err != nil {
		return err
	}
	sessions, err := shareCandidates(handoffSources(dirs), harness, dir, shareOpts.limit)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if shareOpts.jsonOutput {
		if sessions == nil {
			sessions = []handoff.Session{}
		}
		return json.NewEncoder(out).Encode(map[string]any{"ok": true, "data": map[string]any{"directory": dir, "sessions": sessions}})
	}
	printHandoffSessions(out, sessions, shareNow())
	return nil
}

// shareEvents reads the subject's events: from its runtime's store, or from the runtime log when the
// session came from there or its store has lost it since it was listed.
func shareEvents(subject shareSubject) (shareSubject, []schema.Event, error) {
	if subject.fromLog {
		return subject, subject.logEvents, nil
	}
	dirs, err := shareStoreDirs()
	if err != nil {
		return subject, nil, err
	}
	events, err := handoff.Events(handoffSources(dirs), subject.session)
	if err == nil {
		return subject, events, nil
	}
	// The store lost the session, or cannot be read (locked, corrupt, no permission): the runtime
	// log may still have it.
	logSubject, logErr := resolveShareLog(subject.session)
	if logErr == nil && !errors.Is(err, handoff.ErrNotFound) && !shareHasProjectable(logSubject.logEvents) {
		// The log holds nothing of the session but Beacon's own bookkeeping (a session.shared
		// record from an earlier upload): it is no copy of the session, so the store's error stands.
		logErr = fmt.Errorf("%s session %s is not in the runtime log %s", subject.session.Harness, subject.session.ID, shareLogPath())
	}
	if logErr != nil {
		if errors.Is(err, handoff.ErrNotFound) {
			return subject, nil, logErr
		}
		return subject, nil, fmt.Errorf("read %s session %s: %w (and the runtime log: %v)", subject.session.Harness, subject.session.ID, err, logErr)
	}
	return logSubject, logSubject.logEvents, nil
}

// shareHasProjectable reports whether any of events would be carried by a shared trace.
func shareHasProjectable(events []schema.Event) bool {
	for _, event := range events {
		if asymptoteobserve.ShareProjects(event) {
			return true
		}
	}
	return false
}

// shareMetadataOnly reports whether this endpoint forwards to Beacon Cloud in Metadata-only mode
// (user or system mode), or has a system-mode privacy mode this user cannot read. A share then
// leaves out what forwarding leaves out, so publishing a session never sends more than the
// endpoint's privacy mode allows.
func shareMetadataOnly() bool {
	_, mode := shareAutoCloud()
	return mode == managedprivacy.MetadataOnly
}

func buildSharePayload(subject shareSubject) (sharePayload, error) {
	subject, events, err := shareEvents(subject)
	if err != nil {
		return sharePayload{}, err
	}
	session := subject.session
	sort.SliceStable(events, func(i, j int) bool { return shareEventTime(events[i]).Before(shareEventTime(events[j])) })
	metadataOnly := shareMetadataOnly()
	bundle := asymptoteobserve.BuildShareBundle(events, asymptoteobserve.ShareOptions{
		WorkingDirectory: session.Directory,
		BeaconVersion:    version.GetVersion(),
		GeneratedAt:      shareNow(),
		MetadataOnly:     metadataOnly,
	})
	if len(bundle.Events) == 0 {
		return sharePayload{}, fmt.Errorf("%s session %s %w", session.Harness, session.ID, errShareNothingYet)
	}
	// Visibility is set only when the person names one, so a re-share keeps the trace's own.
	trace := asymptoteobserve.NewShareTrace(bundle, "")
	if trace.Summary.Harness.Name == "" {
		trace.Summary.Harness.Name = session.Harness
	}
	if name := asymptoteobserve.NormalizeHarnessName(trace.Summary.Harness.Name); name != "" {
		trace.Summary.Harness.Name = name
	}
	switch {
	case strings.TrimSpace(shareOpts.title) != "":
		trace.Title = asymptoteobserve.ShareTitle(shareOpts.title)
	case metadataOnly:
		// Most runtimes title a session with its first prompt, which Metadata-only keeps off the
		// trace like every other prompt. Only a title the person typed is sent.
		trace.Title = ""
	default:
		if title := asymptoteobserve.ShareTitle(session.Title); title != "" {
			trace.Title = asymptoteobserve.RedactString(title)
		}
	}
	shareSummaryFromSession(&trace.Summary, session)
	if session.Subagent && session.ParentID != "" {
		trace.RelatedTo = asymptoteobserve.ShareExternalID(session.Harness, session.ParentID)
		trace.RelationshipType = asymptoteobserve.ShareRelationshipSubagent
	} else {
		trace.RelatedTo, trace.RelationshipType = asymptoteobserve.ShareRelation(events)
	}
	// The token aggregator resolves cumulative metric series and live-versus-backfill overlap,
	// which the summary's plain sum of every event does not. Cost is what runtimes reported, never
	// an estimate.
	totals := tokens.Aggregate(events, tokens.Options{}).Totals
	usage := asymptoteobserve.TraceUsageV1{
		InputTokens:                totals.InputTokens,
		OutputTokens:               totals.OutputTokens,
		CacheReadInputTokens:       totals.CacheReadInputTokens,
		CacheCreationInputTokens:   totals.CacheCreationInputTokens,
		CacheCreation1hInputTokens: totals.CacheCreation1hInputTokens,
		ReasoningOutputTokens:      totals.ReasoningOutputTokens,
		CostUSD:                    totals.CostUSD,
	}
	trace.Summary.TokenUsage = nil
	if usage != (asymptoteobserve.TraceUsageV1{}) {
		trace.Summary.TokenUsage = &usage
	}
	return sharePayload{
		ExternalID: asymptoteobserve.ShareExternalID(session.Harness, session.ID),
		Trace:      trace,
		Events:     bundle.Events,
	}, nil
}

// shareSummaryFromSession fills what the events did not say from the session listing and the
// repository it ran in: its branch, its origin remote (credentials removed), and the project's name
// from that remote when the events named only the directory.
func shareSummaryFromSession(summary *asymptoteobserve.TraceSummaryV1, session handoff.Session) {
	repo := summary.Repository
	if repo == nil {
		repo = &asymptoteobserve.TraceRepositoryV1{}
	}
	if repo.Branch == "" {
		repo.Branch = asymptoteobserve.ShareBranch(session.Branch)
	}
	if repo.RemoteURL == "" && session.Directory != "" {
		repo.RemoteURL = asymptoteobserve.ShareRemoteURL(shareGitRemote(session.Directory))
		if summary.Project == nil || summary.Project.Name == filepath.Base(session.Directory) {
			if name := strings.TrimSuffix(lastURLSegment(repo.RemoteURL), ".git"); name != "" {
				summary.Project = &asymptoteobserve.TraceProjectV1{Name: name}
			}
		}
	}
	if *repo == (asymptoteobserve.TraceRepositoryV1{}) {
		summary.Repository = nil
	} else {
		summary.Repository = repo
	}
}

func shareEventTime(event schema.Event) time.Time {
	ts, err := schema.ParseTimestamp(event.Timestamp)
	if err != nil {
		return time.Time{}
	}
	return ts
}

func lastURLSegment(value string) string {
	value = strings.TrimRight(value, "/")
	if i := strings.LastIndexAny(value, "/:"); i >= 0 {
		return value[i+1:]
	}
	return ""
}

// gitRemoteForDir reads the origin remote of the repository at dir, or "" when there is none. It
// is a local read of git's config; nothing is fetched.
func gitRemoteForDir(dir string) string {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func confirmPublicShare(errOut io.Writer, session handoff.Session, events int) error {
	if !shareIsTTY() || isCIEnvironment() {
		return errors.New("a public trace is listed for anyone to find; pass --yes to share publicly without a terminal to confirm")
	}
	fmt.Fprintf(errOut, "Share %s session %s (%d events) publicly? Anyone can find and read it in Discover. [y/N] ", session.Harness, session.ID, events)
	if !confirmNo(shareStdin) {
		return errors.New("not shared")
	}
	return nil
}

func newShareClient() (*account.ShareClient, error) {
	cred, err := account.ResolveShareCredential(account.ResolveShareCredentialOptions{
		Key:     shareOpts.key,
		BaseURL: shareOpts.authURL,
		Load:    shareAccountLoad,
		Getenv:  shareGetenv,
		Now:     shareNow(),
	})
	if err != nil {
		return nil, err
	}
	return account.NewShareClient(cred, shareHTTPClient)
}

func parseShareExternalID(value string) (harness, sessionID string, ok bool) {
	name, rest, found := strings.Cut(value, ":")
	if !found || rest == "" || strings.HasPrefix(rest, "sha256-") || !asymptoteobserve.ValidShareExternalID(value) {
		return "", "", false
	}
	h, err := handoff.ParseHarness(name)
	if err != nil {
		return "", "", false
	}
	return h, rest, true
}

// recordShareEvent writes the session.shared event into the session's own timeline, with the
// sharing fields the trace list reads (shared_url, visibility, shared_at, last_refreshed_at).
func recordShareEvent(session handoff.Session, result shareResult, refresh bool) error {
	event := shareRecordEvent(session, result, refresh, shareNow())
	_, err := writer.AppendEvent(event, writer.Options{Path: shareLogPath(), UserMode: true})
	return err
}

func shareRecordEvent(session handoff.Session, result shareResult, refresh bool, now time.Time) schema.Event {
	verb := "shared"
	if refresh {
		verb = "refreshed"
	}
	event := schema.NewEvent(schema.NewEventOptions{
		Action:       asymptoteobserve.ActionSessionShared,
		Category:     "session",
		Severity:     schema.SeverityInfo,
		AgentVersion: version.GetVersion(),
		Harness:      schema.HarnessInfo{Name: session.Harness},
		Fidelity:     asymptoteobserve.FidelityInferred,
		Message:      fmt.Sprintf("session %s to beacon.sh (%s, %d events)", verb, result.Visibility, result.EventCount),
	})
	event.Timestamp = schema.FormatTimestamp(now)
	event.Session = &schema.SessionInfo{ID: session.ID, WorkingDirectory: session.Directory}
	stamp := now.UTC().Format(time.RFC3339)
	event.Raw = map[string]interface{}{
		"shared_url":        result.SharedURL,
		"visibility":        result.Visibility,
		"shared_trace_id":   result.TraceID,
		"external_id":       result.ExternalID,
		"last_refreshed_at": stamp,
	}
	if !refresh {
		event.Raw["shared_at"] = stamp
	} else if created, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(result.sharedAt)); err == nil {
		// A refresh keeps the share date: the trace's creation on beacon.sh, which also stands in
		// for a first record that has rotated out of the log.
		event.Raw["shared_at"] = created.UTC().Format(time.RFC3339)
	}
	if !result.SharedThrough.IsZero() {
		event.Raw["shared_through"] = schema.FormatTimestamp(result.SharedThrough)
	}
	return event
}

func printShareResult(cmd *cobra.Command, result shareResult) error {
	out := cmd.OutOrStdout()
	if shareOpts.jsonOutput {
		return json.NewEncoder(out).Encode(map[string]any{"ok": true, "data": result})
	}
	fmt.Fprintln(out, result.SharedURL)
	fmt.Fprintf(cmd.ErrOrStderr(), "%s, %d events (%d sent). External id: %s\n", result.Visibility, result.EventCount, result.Uploaded, result.ExternalID)
	return nil
}
