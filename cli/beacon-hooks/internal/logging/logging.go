package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	osuser "os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/activesession"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/filelock"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/shareauto"
)

// endpointSequence numbers the endpoint events emitted by this hook process, in the order
// they are written.
var endpointSequence asymptoteobserve.Sequencer

const (
	defaultEndpointRotateBytes    = 10 * 1024 * 1024
	defaultEndpointRotateArchives = asymptoteobserve.DefaultRuntimeLogRotateArchives
	// endpointRuntimeFileMode is the shared (system-mode) log's mode. A log inside the current
	// user's home is private instead; asymptoteobserve.RuntimeLogModes decides, identically in all
	// three writers.
	endpointRuntimeFileMode = asymptoteobserve.SharedRuntimeLogFileMode
)

// endpointEventsSuppressed stops this process writing endpoint events. A hook sets it when another
// hook of Beacon's records the same event, as when Cursor runs Beacon's Claude Code hooks next to
// its own.
var endpointEventsSuppressed bool

// SuppressEndpointEvents turns endpoint event writes off (true) or back on (false) for the rest of
// this process.
func SuppressEndpointEvents(suppress bool) {
	endpointEventsSuppressed = suppress
}

// Logger provides structured JSON logging for hook events
type Logger struct {
	hookName  string
	logFile   string
	sessionID string
	platform  string
}

// NewLoggerForPlatform creates a logger for a specific hook and platform
func NewLoggerForPlatform(hookName, platform string) *Logger {
	config.EnsureStateDir(platform)
	return &Logger{hookName: hookName, logFile: config.GetLogFile(platform), platform: platform}
}

// NewSessionLogger creates a logger that writes to a per-session log file.
// Log file path: ~/.beacon/{platform}/logs/{session_id}.log
func NewSessionLogger(hookName, platform, sessionID string) *Logger {
	config.EnsureSessionLogDir(platform)
	return &Logger{
		hookName:  hookName,
		logFile:   config.GetSessionLogFile(platform, sessionID),
		sessionID: sessionID,
		platform:  platform,
	}
}

func (l *Logger) formatEntry(level, message string, fields ...interface{}) map[string]interface{} {
	entry := map[string]interface{}{
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"hook":      l.hookName,
		"level":     level,
		"message":   message,
	}

	if l.sessionID != "" {
		entry["session_id"] = l.sessionID
	}

	// Add additional fields
	for i := 0; i < len(fields)-1; i += 2 {
		if key, ok := fields[i].(string); ok {
			entry[key] = fields[i+1]
		}
	}

	return entry
}

func (l *Logger) write(entry map[string]interface{}) {
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}

	f, err := os.OpenFile(l.logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			fmt.Fprintf(os.Stderr, "logging: failed to close log file %s: %v\n", l.logFile, cerr)
		}
	}()

	if _, err := f.Write(data); err != nil {
		fmt.Fprintf(os.Stderr, "logging: failed to write log entry to %s: %v\n", l.logFile, err)
		return
	}
	if _, err := f.WriteString("\n"); err != nil {
		fmt.Fprintf(os.Stderr, "logging: failed to write newline to %s: %v\n", l.logFile, err)
		return
	}
}

// EndpointEvent writes an event whose action the runtime named for us, which is the normal case
// on this path: a hook or plugin fires for a specific lifecycle event and the action follows from
// which one. Callers that instead derive an action from an adjacent observation must use
// EndpointEventWithFidelity and say so.
func (l *Logger) EndpointEvent(action, category, severity, message string, fields map[string]interface{}) error {
	return l.EndpointEventWithFidelity(action, category, severity, message, asymptoteobserve.FidelityObserved, fields)
}

// EndpointEventWithFidelity is EndpointEvent with an explicit event.fidelity. It exists for the
// handful of hook paths that report an action the payload did not contain -- synthesizing an
// approval from a pre-tool notification, for instance -- so those events are distinguishable from
// the ones a runtime actually reported.
func (l *Logger) EndpointEventWithFidelity(action, category, severity, message, fidelity string, fields map[string]interface{}) error {
	if endpointEventsSuppressed {
		return nil
	}
	path := endpointLogPath()
	if path == "" {
		return nil
	}
	if severity == "" {
		severity = "info"
	}
	event := l.baseEndpointEvent(action, category, severity, message, fidelity)
	for key, value := range fields {
		if value == nil {
			continue
		}
		event[key] = value
	}
	normalizeEventModel(event)
	attachDerivedTrace(event)
	if err := writeEndpointJSON(path, event); err != nil {
		// A hook inside a sandbox that confines writes to the session workspace cannot reach the
		// runtime log (DeepSeek Harness first, #605; container and operator sandboxes since, #1284)
		// but can write the workspace. Staging the event there keeps capture live; a sweep, `beacon
		// endpoint spool drain`, or this session's next hook that can reach the log drains it. A
		// staged event is not reported as lost; the first one a session stages prints one note
		// saying where it waits (stageInWorkspaceSpool). When staging fails too -- read-only mode,
		// no workspace, unsafe ids, a full spool, a Beacon-only action -- the NOT-recorded
		// explanation below fires unchanged, which is the honest report for an event nothing
		// captured.
		if l.stageInWorkspaceSpool(event, path) {
			return nil
		}
		fmt.Fprintf(os.Stderr, "logging: failed to write endpoint event to %s: %v\n", path, err)
		reportEndpointWriteFailure(os.Stderr, l.platform, path, err)
		return err
	}
	l.drainOwnWorkspaceSpool(path, event)
	recordActiveSession(event)
	l.requestShareOnce(event)
	return nil
}

// ShareSettingsPath locates the user's share settings. It is never taken from the environment,
// which a repository can supply; tests replace it.
var ShareSettingsPath = shareauto.HookPath

// requestShareOnce writes one session.share_requested event for the event's session when the person
// turned auto-share on for its working directory (`beacon share auto enable`). It is the first event
// of the session the hooks see -- SessionStart where the runtime has one -- and never more than one
// per session, deduplicated under a lock in the active-session record.
//
// The event is only written to the local runtime log. Beacon Cloud forwarding ships it like any
// other line, and beacon.sh publishes the session when it receives it; without forwarding it is an
// inert record. Nothing here touches the network. It is event.fidelity=inferred because Beacon
// originated it: no runtime reported a share request.
func (l *Logger) requestShareOnce(event map[string]interface{}) {
	info, _ := event["event"].(map[string]interface{})
	if action, _ := info["action"].(string); action == asymptoteobserve.ActionShareRequested {
		return
	}
	session, _ := event["session"].(map[string]interface{})
	harness, _ := event["harness"].(map[string]interface{})
	if session == nil || harness == nil {
		return
	}
	id, _ := session["id"].(string)
	dir, _ := session["working_directory"].(string)
	name, _ := harness["name"].(string)
	if id == "" || dir == "" || name == "" {
		return
	}
	decision := shareauto.ForHook(ShareSettingsPath(), dir)
	if !decision.Enabled {
		return
	}
	recordPath := activesession.DefaultPath()
	markedAt := time.Now()
	first, err := activesession.MarkShareRequested(recordPath, name, id, markedAt)
	if err != nil || !first {
		return
	}
	fields := map[string]interface{}{
		"session": map[string]interface{}{"id": id, "working_directory": dir},
	}
	if visibility := decision.Visibility; visibility == "public" || visibility == "direct" || visibility == "private" {
		fields["share"] = map[string]interface{}{"visibility": visibility}
	}
	if repo, _ := event["repository"].(string); repo != "" {
		fields["repository"] = repo
	}
	if branch, _ := event["branch"].(string); branch != "" {
		fields["branch"] = branch
	}
	if err := l.EndpointEventWithFidelity(asymptoteobserve.ActionShareRequested, "session", "info", "Auto-share requested for this session", asymptoteobserve.FidelityInferred, fields); err != nil {
		// The request was not written (a read-only log, a sandbox): take the mark back so the next
		// hook of the session asks again, rather than leave the session marked and never requested.
		// Like the rest of this path it stays silent and never fails the hook.
		_ = activesession.UnmarkShareRequested(recordPath, name, id, markedAt)
	}
}

// recordActiveSession notes the event's session as the one its runtime is running in its working
// directory, for `beacon share --current`. It is a local file write and never fails the hook: the
// record is a convenience, the event above is what matters. Nothing here touches the network.
func recordActiveSession(event map[string]interface{}) {
	session, _ := event["session"].(map[string]interface{})
	harness, _ := event["harness"].(map[string]interface{})
	if session == nil || harness == nil {
		return
	}
	id, _ := session["id"].(string)
	dir, _ := session["working_directory"].(string)
	name, _ := harness["name"].(string)
	if id == "" || dir == "" || name == "" {
		return
	}
	// Silent on failure: stderr is shown to the person on some runtimes, and a missing record
	// only means `beacon share --current` falls back to the newest session in the directory.
	_ = activesession.Record(activesession.DefaultPath(), name, id, dir, time.Now())
}

// stageInWorkspaceSpool stages one event this hook could not write to the runtime log at logPath
// in its session's workspace spool (asymptoteobserve.WorkspaceSpoolDir), and reports whether it
// did.
//
// Every harness stages, because every harness's spool has a drain: the sweep, `beacon endpoint
// spool drain`, and the session's own next hook that reaches the log. It began as DeepSeek
// Harness's alone (#605) and became general once other sandboxes showed the same shape (#1284).
// The session id and harness name must be safe directory names on BOTH the writing and the
// draining side (ValidSpoolName is what they share), so a hook without a session id stages
// nothing. The workspace comes from the event's own session.working_directory -- the directory a
// sandbox confines writes to -- with the process cwd as the fallback for callers that did not
// carry the field.
//
// An action only Beacon writes (IsBeaconOnlyAction) is never staged: every drain refuses it, as it
// must refuse one a repository planted, so staging would only turn a write the caller can retry
// (requestShareOnce takes its mark back) into one silently lost.
//
// The spool is a localingest.Spool, the store a restricted-mode hook already falls back to, not the
// runtime log's writer: it is private to this account wherever the workspace lives, bounded, and
// waits at most its short lock timeout for a drain under way, so a hook never holds the agent up
// behind one.
func (l *Logger) stageInWorkspaceSpool(event map[string]interface{}, logPath string) bool {
	if l == nil || l.sessionID == "" {
		return false
	}
	if info, _ := event["event"].(map[string]interface{}); info != nil {
		if action, _ := info["action"].(string); asymptoteobserve.IsBeaconOnlyAction(action) {
			return false
		}
	}
	harness := asymptoteobserve.NormalizeHarnessName(l.platform)
	workspace := spoolWorkspace(event)
	dir, ok := asymptoteobserve.WorkspaceSpoolDir(workspace, harness, l.sessionID)
	// A workspace on the sandbox's own root filesystem (a cwd the sandbox never mounted) would
	// hold the spool only as long as the sandbox lives.
	if !ok || asymptoteobserve.OnSandboxRoot(dir) || !spoolPathIsReal(dir, true) {
		return false
	}
	line, err := encodeEndpointLine(event)
	if err != nil {
		return false
	}
	spool := localingest.Spool{Dir: dir}
	pending, _, err := spool.Pending()
	first := err != nil || pending == 0
	if dropped, err := spool.Append([][]byte{line}); err != nil || dropped > 0 {
		return false
	}
	ensureSpoolGitignore(filepath.Dir(filepath.Dir(dir)))
	// <ws>/.beacon/spool, two levels above <harness>/<session>.
	ensureSpoolGitExclude(filepath.Dir(filepath.Dir(dir)))
	if first {
		reportStagedSession(os.Stderr, harness, l.sessionID, logPath, dir, workspace)
	}
	return true
}

// reportStagedSession prints, once for each session's spool (when it starts holding events), where
// this session's events now wait and what delivers them. Not on every event: a hook runs as one
// process per event, and every note lands in the runtime's hook stderr stream. Nothing tells the
// hook whether a drain will find the session -- that needs Beacon to have recorded it outside the
// sandbox, or the workspace under workspace_spool_roots -- so the note names the drain the person
// can always run. That command names this session alone and the log this hook writes, shell-quoted
// to paste as it is: pasted, it vouches for one session, never for every spool a cloned repository
// planted beside it, and delivers to the log the hook meant, whatever mode the command runs in.
// DeepSeek Harness is the exception: `beacon endpoint dsh sync` finds its sessions through its own
// store, so its staging stays silent as it always was.
func reportStagedSession(w io.Writer, harness, sessionID, logPath, dir, workspace string) {
	if harness == dshHarnessName {
		return
	}
	fmt.Fprintf(w, "beacon-hooks: cannot write the Beacon runtime log %s here; this session's events are held in %s until Beacon drains them.\n", logPath, dir)
	fmt.Fprintf(w, "beacon-hooks: they are delivered by this session's next hook that reaches the log, or by running this outside the sandbox: beacon endpoint spool drain --log-path %s --workspace %s --harness %s --session %s\n",
		shellQuote(logPath), shellQuote(workspace), shellQuote(harness), shellQuote(sessionID))
}

// shellQuote quotes s for a POSIX shell, so a path with spaces, quotes or `$(...)` pastes as one
// literal argument.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// spoolWorkspace is the workspace an event's session runs in: its session.working_directory, or
// the process cwd when the caller did not carry one.
func spoolWorkspace(event map[string]interface{}) string {
	cwd := ""
	if session, ok := event["session"].(map[string]interface{}); ok {
		cwd, _ = session["working_directory"].(string)
	}
	if strings.TrimSpace(cwd) == "" {
		cwd, _ = os.Getwd()
	}
	return cwd
}

// spoolPathIsReal reports whether the components WorkspaceSpoolDir adds below the workspace --
// .beacon, spool, <harness> and <session> -- are real directories and the spool file, if present,
// a regular file. allowMissing lets components that do not exist yet pass, for a writer that
// creates them.
//
// The sandboxed agent can write the workspace and knows its own session id, so it can plant the
// session directory as a link to the account's own spool; the hook's drain, running outside the
// sandbox, would then remove the lines it refuses from that spool. Lstat checks the components
// themselves, from the session directory up, so a missing spool costs a drain one stat.
func spoolPathIsReal(dir string, allowMissing bool) bool {
	file := localingest.Spool{Dir: dir}.FilePath()
	for i := 0; i < 4; i, dir = i+1, filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		switch {
		case err == nil:
			if !info.IsDir() {
				return false
			}
		case allowMissing && os.IsNotExist(err):
		default:
			return false
		}
	}
	info, err := os.Lstat(file)
	return err != nil || info.Mode().IsRegular()
}

// ownSpoolDrainBatch bounds how many staged lines one hook delivers, so a long-sandboxed session's
// backlog never makes a single hook slow; what is left waits for a sweep or a later process.
const ownSpoolDrainBatch = 256

// drainedOwnSpools remembers the spool directories this process has already drained, so a hook
// that emits several events drains once rather than once per event.
var drainedOwnSpools sync.Map

// drainOwnWorkspaceSpool delivers, after this hook reached the runtime log at path, one batch of
// what earlier hooks of the same session staged when they could not -- a session that leaves its
// sandbox, or whose log comes back, catches up without waiting for a sweep.
//
// Only this hook's own session spool is drained: the hook knows its own session id is genuine, and
// cannot know that of any other directory under the workspace, which anything able to write the
// workspace (a cloned repository included) may have planted. Lines are untrusted all the same, so
// each passes AdmitStagedLine first; a refused one counts as processed, discarded rather than
// retried forever. A delivery error leaves it and the rest staged for the next drain.
//
// Everything here is best-effort and quiet: it never stages, never goes back through
// EndpointEventWithFidelity, never writes stderr and never fails the hook. It drains only through
// real directories (spoolPathIsReal), and Spool.Drain skips without waiting when another drainer
// holds the spool. Each admitted line is decoded and written again through encodeEndpointLine, as
// every line this hook writes is -- redacted, size-capped and stamped -- never appended byte for
// byte.
func (l *Logger) drainOwnWorkspaceSpool(path string, event map[string]interface{}) {
	if l == nil || l.sessionID == "" {
		return
	}
	harness := asymptoteobserve.NormalizeHarnessName(l.platform)
	dir, ok := asymptoteobserve.WorkspaceSpoolDir(spoolWorkspace(event), harness, l.sessionID)
	if !ok {
		return
	}
	if _, done := drainedOwnSpools.LoadOrStore(dir, true); done || !spoolPathIsReal(dir, false) {
		return
	}
	_, _ = localingest.Spool{Dir: dir}.Drain(ownSpoolDrainBatch, func(lines [][]byte) (int, error) {
		for i, line := range lines {
			if !asymptoteobserve.AdmitStagedLine(line, harness, l.sessionID) {
				continue
			}
			var staged map[string]interface{}
			if err := json.Unmarshal(line, &staged); err != nil {
				continue
			}
			encoded, err := encodeEndpointLine(staged)
			if err != nil {
				continue
			}
			if err := deliverEndpointLine(path, encoded); err != nil {
				return i, err
			}
		}
		return len(lines), nil
	})
}

// reportedWriteFailures remembers which runtime log paths this process has already explained a
// write failure for, so the explanation is printed once per path rather than once per event. One
// hook invocation can emit several events -- a post-tool hook records the tool result, the file
// edit and the command in turn -- and a runtime that shows hook stderr to the person at the
// keyboard would otherwise repeat the same paragraph for each.
var reportedWriteFailures sync.Map

// reportEndpointWriteFailure explains, on stderr, that an endpoint event was not recorded and what
// to do about it.
//
// The one-line error above it names the path and the errno, which is enough for someone reading a
// log and not enough for someone watching a hook fail: "read-only file system" from inside an
// agent's sandbox reads like a broken disk, not like a runtime that confines its hooks.
//
// The exit status is deliberately not changed. Every hook path exits 0 (see the policy seam and
// the per-runtime deny notes in cmd/policy.go), and on the Claude-shaped hook contract DeepSeek
// Harness and several others reproduce, exit 2 is a *block*: it would refuse the tool call, erase
// the prompt, or -- on Stop -- keep the agent running, on every event, for as long as the log stays
// unwritable. A telemetry write that failed must never become a control decision, so the failure is
// made loud here, on the stream a runtime shows or logs for a hook, and detectable from outside by
// `beacon endpoint doctor`, which is where an operator can act on it.
func reportEndpointWriteFailure(w io.Writer, platform, path string, err error) {
	if w == nil || err == nil {
		return
	}
	if _, already := reportedWriteFailures.LoadOrStore(path, true); already {
		return
	}
	fmt.Fprintf(w, "beacon-hooks: this event was NOT recorded: cannot write the Beacon runtime log %s (%v).\n", path, err)
	if asymptoteobserve.NormalizeHarnessName(platform) == dshHarnessName {
		// DeepSeek Harness runs hook commands sandboxed to the session's working directory in
		// every sandbox mode except danger-full-access, and its sandbox policy has no writable-path
		// allow-list, so a log under the user's home is unreachable from a hook by design (#605).
		fmt.Fprintln(w, "beacon-hooks: DeepSeek Harness runs hooks inside the session sandbox, which can write only the session workspace unless the sandbox mode is danger-full-access; if this session is sandboxed, that is the cause and live hook capture is off for it.")
		fmt.Fprintln(w, "beacon-hooks: run `beacon endpoint dsh sync` outside the harness to backfill this session from DeepSeek's own session store, and `beacon endpoint doctor` to check hook capture.")
		return
	}
	fmt.Fprintln(w, "beacon-hooks: check that this user can write that path (the hook may be running in a sandbox that confines writes), then run `beacon endpoint doctor`.")
}

// dshHarnessName is DeepSeek Harness's canonical harness.name.
const dshHarnessName = "deepseek_harness"

// normalizeEventModel canonicalizes event["model"] in place, the way the harness name is
// canonicalized in baseEndpointEvent and for the same reason: every token report groups by this
// field, so one model reported under two spellings is two rows in the BY MODEL rollup.
//
// It runs after the caller's fields are merged rather than inside each mapper because every hook
// platform sets fields["model"] from its own payload -- Cline reads modelId, OpenCode reads a
// nested model object, the shared path reads "model" -- and normalizing in one place is what
// keeps a new mapper from quietly reintroducing the split.
//
// The provider prefix that normalizing removes is not dropped: it is recorded as
// gen_ai.provider.name when the runtime did not already report one, so "anthropic/claude-..."
// loses nothing by being canonicalized. An existing provider always wins, since the runtime
// naming its own provider is a better source than a prefix parsed out of a model string.
func normalizeEventModel(event map[string]interface{}) {
	raw, ok := event["model"].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return
	}
	name, provider := asymptoteobserve.SplitModelProvider(raw)
	event["model"] = name
	if provider == "" {
		return
	}
	genAI, _ := event["gen_ai"].(map[string]interface{})
	if genAI == nil {
		genAI = map[string]interface{}{}
	}
	providerBlock, _ := genAI["provider"].(map[string]interface{})
	if providerBlock == nil {
		providerBlock = map[string]interface{}{}
	}
	if existing, _ := providerBlock["name"].(string); strings.TrimSpace(existing) != "" {
		return
	}
	providerBlock["name"] = provider
	genAI["provider"] = providerBlock
	event["gen_ai"] = genAI
}

func (l *Logger) baseEndpointEvent(action, category, severity, message, fidelity string) map[string]interface{} {
	hostname, _ := os.Hostname()
	userName := os.Getenv("USER")
	user := map[string]interface{}{
		"name": userName,
	}
	if uid := firstEnv("BEACON_CLOUD_USER_ID_HASH", "BEACON_CLOUD_USER_ID"); uid != "" {
		user["uid"] = uid
	} else if current, err := osuser.Current(); err == nil {
		user["uid"] = current.Uid
		if userName == "" {
			user["name"] = current.Username
		}
	}
	// Both blocks are assembled before the event literal so the provenance keys can be omitted
	// rather than written empty. This path builds raw maps instead of the shared Event struct, so
	// it does not get `omitempty` for free, and an empty "collection_method" on every event would
	// be both noise in the log and a value a consumer could mistake for a real one.
	eventBlock := map[string]interface{}{
		"kind":     "agent_runtime",
		"action":   action,
		"category": category,
	}
	if fidelity != "" {
		eventBlock["fidelity"] = fidelity
	}
	harnessBlock := map[string]interface{}{
		// Normalized rather than written raw. The OTLP path already reports the canonical name
		// for the same session, so emitting the raw --platform value here recorded one session
		// under two names -- splitting it for any query or SIEM detection that groups by
		// harness.name. The flag itself is unchanged, so existing installs are fixed without
		// having their settings.json rewritten.
		"name": asymptoteobserve.NormalizeHarnessName(l.platform),
	}
	// Derived from --platform rather than from the normalized name above, because the flag is what
	// records which integration shape is installed. The two differ for VS Code, whose hook and
	// OTLP telemetry share a harness name.
	if method := asymptoteobserve.CollectionMethodForPlatform(l.platform); method != "" {
		harnessBlock["collection_method"] = method
	}
	event := map[string]interface{}{
		"timestamp": asymptoteobserve.FormatTimestamp(time.Now()),
		// Emission order within this hook invocation. One invocation can emit several
		// events (a post-tool hook records the tool result, the file edit and the command
		// in turn), and on a coarse clock those can land on the same nanosecond -- the
		// counter is what still separates them. It restarts at 1 in every hook process,
		// which is why it is a tiebreaker under the timestamp rather than an ordering key
		// of its own; see asymptoteobserve.Event.Sequence.
		"sequence":       endpointSequence.Next(),
		"vendor":         "beacon",
		"product":        "endpoint-agent",
		"schema_version": "1.0",
		"event":          eventBlock,
		"severity":       severity,
		"endpoint": map[string]interface{}{
			"hostname": hostname,
			"os":       runtime.GOOS,
		},
		"user":    user,
		"harness": harnessBlock,
		"message": asymptoteobserve.CleanString(message, asymptoteobserve.DefaultStringLimit, true),
	}
	if origin := firstEnv("BEACON_ORIGIN"); origin != "" {
		event["origin"] = origin
	}
	if run := cloudRunFields(); len(run) > 0 {
		event["run"] = run
	}
	if l.sessionID != "" {
		event["session"] = map[string]interface{}{"id": l.sessionID}
	}
	return event
}

func attachDerivedTrace(event map[string]interface{}) {
	if event == nil {
		return
	}
	session, _ := event["session"].(map[string]interface{})
	sessionID, _ := session["id"].(string)
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	trace, _ := event["trace"].(map[string]interface{})
	if trace == nil {
		trace = map[string]interface{}{}
	}
	if id, _ := trace["id"].(string); strings.TrimSpace(id) == "" {
		harness, _ := event["harness"].(map[string]interface{})
		harnessName, _ := harness["name"].(string)
		if traceID := asymptoteobserve.DerivedTraceIDForSession(harnessName, sessionID); traceID != "" {
			trace["id"] = traceID
		}
	}
	if spanID, _ := trace["span_id"].(string); strings.TrimSpace(spanID) == "" {
		if callID := endpointToolCallID(event); callID != "" {
			trace["span_id"] = callID
		}
	}
	if len(trace) > 0 {
		event["trace"] = trace
	}
}

func endpointToolCallID(event map[string]interface{}) string {
	genAI, _ := event["gen_ai"].(map[string]interface{})
	tool, _ := genAI["tool"].(map[string]interface{})
	call, _ := tool["call"].(map[string]interface{})
	id, _ := call["id"].(string)
	return strings.TrimSpace(id)
}

func cloudRunFields() map[string]interface{} {
	run := map[string]interface{}{}
	if provider := firstEnv("BEACON_RUN_PROVIDER"); provider != "" {
		run["provider"] = provider
	}
	if runID := firstEnv("BEACON_RUN_ID", "CLAUDE_CODE_REMOTE_SESSION_ID"); runID != "" {
		run["run_id"] = runID
	}
	if attempt := firstEnv("BEACON_RUN_ATTEMPT"); attempt != "" {
		run["run_attempt"] = attempt
	}
	if workflow := firstEnv("BEACON_RUN_WORKFLOW"); workflow != "" {
		run["workflow"] = workflow
	}
	if job := firstEnv("BEACON_RUN_JOB"); job != "" {
		run["job"] = job
	}
	if eventName := firstEnv("BEACON_RUN_EVENT_NAME"); eventName != "" {
		run["event_name"] = eventName
	}
	if commit := firstEnv("BEACON_RUN_COMMIT"); commit != "" {
		run["commit"] = commit
	}
	if repository := firstEnv("BEACON_RUN_REPOSITORY"); repository != "" {
		run["repository"] = repository
	}
	if branch := firstEnv("BEACON_RUN_BRANCH"); branch != "" {
		run["branch"] = branch
	}
	if pr := firstEnv("BEACON_RUN_PR"); pr != "" {
		run["pr"] = pr
	}
	if prNumber := firstEnv("BEACON_RUN_PR_NUMBER"); prNumber != "" {
		run["pr_number"] = prNumber
	}
	if actor := firstEnv("BEACON_RUN_ACTOR"); actor != "" {
		run["actor"] = actor
	}
	if ephemeral := firstEnv("BEACON_RUN_EPHEMERAL"); ephemeral != "" {
		if value, err := strconv.ParseBool(ephemeral); err == nil {
			run["ephemeral"] = value
		}
	}
	return run
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

// writeEndpointJSON appends one endpoint event to the runtime log. A variable so a test can make one
// write fail.
var writeEndpointJSON = writeEndpointJSONFile

func writeEndpointJSONFile(path string, event map[string]interface{}) error {
	_, dirMode, _ := asymptoteobserve.RuntimeLogModes(path)
	if asymptoteobserve.RuntimeLogDirCreatable(path) {
		if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
			return err
		}
	}
	data, err := encodeEndpointLine(event)
	if err != nil {
		return err
	}
	return deliverEndpointLine(path, data)
}

// encodeEndpointLine is the line an endpoint event is written as: sanitized, then fitted to the
// event size limit and stamped with its event.id the way every writer does (FitEventLine).
func encodeEndpointLine(event map[string]interface{}) ([]byte, error) {
	data, err := json.Marshal(sanitizeEndpointMap(event))
	if err != nil {
		return nil, err
	}
	var stamp func(id string) ([]byte, error)
	if info, ok := event["event"].(map[string]interface{}); ok {
		if id, _ := info["id"].(string); strings.TrimSpace(id) == "" {
			stamp = func(id string) ([]byte, error) {
				info["id"] = id
				return json.Marshal(sanitizeEndpointMap(event))
			}
		}
	}
	return asymptoteobserve.FitEventLine(data, asymptoteobserve.DefaultMaxEventBytes, stamp)
}

// EndpointIngest delivers to the system log through the collector when the endpoint is in the
// restricted log access mode. Tests point it at a socket and spool of their own; nothing else
// changes it.
var EndpointIngest = defaultEndpointIngest()

// defaultEndpointIngest is the real socket and the account's spool, except in a test binary, where
// a test writing a system log as an ordinary account must never reach the machine's collector or
// leave lines in the developer's own spool.
func defaultEndpointIngest() localingest.Deliverer {
	if !testing.Testing() {
		return localingest.Deliverer{}
	}
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("beacon-hooks-test-%d", os.Getpid()))
	socket := filepath.Join(dir, "no-collector.sock")
	if runtime.GOOS == "windows" {
		socket = fmt.Sprintf(`\\.\pipe\beacon-hooks-test-%d-none`, os.Getpid())
	}
	return localingest.Deliverer{
		Client: localingest.Client{SocketPath: socket},
		Spool:  localingest.Spool{Dir: filepath.Join(dir, "spool")},
	}
}

// deliverEndpointLine writes one finished line to the runtime log at path.
//
// A hook writing the system log as an ordinary account hands the line to the collector over the
// local ingest socket when the endpoint has one (the restricted log access mode, in which the log
// is not writable by this account at all), and otherwise appends directly, as hooks always have.
// Only a direct append the log refuses -- a restricted endpoint whose collector is restarting --
// puts the line in this account's private spool, and it reaches the log with a later event
// instead of being lost. Root and per-user logs are always written directly. See
// localingest.Deliverer.Write.
func deliverEndpointLine(path string, data []byte) error {
	return EndpointIngest.Write(path, data, func(line []byte) error {
		return appendEndpointJSONL(path, append(line[:len(line):len(line)], '\n'), defaultEndpointRotateBytes, defaultEndpointRotateArchives)
	})
}

// EndpointLogConfigured reports whether this process has a runtime log to write to. Without one,
// EndpointEvent records nothing and returns nil, so a caller reporting what it recorded checks this
// rather than reading a nil error as a write.
func EndpointLogConfigured() bool {
	return endpointLogPath() != ""
}

func endpointLogPath() string {
	if path := firstEnv("BEACON_ENDPOINT_LOG", "BEACON_CLOUD_LOG_PATH", "BEACON_LOG_PATH", "BEACON_RUNTIME_LOG"); path != "" {
		return path
	}
	if os.Getenv("BEACON_ENDPOINT_MODE") == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")
}

func sanitizeEndpointMap(input map[string]interface{}) map[string]interface{} {
	return asymptoteobserve.SanitizeMap(input, asymptoteobserve.PrivacyOptions{
		RedactSecrets: true,
		StringLimit:   asymptoteobserve.DefaultStringLimit,
	})
}

func sanitizeEndpointSlice(input []interface{}) []interface{} {
	return asymptoteobserve.SanitizeSlice(input, asymptoteobserve.PrivacyOptions{
		RedactSecrets: true,
		StringLimit:   asymptoteobserve.DefaultStringLimit,
	})
}

// Keep this file-mode contract mirrored with the endpoint CLI and beaconjson exporter; the rotation
// itself is asymptoteobserve.RotateRuntimeLog for all three.
func appendEndpointJSONL(path string, line []byte, rotateBytes int64, rotateArchives int) error {
	_, dirMode, _ := asymptoteobserve.RuntimeLogModes(path)
	if asymptoteobserve.RuntimeLogDirCreatable(path) {
		if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
			return err
		}
	}
	lock, err := openEndpointRuntimeFileAs(path+".lock", path, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return err
	}
	held, err := filelock.Exclusive(lock)
	if err != nil {
		_ = lock.Close()
		return err
	}
	// Unlock before closing: LIFO defer ordering means the close listed first runs last.
	defer lock.Close()
	defer held.Release()
	if asymptoteobserve.IsDuplicateEndpointEvent(path, line, asymptoteobserve.EndpointDuplicateWindow) {
		return nil
	}
	// A rotation that cannot happen now is deferred, never fatal, and the event is appended past the
	// size limit for a later write to rotate. A hook usually runs as the person at the keyboard, and
	// on a system-mode log it may not rename or delete anything in the directory: the directory is
	// root-owned 0755 on POSIX, and on Windows its ACL gives ordinary users no right to add, rename or
	// delete entries -- the rights that would let one redirect a SYSTEM write. Rotation is the
	// privileged writers' job there. And on Windows a reader holding the log open without delete
	// sharing blocks the rename for every writer until it lets go. asymptoteobserve.RotateRuntimeLog
	// moves nothing in either case.
	_ = asymptoteobserve.RotateRuntimeLog(path, rotateBytes, rotateArchives, int64(len(line)))
	f, err := openEndpointRuntimeFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(line)
	return err
}

func openEndpointRuntimeFile(path string, flag int) (*os.File, error) {
	return openEndpointRuntimeFileAs(path, path, flag)
}

// openEndpointRuntimeFileAs opens path (the log or its lock) with the mode logPath calls for, and
// settles its mode through asymptoteobserve.OpenRuntimeLogFile: a new file gets that mode
// whatever the umask, an existing one is only ever narrowed toward it, by its owner, and root never
// follows a symlink or opens anything but a regular file there.
func openEndpointRuntimeFileAs(path, logPath string, flag int) (*os.File, error) {
	return asymptoteobserve.OpenRuntimeLogFile(path, logPath, flag)
}

// Debug logs a debug message
func (l *Logger) Debug(message string, fields ...interface{}) {
	entry := l.formatEntry("debug", message, fields...)
	l.write(entry)
}

// Info logs an info message
func (l *Logger) Info(message string, fields ...interface{}) {
	entry := l.formatEntry("info", message, fields...)
	l.write(entry)
}

// Warn logs a warning message
func (l *Logger) Warn(message string, fields ...interface{}) {
	entry := l.formatEntry("warn", message, fields...)
	l.write(entry)
}

// Error logs an error message and also writes to stderr
func (l *Logger) Error(message string, fields ...interface{}) {
	entry := l.formatEntry("error", message, fields...)
	l.write(entry)
	fmt.Fprintf(os.Stderr, "[%s] ERROR: %s\n", l.hookName, message)
}
