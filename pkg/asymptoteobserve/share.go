package asymptoteobserve

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// This file is the CLI half of the shared-trace wire contract: the request and response bodies
// `beacon share` exchanges with beacon.sh, and BuildShareBundle, which turns one session's endpoint
// events into the trace that is published.
//
// A shared trace IS a beacon.trace.v1 trace (trace.go), projected by the same code the local
// dashboard uses (tracebuild.go), so a shared trace and the local one read the same and every
// lens runs on both. The chat view beacon.sh shows is derived from the bundle with TraceMessages;
// it is never uploaded.
//
// A shared trace is a snapshot a person chose to publish, so the build is conservative about what
// leaves the machine: every event goes through SanitizeEvent and then has local paths rewritten
// relative to the session's directory or the home directory before it is projected, and Beacon's
// own bookkeeping and status telemetry are left out (ShareProjects).

// ShareInfo is the sharing request an event carries. Today only session.share_requested sets it:
// the hook adapter writes that event once per session when auto-share is on, and Beacon Cloud,
// receiving it through forwarding, publishes the session as a shared trace with this visibility
// ("" means the server default, direct).
type ShareInfo struct {
	Visibility string `json:"visibility,omitempty"`
}

// ShareRefreshCommand is the command that refreshes harness's shared session: by external id when
// that names the session plainly, and otherwise by session id and runtime.
func ShareRefreshCommand(harness, sessionID string) string {
	harness, sessionID = strings.TrimSpace(harness), strings.TrimSpace(sessionID)
	if harness == "" || sessionID == "" {
		return ""
	}
	if id := ShareExternalID(harness, sessionID); !strings.Contains(id, ":sha256-") {
		return "beacon share refresh " + id
	}
	if strings.ContainsAny(sessionID, " \t\n'\"$`\\;&|<>()") {
		return ""
	}
	return "beacon share refresh --harness " + NormalizeHarnessName(harness) + " " + sessionID
}

// Visibility levels of a shared trace. Direct is the default: anyone with the link.
const (
	ShareVisibilityPublic  = "public"
	ShareVisibilityDirect  = "direct"
	ShareVisibilityPrivate = "private"
)

// ValidShareVisibility reports whether value is one of the three visibility levels.
func ValidShareVisibility(value string) bool {
	switch value {
	case ShareVisibilityPublic, ShareVisibilityDirect, ShareVisibilityPrivate:
		return true
	}
	return false
}

// ShareSourceCLI is the source the CLI reports for traces it uploads, and the provenance source of
// the bundles it builds.
const ShareSourceCLI = "cli"

// ShareMaxEventsPerBatch is the most events one batch request may carry.
const ShareMaxEventsPerBatch = 1000

// ShareTitleMaxRunes bounds a title derived from the first prompt.
const ShareTitleMaxRunes = 80

// ShareTraceV1 is the body of PUT /v1/traces/{external_id}: the trace's summary and how it is
// shared. SchemaVersion is TraceSchemaVersion. The server owns the summary's sharing block and
// ignores it on input.
type ShareTraceV1 struct {
	SchemaVersion string            `json:"schema_version"`
	Summary       TraceSummaryV1    `json:"summary"`
	Visibility    string            `json:"visibility,omitempty"`
	Title         string            `json:"title,omitempty"`
	Source        string            `json:"source,omitempty"`
	Provenance    TraceProvenanceV1 `json:"provenance"`
	// RelatedTo is the external id of the trace this one continues, and RelationshipType how
	// (ShareRelationshipHandoff). Both are empty for a trace with no known origin.
	RelatedTo        string `json:"related_to,omitempty"`
	RelationshipType string `json:"relationship_type,omitempty"`
}

// Relationship types of related_to. Handoff relates a trace to the session it was handed off from
// (the session's first prompt carried the handoff marker, session.handoff); subagent relates a
// subagent's session to its parent's.
const (
	ShareRelationshipHandoff  = "handoff"
	ShareRelationshipSubagent = "subagent"
)

// ShareRelation is the trace a session continues, from its first session.handoff event: the source
// session's ShareExternalID and ShareRelationshipHandoff, or two empty strings. The handoff is the
// claim the marker made, not a verified identity, which is all related_to asserts.
func ShareRelation(events []Event) (relatedTo, relationshipType string) {
	for _, event := range events {
		if event.Handoff == nil {
			continue
		}
		harness := strings.TrimSpace(event.Handoff.SourceHarness)
		session := strings.TrimSpace(event.Handoff.SourceSessionID)
		if harness != "" && session != "" {
			return ShareExternalID(harness, session), ShareRelationshipHandoff
		}
	}
	return "", ""
}

// ShareTraceResultV1 is the response of PUT /v1/traces/{external_id}.
type ShareTraceResultV1 struct {
	ID         string `json:"id"`
	ExternalID string `json:"external_id"`
	URL        string `json:"url"`
	Visibility string `json:"visibility"`
	EventCount int    `json:"event_count"`
	Created    bool   `json:"created"`
}

// ShareTraceStateV1 is the owner's view of a trace from GET /v1/traces/{external_id}: enough to
// report on, and the event watermark a refresh continues from.
type ShareTraceStateV1 struct {
	ID          string `json:"id"`
	ExternalID  string `json:"external_id,omitempty"`
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Visibility  string `json:"visibility"`
	EventCount  int    `json:"event_count"`
	LastEventID string `json:"last_event_id,omitempty"`
	MaxNumber   int    `json:"max_number"`
	CreatedAt   string `json:"created_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// ShareEventsBatchV1 is the body of POST /v1/traces/{external_id}/events/batch. Events are upserted
// by ID, which is stable across uploads, so re-sending a batch is idempotent.
type ShareEventsBatchV1 struct {
	Events []TraceEventV1 `json:"events"`
}

// ShareBatchResultV1 is the response of an event batch.
type ShareBatchResultV1 struct {
	Upserted   int `json:"upserted"`
	EventCount int `json:"event_count"`
}

var shareExternalIDPattern = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,199}$`) })

// ValidShareExternalID reports whether id is a well-formed external id.
func ValidShareExternalID(id string) bool {
	return shareExternalIDPattern().MatchString(id)
}

// ShareExternalID is the client-chosen id of a session's shared trace: `<harness>:<session-id>`
// when that is a valid external id, otherwise `<harness>:sha256-<first 32 hex of sha256(session-id)>`.
// The harness is normalized, so every capture path of one session names the same trace.
func ShareExternalID(harness, sessionID string) string {
	name := NormalizeHarnessName(harness)
	if name == "" {
		name = strings.ToLower(strings.TrimSpace(harness))
	}
	if !ValidShareExternalID(name) || strings.Contains(name, ":") {
		name = "unknown"
	}
	if id := name + ":" + sessionID; sessionID != "" && ValidShareExternalID(id) {
		return id
	}
	sum := sha256.Sum256([]byte(sessionID))
	return name + ":sha256-" + hex.EncodeToString(sum[:])[:32]
}

// ShareOptions tunes BuildShareBundle. The zero value takes the working directory from the events,
// the home directory from the environment, and stamps the bundle with the current time.
type ShareOptions struct {
	// WorkingDirectory is the session's directory. Paths under it are written relative to it.
	WorkingDirectory string
	// HomeDirectory is replaced by "~" in paths outside the working directory.
	HomeDirectory string
	// NoHomeLookup leaves HomeDirectory empty rather than reading it from the environment.
	NoHomeLookup bool
	// BeaconVersion is recorded in the bundle's provenance.
	BeaconVersion string
	// GeneratedAt is the provenance timestamp; zero means now.
	GeneratedAt time.Time
	// MetadataOnly removes from every event what Metadata-only forwarding removes (StripToMetadata):
	// prompts, answers, reasoning, tool arguments and results, command output, diffs, raw payloads
	// and reasons. Event ids are derived before, so a trace shared in either mode names its events
	// the same way.
	MetadataOnly bool
}

// ShareEventsAfter is what a re-share or refresh uploads of a bundle's events, given the last event
// beacon.sh holds (ShareTraceStateV1.LastEventID, or with no id its MaxNumber): the events after it,
// re-sending that last event and every event from the earliest tool call it holds without a
// completion, since a call still open on the server is the part of a trace most likely to have
// been rewritten (a call recorded before its result arrives). Events are upserted by id, so a
// re-send is harmless. When lastID is not among events, or neither watermark applies, every event is
// returned.
func ShareEventsAfter(events []TraceEventV1, lastID string, maxNumber int) []TraceEventV1 {
	end := -1
	switch {
	case lastID != "":
		for i, event := range events {
			if event.ID == lastID {
				end = i
				break
			}
		}
	case maxNumber > 0 && maxNumber <= len(events):
		end = maxNumber - 1
	}
	if end < 0 {
		return events
	}
	uploaded := events[:end+1]
	completed := map[string]bool{}
	for _, event := range uploaded {
		if event.ToolCallID != "" && !traceToolInvocation(event) && traceToolCompletion(event) {
			completed[event.ToolCallID] = true
		}
	}
	start := end
	for i, event := range uploaded {
		if event.ToolCallID != "" && traceToolInvocation(event) && !completed[event.ToolCallID] {
			start = i
			break
		}
	}
	return events[start:]
}

// ShareProjects reports whether a shared trace carries event: false for Beacon's bookkeeping about
// the session (session.shared, session.share_requested, session.commit_linked), for Beacon's own
// telemetry and runtime status pings (heartbeats, inventory, destination health, idle/activity
// status, MCP tool listings, runtime detection), for metric datapoints that carry no token usage,
// and for an event with no action. It is the one definition of what a share publishes, so a local
// view can tell when a shared trace is behind the session: an event ShareProjects accepts that is
// newer than the share.
func ShareProjects(event Event) bool {
	NormalizeTraceEvent(&event)
	action := strings.ToLower(strings.TrimSpace(event.Event.Action))
	category := strings.ToLower(strings.TrimSpace(event.Event.Category))
	switch {
	case action == "" || shareBookkeepingActions[action] || traceNoise(action, category):
		return false
	case category == "metric" || strings.HasPrefix(action, "token.") || strings.HasPrefix(action, "metric."):
		// Usage is the only part of a datapoint a trace has use for.
		return event.GenAI != nil && event.GenAI.Usage != nil
	}
	return true
}

// ShareEventID is the stable id of the trace event an endpoint event becomes: its event.id when it
// has one, otherwise the id the runtime log writer would derive for it (EventIDForLine over the
// event as the writer stamps it), so a session read from its runtime's store names its events the
// same way on every upload.
func ShareEventID(event Event) string {
	if id := strings.TrimSpace(event.Event.ID); id != "" {
		return id
	}
	// Clone before deriving: AttachDerivedTrace writes through the event's trace pointer, which the
	// caller's event shares.
	event = cloneEvent(event)
	event.Event.ID = ""
	AttachDerivedTrace(&event)
	event = SanitizeEvent(event, 64*1024)
	data, err := json.Marshal(event)
	if err != nil {
		return eventUUID("share", event.Timestamp+"\x00"+event.Event.Action+"\x00"+event.Message)
	}
	return EventIDForLine(data)
}

// BuildShareBundle builds the trace a person publishes from one session's events: a
// beacon.trace.v1 bundle projected by TraceEventFromEvent and TraceBuilder, the code the local
// dashboard uses.
//
// Before projection every event is sanitized (SanitizeEvent) and has local paths rewritten -- the
// session's directory becomes ".", the home directory "~" -- in every field the projection reads:
// the message, prompt text, retained assistant output, command line and output, file path and diff,
// tool command, path, arguments and result, MCP fields, approval and policy reasons, error, the
// working directory and the repository. With MetadataOnly, every event is then cut down to what
// Metadata-only forwarding keeps (StripToMetadata). Events ShareProjects rejects are left out. The
// rest are ordered by timestamp, then with tool invocations ahead of everything else at one instant (a
// pre-tool and a post-tool hook can share a timestamp), then by ID; events that cannot be timed keep
// their input order after every timed one. An event that appears twice (the same ID) is carried
// once. Each event's ID is ShareEventID, so re-sharing upserts rather than duplicates.
//
// The summary is folded from the carried events, as the dashboard folds a trace, with these
// differences for publication: session.working_directory and repository.path are dropped,
// project.name is the project's directory name (never the home directory's name, which is the local
// account's), and repository.remote_url and ref come from the session's commit links and CI run,
// with credentials removed (ShareRemoteURL). Token usage is the plain sum of the carried events'
// usage, as in the local view.
func BuildShareBundle(events []Event, opts ShareOptions) TraceBundleV1 {
	if opts.WorkingDirectory == "" {
		for _, event := range events {
			if event.Session != nil && strings.TrimSpace(event.Session.WorkingDirectory) != "" {
				opts.WorkingDirectory = strings.TrimSpace(event.Session.WorkingDirectory)
				break
			}
		}
	}
	if opts.HomeDirectory == "" && !opts.NoHomeLookup {
		if home, err := os.UserHomeDir(); err == nil {
			opts.HomeDirectory = home
		}
	}
	scrub := newPathScrubber(opts.WorkingDirectory, opts.HomeDirectory)

	type item struct {
		event Event
		te    TraceEventV1
		ts    time.Time
		hasTS bool
		rank  int
	}
	items := make([]item, 0, len(events))
	for _, original := range events {
		if !ShareProjects(original) {
			continue
		}
		clone := cloneEvent(original)
		id := ShareEventID(clone)
		event := scrub.event(SanitizeEvent(clone, 1<<30))
		NormalizeTraceEvent(&event)
		if opts.MetadataOnly {
			// After the id and after normalizing, which names a metric datapoint from its message.
			StripToMetadata(&event)
		}
		te := TraceEventFromEvent(event, id, 0)
		if te.Tool != nil && te.Tool.Arguments == nil {
			// A hook keeps the tool's input in raw when the runtime gave it no gen_ai call.
			if args := rawToolArgs(event.Raw); args != nil {
				te.Tool.Arguments = args
			}
		}
		it := item{event: event, te: te, rank: 1}
		it.ts, it.hasTS = traceEventTime(te)
		if traceToolInvocation(te) {
			it.rank = 0
		}
		items = append(items, it)
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.hasTS != b.hasTS {
			return a.hasTS
		}
		if !a.hasTS {
			return false
		}
		if !a.ts.Equal(b.ts) {
			return a.ts.Before(b.ts)
		}
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		return a.te.ID < b.te.ID
	})

	provenance := TraceProvenanceV1{Source: ShareSourceCLI, BeaconVersion: opts.BeaconVersion}
	generated := opts.GeneratedAt
	if generated.IsZero() {
		generated = time.Now()
	}
	provenance.GeneratedAt = FormatTimestamp(generated)

	var builder *TraceBuilder
	seen := map[string]bool{}
	for _, it := range items {
		if seen[it.te.ID] {
			continue
		}
		seen[it.te.ID] = true
		if builder == nil {
			builder = NewTraceBuilder(shareTraceID(it.event), it.event)
		}
		builder.Add(it.event, it.te)
	}
	if builder == nil {
		builder = NewTraceBuilder(shareTraceID(Event{}), Event{})
	}
	bundle := builder.Bundle(provenance)
	bundle.Range = nil
	if bundle.Events == nil {
		bundle.Events = []TraceEventV1{}
	}
	shareSummaryForPublication(&bundle.Summary, events, opts)
	return bundle
}

// shareTraceID names the shared bundle the way the dashboard names a session's trace.
func shareTraceID(event Event) string {
	if event.Session != nil && strings.TrimSpace(event.Session.ID) != "" {
		harness := NormalizeHarnessName(event.Harness.Name)
		if harness == "" {
			harness = strings.TrimSpace(event.Harness.Name)
		}
		return "session:" + harness + ":" + strings.TrimSpace(event.Session.ID)
	}
	return TraceProjectionID(event)
}

// shareSummaryForPublication replaces the summary's local paths with what a reader elsewhere can
// use, from the session's original (unscrubbed) events.
func shareSummaryForPublication(summary *TraceSummaryV1, events []Event, opts ShareOptions) {
	if summary.Session != nil {
		summary.Session.WorkingDirectory = ""
	}
	home := opts.HomeDirectory
	var remote, branch, ref, project, workDir string
	for _, event := range events {
		if workDir == "" && event.Session != nil {
			workDir = strings.TrimSpace(event.Session.WorkingDirectory)
		}
		if event.VCS != nil {
			if event.VCS.Repository != nil && event.VCS.Repository.URL != nil && remote == "" {
				remote = ShareRemoteURL(event.VCS.Repository.URL.Full)
			}
			if event.VCS.Ref != nil && event.VCS.Ref.Head != nil {
				if branch == "" {
					branch = ShareBranch(event.VCS.Ref.Head.Name)
				}
				// The newest commit wins: a session that made several is described by the last.
				if event.VCS.Ref.Head.Revision != "" {
					ref = event.VCS.Ref.Head.Revision
				}
			}
		}
		if event.Run != nil {
			if ref == "" {
				ref = event.Run.Commit
			}
			if branch == "" {
				branch = ShareBranch(event.Run.Branch)
			}
			if project == "" {
				project = shareProjectName(event.Run.Repository, home)
			}
		}
		if project == "" {
			project = shareProjectName(event.Repository, home)
		}
	}
	if project == "" && remote != "" {
		project = strings.TrimSuffix(lastPathSegment(remote), ".git")
	}
	if project == "" {
		project = shareProjectName(workDir, home)
	}
	if project = traceProjectName(project); project != "" {
		summary.Project = &TraceProjectV1{Name: project}
	} else {
		summary.Project = nil
	}
	repo := summary.Repository
	if repo == nil {
		repo = &TraceRepositoryV1{}
	}
	// A local path, even rewritten, names nothing a reader elsewhere can open.
	repo.Path = ""
	repo.Branch = ShareBranch(repo.Branch)
	if repo.Branch == "" {
		repo.Branch = branch
	}
	if repo.RemoteURL == "" {
		repo.RemoteURL = remote
	}
	if repo.Ref == "" {
		repo.Ref = ref
	}
	if *repo == (TraceRepositoryV1{}) {
		summary.Repository = nil
	} else {
		summary.Repository = repo
	}
}

// shareProjectName is the last segment of a directory or repository name, unless it is the home
// directory, whose name is the local account's.
func shareProjectName(value, home string) string {
	value = strings.TrimSpace(value)
	if value == "" || samePath(value, home) {
		return ""
	}
	return lastPathSegment(value)
}

// NewShareTrace is the PUT body for a bundle built by BuildShareBundle: its summary, the requested
// visibility, a title from its first prompt (ShareTitle), and its provenance.
func NewShareTrace(bundle TraceBundleV1, visibility string) ShareTraceV1 {
	trace := ShareTraceV1{
		SchemaVersion: TraceSchemaVersion,
		Summary:       bundle.Summary,
		Visibility:    visibility,
		Source:        ShareSourceCLI,
		Provenance:    bundle.Provenance,
	}
	trace.Summary.Sharing = TraceSharingV1{State: "shared", Visibility: visibility}
	for _, event := range bundle.Events {
		if event.Type == "user_message" && event.Content != nil && strings.TrimSpace(event.Content.Text) != "" {
			trace.Title = ShareTitle(event.Content.Text)
			break
		}
	}
	return trace
}

// rawToolArgs is the tool input a hook kept in raw, when it is an object.
func rawToolArgs(raw map[string]interface{}) map[string]interface{} {
	for _, key := range []string{"tool_input", "toolInput", "arguments", "args", "input"} {
		if args := objectValue(raw[key]); args != nil {
			return args
		}
	}
	return nil
}

// cloneEvent deep-copies an event through its JSON form, so sanitizing and scrubbing never write
// into the caller's events.
func cloneEvent(event Event) Event {
	data, err := json.Marshal(event)
	if err != nil {
		return event
	}
	var out Event
	if err := json.Unmarshal(data, &out); err != nil {
		return event
	}
	return out
}

// pathScrubber rewrites local paths before they leave the machine: the session's directory
// becomes ".", the home directory "~". A shared trace has no use for the absolute layout of the
// sharer's disk, and the home directory names the local account.
type pathScrubber struct {
	work string
	home string
}

func newPathScrubber(work, home string) pathScrubber {
	clean := func(p string) string {
		p = strings.TrimRight(strings.TrimSpace(p), `/\`)
		// A root or a single short segment would rewrite unrelated text.
		if len(p) < 3 {
			return ""
		}
		return p
	}
	return pathScrubber{work: clean(work), home: clean(home)}
}

func (s pathScrubber) path(p string) string {
	if p == "" {
		return ""
	}
	if s.work != "" {
		if p == s.work {
			return "."
		}
		for _, sep := range []string{"/", `\`} {
			if strings.HasPrefix(p, s.work+sep) {
				return strings.TrimPrefix(p, s.work+sep)
			}
		}
	}
	return s.text(p)
}

func (s pathScrubber) text(value string) string {
	if value == "" {
		return ""
	}
	if s.work != "" {
		value = replacePathPrefix(value, s.work, ".")
	}
	if s.home != "" {
		value = replacePathPrefix(value, s.home, "~")
	}
	return value
}

// replacePathPrefix replaces prefix with repl only where prefix is a whole path: the character
// before it cannot continue a path, and the one after it ends the path or starts its next
// segment. A bare substring match would turn /home/alex into ~ex for a home of /home/al.
func replacePathPrefix(value, prefix, repl string) string {
	if prefix == "" || !strings.Contains(value, prefix) {
		return value
	}
	var b strings.Builder
	rest := value
	for {
		i := strings.Index(rest, prefix)
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		end := i + len(prefix)
		consumed := len(value) - len(rest)
		before := consumed + i
		// A URL's "://" is not a parent directory: file:///Users/al is the path /Users/al.
		if (before == 0 || !continuesPath(value[before-1], true) || strings.HasSuffix(value[:before], "://")) &&
			(end == len(rest) || !continuesPath(rest[end], false) || endsSentence(rest[end:])) {
			b.WriteString(rest[:i])
			b.WriteString(repl)
		} else {
			b.WriteString(rest[:end])
		}
		rest = rest[end:]
	}
}

// endsSentence reports whether tail is punctuation closing the sentence a path ends, such as
// the "." in "saved to /home/al.", rather than a longer name such as /home/al.bak.
func endsSentence(tail string) bool {
	if tail == "" || (tail[0] != '.' && tail[0] != '-' && tail[0] != '~') {
		return false
	}
	return len(tail) == 1 || !continuesPath(tail[1], false)
}

// continuesPath reports whether c would make a match part of a longer path. Before a match a
// separator also counts, because /srv/home/al is not /home/al; after it a separator starts the
// next segment of the same path.
func continuesPath(c byte, before bool) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '.', c == '_', c == '-', c == '~':
		return true
	case c == '/', c == '\\':
		return before
	}
	return false
}

func (s pathScrubber) args(args map[string]interface{}) map[string]interface{} {
	if args == nil {
		return nil
	}
	out := make(map[string]interface{}, len(args))
	for key, value := range args {
		out[key] = s.value(value)
	}
	return out
}

func (s pathScrubber) value(value interface{}) interface{} {
	switch typed := value.(type) {
	case string:
		return s.text(typed)
	case map[string]interface{}:
		return s.args(typed)
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i, item := range typed {
			out[i] = s.value(item)
		}
		return out
	}
	return value
}

// event rewrites local paths in every field of an event the trace projection reads, on copies, so
// the caller's event is unchanged. Paths (file, tool path, working directory, repository) are
// written relative to the working directory; free text (messages, prompts, command lines and
// output, diffs, reasons) has whole-path occurrences replaced; structured blocks (gen_ai with its
// retained messages and tool arguments and results, MCP, errors, VCS, CI run, system context, raw)
// have every string value treated as free text.
func (s pathScrubber) event(e Event) Event {
	e.Message = s.text(e.Message)
	e.Repository = s.path(e.Repository)
	if e.Session != nil {
		c := *e.Session
		c.WorkingDirectory = s.path(c.WorkingDirectory)
		e.Session = &c
	}
	if e.Prompt != nil {
		c := *e.Prompt
		c.Text = s.text(c.Text)
		e.Prompt = &c
	}
	if e.Tool != nil {
		c := *e.Tool
		c.Name = s.text(c.Name)
		c.Command = s.text(c.Command)
		c.Path = s.path(c.Path)
		e.Tool = &c
	}
	if e.Command != nil {
		c := *e.Command
		c.Command = s.text(c.Command)
		c.Output = s.text(c.Output)
		e.Command = &c
	}
	if e.File != nil {
		c := *e.File
		c.Path = s.path(c.Path)
		c.Diff = s.text(c.Diff)
		e.File = &c
	}
	if e.Approval != nil {
		c := *e.Approval
		c.Reason = s.text(c.Reason)
		e.Approval = &c
	}
	if e.Policy != nil {
		c := *e.Policy
		c.Name = s.text(c.Name)
		c.Reason = s.text(c.Reason)
		e.Policy = &c
	}
	e.Error = scrubTyped(s, e.Error)
	e.MCP = scrubTyped(s, e.MCP)
	e.GenAI = scrubTyped(s, e.GenAI)
	e.VCS = scrubTyped(s, e.VCS)
	e.Run = scrubTyped(s, e.Run)
	e.SystemContext = scrubTyped(s, e.SystemContext)
	e.Handoff = scrubTyped(s, e.Handoff)
	if e.Raw != nil {
		e.Raw = s.args(e.Raw)
	}
	return e
}

// scrubTyped rewrites every string in a structured block as free text, through its JSON form. A
// block that does not survive the round trip is dropped rather than published unscrubbed.
func scrubTyped[T any](s pathScrubber, input *T) *T {
	if input == nil {
		return nil
	}
	data, err := json.Marshal(input)
	if err != nil {
		return nil
	}
	var generic interface{}
	if err := json.Unmarshal(data, &generic); err != nil {
		return nil
	}
	data, err = json.Marshal(s.value(generic))
	if err != nil {
		return nil
	}
	out := new(T)
	if err := json.Unmarshal(data, out); err != nil {
		return nil
	}
	return out
}

// ShareTitle is a one-line title of at most ShareTitleMaxRunes runes from prompt text, without the
// handoff marker a resumed session's first prompt carries.
func ShareTitle(text string) string {
	if marker := handoffMarker(text); marker != "" {
		text = strings.ReplaceAll(text, marker, " ")
	}
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= ShareTitleMaxRunes {
		return text
	}
	return strings.TrimSpace(string(runes[:ShareTitleMaxRunes-1])) + "…"
}

// ShareRemoteURL returns a git remote worth publishing: a network remote with its credentials,
// query and fragment removed, and the login of an scp-style remote dropped, so a token embedded in
// an https remote never leaves the machine. A local remote (a path, or a file:// URL) is dropped
// whole: it names a directory on this disk, often under the home directory, and identifies no
// repository anyone else can open.
func ShareRemoteURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if scheme := strings.Index(raw, "://"); scheme >= 0 {
		if strings.EqualFold(raw[:scheme], "file") {
			return ""
		}
		rest := raw[scheme+3:]
		host, path := rest, ""
		if slash := strings.Index(rest, "/"); slash >= 0 {
			host, path = rest[:slash], rest[slash:]
		}
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		if host == "" {
			return ""
		}
		if cut := strings.IndexAny(path, "?#"); cut >= 0 {
			path = path[:cut]
		}
		return raw[:scheme+3] + host + path
	}
	// scp-style [user@]host:path. The colon must come before any slash, and a one-letter host is a
	// Windows drive (C:\repo), not a machine.
	colon := strings.Index(raw, ":")
	if colon <= 0 || strings.ContainsAny(raw[:colon], `/\`) {
		return ""
	}
	host := raw[:colon]
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	if len(host) < 2 {
		return ""
	}
	return host + raw[colon:]
}

// ShareBranch is a branch name worth reporting: "HEAD", which runtimes record for a detached
// checkout, names no branch.
func ShareBranch(name string) string {
	name = strings.TrimSpace(name)
	if name == "HEAD" {
		return ""
	}
	return name
}

func samePath(a, b string) bool {
	a, b = strings.TrimRight(strings.TrimSpace(a), `/\`), strings.TrimRight(strings.TrimSpace(b), `/\`)
	return a != "" && a == b
}

func lastPathSegment(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), `/\`)
	if i := strings.LastIndexAny(value, `/\:`); i >= 0 {
		value = value[i+1:]
	}
	return value
}
