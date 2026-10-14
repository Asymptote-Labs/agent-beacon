package asymptoteobserve

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// This file is the one projection of Beacon endpoint events into the beacon.trace.v1 model in
// trace.go: TraceEventFromEvent turns one event into a TraceEventV1, and TraceBuilder folds a
// trace's events, in order, into its TraceSummaryV1. The local dashboard and its history, lens
// data, and shared traces all project with this code, so a trace reads the same wherever it is
// shown.

// TraceProjectionID is the trace an event belongs to: its trace ID when it has one, otherwise its
// session (keyed by the normalized harness), otherwise the event alone.
func TraceProjectionID(event Event) string {
	if event.Trace != nil && strings.TrimSpace(event.Trace.ID) != "" {
		id := strings.TrimSpace(event.Trace.ID)
		if strings.HasPrefix(id, "session:") || strings.HasPrefix(id, "trace:") || strings.HasPrefix(id, "event:") {
			return id
		}
		return "trace:" + id
	}
	if event.Session != nil && strings.TrimSpace(event.Session.ID) != "" {
		harness := NormalizeHarnessName(event.Harness.Name)
		if harness == "" {
			harness = strings.TrimSpace(event.Harness.Name)
		}
		return "session:" + harness + ":" + strings.TrimSpace(event.Session.ID)
	}
	if event.Event.ID != "" {
		return "event:" + event.Event.ID
	}
	return "event:" + event.Timestamp + ":" + event.Event.Action + ":" + event.Message
}

// NormalizeTraceEvent fills the category an event left empty from its action, and names a metric
// datapoint by its metric, the way every trace reader expects to find them. Readers apply it to an
// event before projecting it.
func NormalizeTraceEvent(event *Event) {
	metricName := traceMetricName(event)
	if event.Event.Category == "" && metricName != "" {
		event.Event.Category = "metric"
	}
	if event.Event.Category == "" {
		event.Event.Category = InferEventCategory(event.Event.Action)
	}
	metricName = traceMetricName(event)
	if event.Event.Category == "metric" && (event.Event.Action == "" || event.Event.Action == "metric.observed") {
		event.Event.Action = metricName
		if event.Event.Action == "" {
			event.Event.Action = "metric.observed"
		}
	}
}

func traceMetricName(event *Event) string {
	if event == nil {
		return ""
	}
	if event.Raw != nil {
		if value, ok := event.Raw["metric_name"].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	if event.Event.Category == "metric" {
		return strings.TrimSpace(event.Message)
	}
	return ""
}

// InferEventCategory is the category an action's prefix implies, or "" when it implies none.
func InferEventCategory(action string) string {
	switch {
	case strings.HasPrefix(action, "prompt."):
		return "prompt"
	case strings.HasPrefix(action, "command."):
		return "command"
	case strings.HasPrefix(action, "file."):
		return "file"
	case strings.HasPrefix(action, "mcp."):
		return "mcp"
	case strings.HasPrefix(action, "approval.") || strings.HasPrefix(action, "policy."):
		return "approval"
	case strings.HasPrefix(action, "metric."):
		return "metric"
	case strings.HasPrefix(action, "tool."):
		return "tool"
	default:
		return ""
	}
}

// TraceEventFromEvent projects one endpoint event into a trace event numbered number. The event's
// ID is its event.id; fallbackID names it when the event carries none (the dashboard passes the
// log line's ID).
func TraceEventFromEvent(event Event, fallbackID string, number int) TraceEventV1 {
	id := event.Event.ID
	if id == "" {
		id = fallbackID
	}
	te := TraceEventV1{
		ID:            id,
		Number:        number,
		Timestamp:     event.Timestamp,
		Type:          TraceEventType(event),
		Action:        event.Event.Action,
		Category:      event.Event.Category,
		Fidelity:      event.Event.Fidelity,
		Actor:         traceActor(event),
		Title:         traceEventTitle(event),
		Summary:       event.Message,
		Model:         NormalizeModelName(event.Model),
		SourceEventID: id,
	}
	if event.Prompt != nil {
		te.PromptID = strings.TrimSpace(event.Prompt.ID)
	}
	if event.Trace != nil {
		te.Trace = &TraceEventTraceV1{ID: event.Trace.ID, SpanID: event.Trace.SpanID, ParentSpanID: event.Trace.ParentSpanID}
	}
	if event.GenAI != nil && event.GenAI.Tool != nil && event.GenAI.Tool.Call != nil {
		te.ToolCallID = event.GenAI.Tool.Call.ID
	}
	if content := traceEventContent(event); content != nil {
		te.Content = content
	}
	if event.Tool != nil || (event.GenAI != nil && event.GenAI.Tool != nil) {
		te.Tool = traceTool(event)
	}
	if event.Command != nil {
		te.Command = &TraceCommandV1{
			Command:    event.Command.Command,
			ExitCode:   event.Command.ExitCode,
			DurationMS: event.Command.DurationMS,
		}
		if event.Command.Output != "" {
			te.Command.Output = contentFromText(event.Command.Output, event.Content)
		}
	}
	if event.File != nil {
		te.File = &TraceFileV1{
			Path:      event.File.Path,
			Operation: event.File.Operation,
			Language:  event.File.Language,
			DiffHash:  event.File.DiffHash,
			DiffBytes: event.File.DiffBytes,
		}
		if event.File.Diff != "" {
			te.File.Diff = contentFromText(event.File.Diff, event.Content)
		}
	}
	if event.MCP != nil {
		te.MCP = &TraceMCPV1{Server: event.MCP.Server, Tool: event.MCP.Tool}
		if event.MCP.Method != nil {
			te.MCP.Method = event.MCP.Method.Name
		}
		if event.MCP.Resource != nil {
			te.MCP.ResourceURI = event.MCP.Resource.URI
		}
		if event.MCP.Session != nil {
			te.MCP.SessionID = event.MCP.Session.ID
		}
	}
	if event.Error != nil && event.Error.Type != "" {
		te.Error = &TraceErrorV1{Type: event.Error.Type}
	}
	if event.Approval != nil {
		te.Approval = &TraceApprovalV1{Required: event.Approval.Required, Decision: event.Approval.Decision, Reason: event.Approval.Reason}
	}
	if event.Policy != nil {
		te.Policy = &TracePolicyV1{
			ID:          event.Policy.ID,
			Name:        event.Policy.Name,
			Decision:    event.Policy.Decision,
			Enforcement: event.Policy.Enforcement,
			Reason:      event.Policy.Reason,
		}
	}
	if usage := traceUsage(event); usage != nil {
		te.Usage = usage
	}
	return te
}

// TraceEventType is the trace event type an endpoint event projects to.
func TraceEventType(event Event) string {
	action := strings.ToLower(event.Event.Action)
	category := strings.ToLower(event.Event.Category)
	switch {
	case category == "prompt" || strings.HasPrefix(action, "prompt."):
		return "user_message"
	case action == "agent.reasoning" || strings.Contains(action, "reasoning"):
		return "agent_reasoning"
	case action == "agent.message" || action == "assistant.message" || action == "agent.response":
		// OpenCode records the assistant's text as agent.response.
		return "agent_message"
	case strings.HasPrefix(action, "tool.invoked") || action == "tool.started":
		return "tool_call"
	case strings.HasPrefix(action, "tool.") || category == "tool":
		return "tool_result"
	case category == "command" || event.Command != nil:
		return "command"
	case category == "file" || event.File != nil:
		return "file"
	case category == "mcp" || event.MCP != nil:
		return "mcp"
	case category == "approval" || event.Approval != nil || event.Policy != nil:
		return "approval"
	case category == "metric" && event.GenAI != nil && event.GenAI.Usage != nil:
		return "token_usage"
	case category == "session":
		return "session"
	case strings.Contains(action, "error") || event.Error != nil:
		return "error"
	default:
		return "other"
	}
}

func traceActor(event Event) string {
	switch TraceEventType(event) {
	case "user_message":
		return "user"
	case "agent_message", "agent_reasoning":
		return "assistant"
	case "tool_call", "tool_result", "command", "file", "mcp":
		return "tool"
	default:
		if strings.HasPrefix(event.Event.Action, "endpoint.") || strings.HasPrefix(event.Event.Action, "telemetry.") {
			return "beacon"
		}
		return "runtime"
	}
}

func traceEventTitle(event Event) string {
	switch {
	case event.Command != nil && event.Command.Command != "":
		return event.Command.Command
	case event.File != nil && event.File.Path != "":
		return event.File.Path
	case event.MCP != nil && (event.MCP.Server != "" || event.MCP.Tool != ""):
		return strings.Trim(strings.Join([]string{event.MCP.Server, event.MCP.Tool}, " "), " ")
	case event.Tool != nil && event.Tool.Name != "":
		return event.Tool.Name
	default:
		return event.Event.Action
	}
}

func traceEventContent(event Event) *TraceContentV1 {
	if event.Prompt != nil && event.Prompt.Text != "" {
		return contentFromText(event.Prompt.Text, event.Content)
	}
	var partType string
	switch TraceEventType(event) {
	case "agent_message":
		partType = GenAIPartTypeText
	case "agent_reasoning":
		partType = GenAIPartTypeReasoning
	}
	if partType != "" {
		// The retained turn text lives in gen_ai.output.messages; event.Message on
		// these events is usually a fixed label ("Codex assistant message"). The
		// label stays the event summary, and is the content only when no text was
		// retained.
		if text := RetainedOutputText(event.GenAI, partType); text != "" {
			return retainedOutputContent(text, event.Content)
		}
		if event.Message != "" {
			return contentFromText(event.Message, event.Content)
		}
	}
	return contentFromContentInfo(event.Content)
}

// retainedOutputContent is contentFromText for text read back out of
// gen_ai.output.messages. The writer already redacted and bounded each stored
// part, but an event can carry several parts, so the joined text is held to the
// same limit prompt.text is stored at and redacted again; the marker records
// either change so a reader can still tell the copy was altered.
func retainedOutputContent(text string, info *ContentInfo) *TraceContentV1 {
	bounded := TruncateString(text, DefaultStringLimit)
	cleaned := RedactString(bounded)
	content := contentFromText(cleaned, info)
	if bounded != text {
		content.Truncated = true
	}
	if cleaned != bounded {
		content.Redacted = true
	}
	return content
}

// RetainedOutputText joins the assistant parts of partType in gen_ai.output.messages, in order, as
// GenAIText reads them. Parts of another type -- reasoning on a message event, tool calls -- are
// skipped, so an assistant message never shows its reasoning as what it said. Every reader (the
// trace projection, the handoff brief) uses it, so they agree on what the assistant said.
func RetainedOutputText(genAI *GenAIInfo, partType string) string {
	if genAI == nil || genAI.Output == nil {
		return ""
	}
	return strings.Join(GenAIText(genAI.Output.Messages, RoleAssistant, partType), "\n\n")
}

func contentFromText(text string, info *ContentInfo) *TraceContentV1 {
	content := contentFromContentInfo(info)
	if content == nil {
		content = &TraceContentV1{Retention: "full", Included: true}
	}
	content.Text = text
	if content.Retention == "" {
		content.Retention = "full"
	}
	content.Included = true
	return content
}

func contentFromContentInfo(info *ContentInfo) *TraceContentV1 {
	if info == nil {
		return nil
	}
	return &TraceContentV1{
		Retention: info.Retention,
		Included:  info.Included,
		Redacted:  info.Redacted,
		Truncated: info.Truncated,
		Hash:      info.Hash,
		Bytes:     info.Bytes,
	}
}

func traceTool(event Event) *TraceToolV1 {
	tool := &TraceToolV1{}
	if event.Tool != nil {
		tool.Name = event.Tool.Name
		tool.Command = event.Tool.Command
		tool.Path = event.Tool.Path
	}
	if event.GenAI != nil && event.GenAI.Tool != nil {
		if tool.Name == "" {
			tool.Name = event.GenAI.Tool.Name
		}
		if event.GenAI.Tool.Call != nil {
			tool.Arguments = event.GenAI.Tool.Call.Arguments
			tool.Result = event.GenAI.Tool.Call.Result
		}
	}
	return tool
}

func traceUsage(event Event) *TraceUsageV1 {
	if event.GenAI == nil || event.GenAI.Usage == nil {
		return nil
	}
	usage := event.GenAI.Usage
	out := &TraceUsageV1{}
	if usage.InputTokens != nil {
		out.InputTokens = *usage.InputTokens
	}
	if usage.OutputTokens != nil {
		out.OutputTokens = *usage.OutputTokens
	}
	if usage.CacheRead != nil && usage.CacheRead.InputTokens != nil {
		out.CacheReadInputTokens = *usage.CacheRead.InputTokens
	}
	if usage.CacheCreation != nil && usage.CacheCreation.InputTokens != nil {
		out.CacheCreationInputTokens = *usage.CacheCreation.InputTokens
		out.CacheCreation1hInputTokens = usage.CacheCreation.OneHourInputTokens()
	}
	if usage.Reasoning != nil && usage.Reasoning.OutputTokens != nil {
		out.ReasoningOutputTokens = *usage.Reasoning.OutputTokens
	}
	if usage.CostUSD != nil {
		out.CostUSD = *usage.CostUSD
	}
	if traceUsageEmpty(*out) {
		return nil
	}
	return out
}

func traceUsageEmpty(u TraceUsageV1) bool {
	return u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadInputTokens == 0 && u.CacheCreationInputTokens == 0 && u.ReasoningOutputTokens == 0 && u.CostUSD == 0
}

// AddTraceUsage adds delta to u, field by field.
func AddTraceUsage(u *TraceUsageV1, delta TraceUsageV1) {
	u.InputTokens += delta.InputTokens
	u.OutputTokens += delta.OutputTokens
	u.CacheReadInputTokens += delta.CacheReadInputTokens
	u.CacheCreationInputTokens += delta.CacheCreationInputTokens
	u.CacheCreation1hInputTokens += delta.CacheCreation1hInputTokens
	u.ReasoningOutputTokens += delta.ReasoningOutputTokens
	u.CostUSD += delta.CostUSD
}

// TraceBuilder folds one trace's events, in order, into its summary, and keeps the events added
// with Add. The zero value is not usable; start one with NewTraceBuilder or RestoreTraceBuilder.
type TraceBuilder struct {
	summary       TraceSummaryV1
	events        []TraceEventV1
	models        map[string]bool
	methods       map[string]bool
	titlePriority int
}

// TraceBuilderState is a builder's running state between folds, for a caller that persists it: the
// summary before Summary finishes it, and the sets finishing reads. The JSON shape is stored by the
// dashboard's history, so it must not change.
type TraceBuilderState struct {
	Summary       TraceSummaryV1 `json:"summary"`
	Models        []string       `json:"models,omitempty"`
	Methods       []string       `json:"methods,omitempty"`
	TitlePriority int            `json:"title_priority"`
}

// NewTraceBuilder starts the trace id, whose first event is first.
func NewTraceBuilder(id string, first Event) *TraceBuilder {
	return &TraceBuilder{
		summary:       newTraceSummary(id, first),
		models:        map[string]bool{},
		methods:       map[string]bool{},
		titlePriority: -1,
	}
}

// RestoreTraceBuilder continues a trace from a saved State. It holds no events.
func RestoreTraceBuilder(state TraceBuilderState) *TraceBuilder {
	b := &TraceBuilder{
		summary:       state.Summary,
		models:        map[string]bool{},
		methods:       map[string]bool{},
		titlePriority: state.TitlePriority,
	}
	for _, model := range state.Models {
		b.models[model] = true
	}
	for _, method := range state.Methods {
		b.methods[method] = true
	}
	return b
}

// State is the builder's running state, for RestoreTraceBuilder.
func (b *TraceBuilder) State() TraceBuilderState {
	return TraceBuilderState{
		Summary:       b.summary,
		Models:        traceSortedKeys(b.models),
		Methods:       traceSortedKeys(b.methods),
		TitlePriority: b.titlePriority,
	}
}

// Add numbers te as the trace's next event, keeps it, and folds event (the endpoint event te was
// projected from) into the summary. It returns the numbered event.
func (b *TraceBuilder) Add(event Event, te TraceEventV1) TraceEventV1 {
	te.Number = len(b.events) + 1
	b.events = append(b.events, te)
	b.Fold(event, te)
	return te
}

// Fold folds event and its projection into the summary without keeping the projection, for a
// caller that stores events itself.
func (b *TraceBuilder) Fold(event Event, traceEvent TraceEventV1) {
	a := b
	a.summary.EventCount++
	a.summary.LocalEventCount++
	if a.summary.StartedAt == "" {
		a.summary.StartedAt = event.Timestamp
	}
	a.summary.EndedAt = event.Timestamp
	a.summary.UpdatedAt = event.Timestamp
	if event.Harness.CollectionMethod != "" {
		a.methods[event.Harness.CollectionMethod] = true
	}
	if traceEvent.Type == "user_message" || traceEvent.Type == "agent_message" {
		a.summary.LocalMessageCount++
	}
	if p := traceTitlePriority(traceEvent); p > a.titlePriority {
		a.summary.Title = traceTitle(event, traceEvent)
		if preview := tracePreview(traceEvent); preview != "" {
			a.summary.Preview = preview
		}
		a.titlePriority = p
	}
	if event.Session != nil && a.summary.Session == nil {
		a.summary.Session = &TraceSessionV1{ID: event.Session.ID, WorkingDirectory: event.Session.WorkingDirectory}
	}
	if event.Trace != nil {
		if a.summary.Trace == nil {
			a.summary.Trace = &TraceIdentityV1{}
		}
		if a.summary.Trace.ID == "" {
			a.summary.Trace.ID = event.Trace.ID
		}
		if event.Trace.ParentSpanID == "" && event.Trace.SpanID != "" && a.summary.Trace.RootSpanID == "" {
			a.summary.Trace.RootSpanID = event.Trace.SpanID
		}
	}
	if event.Repository != "" || event.Branch != "" {
		if a.summary.Repository == nil {
			a.summary.Repository = &TraceRepositoryV1{}
		}
		if a.summary.Repository.Path == "" {
			a.summary.Repository.Path = event.Repository
		}
		if a.summary.Repository.Branch == "" {
			a.summary.Repository.Branch = event.Branch
		}
	}
	if a.summary.Project == nil {
		dir := strings.TrimSpace(event.Repository)
		if dir == "" && event.Session != nil {
			dir = strings.TrimSpace(event.Session.WorkingDirectory)
		}
		if name := traceProjectName(dir); name != "" {
			a.summary.Project = &TraceProjectV1{Name: name}
		}
	}
	if event.Model != "" {
		model := NormalizeModelName(event.Model)
		if model != "" {
			a.models[model] = true
		}
	}
	if traceEvent.Usage != nil {
		if a.summary.TokenUsage == nil {
			a.summary.TokenUsage = &TraceUsageV1{}
		}
		AddTraceUsage(a.summary.TokenUsage, *traceEvent.Usage)
	}
	updateContentSummary(&a.summary.Content, event.Content)
	a.updateRawMetadata(event.Raw)
}

// Events are the events added with Add, in order.
func (b *TraceBuilder) Events() []TraceEventV1 {
	return b.events
}

// Summary is the trace's finished summary. It does not change the builder, so folding may continue.
func (b *TraceBuilder) Summary() TraceSummaryV1 {
	summary := b.summary
	summary.Harness.CollectionMethods = traceSortedKeys(b.methods)
	models := traceSortedKeys(b.models)
	if len(models) > 0 {
		summary.Model = &TraceModelV1{Names: models, Primary: models[0]}
	}
	if summary.Title == "" {
		summary.Title = summary.ID
	}
	if summary.Preview == "" {
		summary.Preview = summary.Title
	}
	if summary.Content.Retention == "" {
		summary.Content.Retention = "metadata"
	}
	if summary.SharedEventCount == 0 && summary.RemoteEventCount > 0 {
		summary.SharedEventCount = summary.RemoteEventCount
	}
	if summary.RemoteEventCount > 0 && summary.RemoteEventCount != summary.LocalEventCount {
		summary.Sharing.State = "stale"
	}
	return summary
}

// Bundle is the whole trace -- the finished summary, every event added with Add and their spans --
// as a bundle with the given provenance.
func (b *TraceBuilder) Bundle(provenance TraceProvenanceV1) TraceBundleV1 {
	summary := b.Summary()
	events := append([]TraceEventV1{}, b.events...)
	total := len(events)
	return TraceBundleV1{
		SchemaVersion: TraceSchemaVersion,
		ID:            summary.ID,
		Summary:       summary,
		Events:        events,
		Spans:         TraceSpans(events),
		Range:         &TraceRangeV1{TotalEvents: total, ReturnedEvents: total, Offset: 1, Limit: total},
		Provenance:    provenance,
	}
}

func newTraceSummary(id string, event Event) TraceSummaryV1 {
	summary := TraceSummaryV1{
		ID:      id,
		Harness: TraceHarnessV1{Name: event.Harness.Name, Version: event.Harness.Version},
		Content: TraceContentSummaryV1{Retention: "metadata"},
		Sharing: TraceSharingV1{State: "local_only", Visibility: "private"},
	}
	if event.Session != nil {
		summary.Session = &TraceSessionV1{ID: event.Session.ID, WorkingDirectory: event.Session.WorkingDirectory}
	}
	if event.Trace != nil {
		summary.Trace = &TraceIdentityV1{ID: event.Trace.ID}
	}
	return summary
}

// traceProjectName is the last segment of a directory, or "" for a path with no name of its own
// (a root, ".", "~").
func traceProjectName(dir string) string {
	name := lastPathSegment(dir)
	switch name {
	case "", ".", "..", "~":
		return ""
	}
	return name
}

func (b *TraceBuilder) updateRawMetadata(raw map[string]interface{}) {
	if raw == nil {
		return
	}
	a := b
	if namespaceID := traceRawString(raw, "namespace_id"); namespaceID != "" {
		if a.summary.Namespace == nil {
			a.summary.Namespace = &TraceNamespaceV1{}
		}
		a.summary.Namespace.ID = namespaceID
	}
	if namespaceSlug := traceRawString(raw, "namespace_slug"); namespaceSlug != "" {
		if a.summary.Namespace == nil {
			a.summary.Namespace = &TraceNamespaceV1{}
		}
		a.summary.Namespace.Slug = namespaceSlug
	}
	if namespaceName := traceRawString(raw, "namespace_name"); namespaceName != "" {
		if a.summary.Namespace == nil {
			a.summary.Namespace = &TraceNamespaceV1{}
		}
		a.summary.Namespace.Name = namespaceName
	}
	if title := traceFirstNonEmpty(traceRawString(raw, "remote_title"), traceRawString(raw, "remote_ai_title")); title != "" {
		if a.summary.Remote == nil {
			a.summary.Remote = &TraceRemoteV1{}
		}
		a.summary.Remote.Title = title
	}
	if url := traceFirstNonEmpty(traceRawString(raw, "shared_url"), traceRawString(raw, "sharedUrl")); url != "" {
		a.summary.Sharing.URL = url
		a.summary.Sharing.State = "shared"
	}
	if visibility := traceFirstNonEmpty(traceRawString(raw, "visibility"), traceRawString(raw, "shared_visibility")); visibility != "" {
		a.summary.Sharing.Visibility = visibility
	}
	if sharedAt := traceRawString(raw, "shared_at"); sharedAt != "" {
		a.summary.Sharing.SharedAt = sharedAt
	}
	if refreshedAt := traceFirstNonEmpty(traceRawString(raw, "last_refreshed_at"), traceRawString(raw, "synced_at")); refreshedAt != "" {
		a.summary.Sharing.LastRefreshedAt = refreshedAt
		if a.summary.Remote == nil {
			a.summary.Remote = &TraceRemoteV1{}
		}
		a.summary.Remote.SyncedAt = refreshedAt
	}
	if remoteEvents := traceIntFromRaw(raw, "remote_event_count"); remoteEvents > 0 {
		a.summary.RemoteEventCount = remoteEvents
	}
	if sharedEvents := traceIntFromRaw(raw, "shared_event_count"); sharedEvents > 0 {
		a.summary.SharedEventCount = sharedEvents
	}
	if remoteMessages := traceIntFromRaw(raw, "remote_message_count"); remoteMessages > 0 {
		a.summary.RemoteMessageCount = remoteMessages
	}
}

// traceTitlePriority ranks an event as a source for the trace title and
// preview. A trace usually opens on lifecycle noise (session.started,
// session.context), so a later user prompt outranks it and takes the label;
// ties keep the earlier event, so the first prompt names the trace. The
// builder seeds this at -1 rather than 0, so a trace that only ever carries
// lifecycle events is still named by its first one rather than falling back to
// its raw trace ID in Summary.
func traceTitlePriority(event TraceEventV1) int {
	switch event.Type {
	case "user_message":
		return 2
	case "session", "other":
		return 0
	default:
		return 1
	}
}

func traceTitle(event Event, traceEvent TraceEventV1) string {
	content := ""
	if traceEvent.Content != nil {
		content = traceEvent.Content.Text
	}
	for _, value := range []string{content, traceEvent.Title, traceEvent.Summary, event.Message} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return traceTruncate(trimmed, 80)
		}
	}
	return traceEvent.Type
}

func tracePreview(event TraceEventV1) string {
	if event.Content != nil && event.Content.Text != "" {
		return traceTruncate(event.Content.Text, 160)
	}
	return traceTruncate(traceFirstNonEmpty(event.Summary, event.Title), 160)
}

func updateContentSummary(summary *TraceContentSummaryV1, info *ContentInfo) {
	if summary.Retention == "" {
		summary.Retention = "metadata"
	}
	if info == nil {
		return
	}
	switch {
	case info.Redacted:
		summary.HasRedactions = true
	case info.Truncated:
		summary.HasTruncations = true
	}
	if info.Truncated {
		summary.HasTruncations = true
	}
	if summary.Retention == "mixed" || info.Retention == "" {
		return
	}
	if summary.Retention == "metadata" && info.Retention != "metadata" {
		summary.Retention = info.Retention
		return
	}
	if summary.Retention != info.Retention {
		summary.Retention = "mixed"
	}
}

// TraceSpans lists the spans of the given events, sorted by span ID, each named by its first
// event and listing its events in order.
func TraceSpans(events []TraceEventV1) []TraceSpanV1 {
	spans := map[string]*TraceSpanV1{}
	for _, event := range events {
		if event.Trace == nil || event.Trace.SpanID == "" {
			continue
		}
		span := spans[event.Trace.SpanID]
		if span == nil {
			span = &TraceSpanV1{ID: event.Trace.SpanID, ParentSpanID: event.Trace.ParentSpanID, TraceID: event.Trace.ID}
			spans[event.Trace.SpanID] = span
		}
		if span.Name == "" {
			span.Name = traceFirstNonEmpty(event.Summary, event.Title, event.Action)
		}
		span.EventIDs = append(span.EventIDs, event.ID)
	}
	return SortedTraceSpans(spans)
}

// SortedTraceSpans lists spans by ID.
func SortedTraceSpans(spans map[string]*TraceSpanV1) []TraceSpanV1 {
	out := make([]TraceSpanV1, 0, len(spans))
	for _, span := range spans {
		out = append(out, *span)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func traceSortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func traceFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// traceTruncate trims value and cuts it to at most limit bytes without splitting UTF-8.
func traceTruncate(value string, limit int) string {
	return TruncateUTF8(strings.TrimSpace(value), limit)
}

func traceRawString(raw map[string]interface{}, key string) string {
	if raw == nil {
		return ""
	}
	value, ok := raw[key]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

func traceIntFromRaw(raw map[string]interface{}, key string) int {
	value, ok := raw[key]
	if !ok {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		parsed, _ := strconv.Atoi(typed)
		return parsed
	default:
		return 0
	}
}
