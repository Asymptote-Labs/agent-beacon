package dashboard

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const TraceSchemaVersion = asymptoteobserve.TraceSchemaVersion

type TraceBundleV1 = asymptoteobserve.TraceBundleV1
type TraceProvenanceV1 = asymptoteobserve.TraceProvenanceV1
type TraceSummaryV1 = asymptoteobserve.TraceSummaryV1
type TraceHarnessV1 = asymptoteobserve.TraceHarnessV1
type TraceSessionV1 = asymptoteobserve.TraceSessionV1
type TraceIdentityV1 = asymptoteobserve.TraceIdentityV1
type TraceRepositoryV1 = asymptoteobserve.TraceRepositoryV1
type TraceNamespaceV1 = asymptoteobserve.TraceNamespaceV1
type TraceModelV1 = asymptoteobserve.TraceModelV1
type TraceRemoteV1 = asymptoteobserve.TraceRemoteV1
type TraceSharingV1 = asymptoteobserve.TraceSharingV1
type TraceActorV1 = asymptoteobserve.TraceActorV1
type TraceContentSummaryV1 = asymptoteobserve.TraceContentSummaryV1
type TraceEventV1 = asymptoteobserve.TraceEventV1
type TraceEventTraceV1 = asymptoteobserve.TraceEventTraceV1
type TraceContentV1 = asymptoteobserve.TraceContentV1
type TraceToolV1 = asymptoteobserve.TraceToolV1
type TraceCommandV1 = asymptoteobserve.TraceCommandV1
type TraceFileV1 = asymptoteobserve.TraceFileV1
type TraceMCPV1 = asymptoteobserve.TraceMCPV1
type TraceApprovalV1 = asymptoteobserve.TraceApprovalV1
type TraceUsageV1 = asymptoteobserve.TraceUsageV1
type TraceSpanV1 = asymptoteobserve.TraceSpanV1
type TraceRangeV1 = asymptoteobserve.TraceRangeV1
type TraceListResultV1 = asymptoteobserve.TraceListResultV1
type TraceSearchResultV1 = asymptoteobserve.TraceSearchResultV1
type TraceEventMatchV1 = asymptoteobserve.TraceEventMatchV1
type TraceShowResultV1 = asymptoteobserve.TraceShowResultV1

type TraceQuery struct {
	EventQuery
	Limit       int
	Page        int
	State       string
	Visibility  string
	EventTypes  []string
	ResultLevel string
	Offset      int
	AroundEvent int
	Before      int
	After       int
}

type traceAggregate struct {
	summary TraceSummaryV1
	events  []TraceEventV1
	spans   map[string]*TraceSpanV1
	models  map[string]bool
	methods map[string]bool
}

func ReadTraceList(path string, query TraceQuery) (TraceListResultV1, error) {
	traces, err := readTraceAggregates(path, query.EventQuery)
	if err != nil {
		return TraceListResultV1{}, err
	}
	filtered := filterTraceSummaries(traces, query)
	sortTraceSummaries(filtered)
	limit := normalizeLimit(query.Limit)
	page := query.Page
	if page <= 0 {
		page = 1
	}
	start := (page - 1) * limit
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return TraceListResultV1{
		Traces:       filtered[start:end],
		TotalMatched: len(filtered),
		Returned:     end - start,
		Limit:        limit,
		Page:         page,
		Truncated:    end < len(filtered),
		Filters:      activeTraceFilters(query),
	}, nil
}

func SearchTraces(path string, query TraceQuery) (TraceSearchResultV1, error) {
	if query.ResultLevel == "" {
		query.ResultLevel = "trace"
	}
	limit := normalizeLimit(query.Limit)
	traces, err := readTraceAggregates(path, withoutFreeText(query.EventQuery))
	if err != nil {
		return TraceSearchResultV1{}, err
	}
	filtered := filterTraceSummaries(traces, query)
	needle := strings.TrimSpace(query.Q)
	resp := TraceSearchResultV1{ResultLevel: query.ResultLevel, Limit: limit, Filters: activeTraceFilters(query)}
	switch query.ResultLevel {
	case "event":
		for _, summary := range filtered {
			agg := traces[summary.ID]
			for _, event := range agg.events {
				if needle != "" && !traceEventMatches(event, needle) {
					continue
				}
				resp.TotalMatched++
				if len(resp.Events) < limit {
					resp.Events = append(resp.Events, TraceEventMatchV1{
						Trace:   agg.summary,
						Event:   event,
						Snippet: traceSnippet(event, needle),
						Score:   1,
					})
				}
			}
		}
	default:
		resp.ResultLevel = "trace"
		matched := make([]TraceSummaryV1, 0, len(filtered))
		for _, summary := range filtered {
			if needle == "" || traceSummaryMatches(summary, needle) {
				matched = append(matched, summary)
			}
		}
		sortTraceSummaries(matched)
		resp.TotalMatched = len(matched)
		if len(matched) > limit {
			resp.Traces = matched[:limit]
		} else {
			resp.Traces = matched
		}
	}
	if resp.ResultLevel == "event" {
		resp.Returned = len(resp.Events)
	} else {
		resp.Returned = len(resp.Traces)
	}
	return resp, nil
}

func ShowTrace(path, id string, query TraceQuery) (TraceShowResultV1, bool, error) {
	traces, err := readTraceAggregates(path, query.EventQuery)
	if err != nil {
		return TraceShowResultV1{}, false, err
	}
	agg := traces[id]
	if agg == nil {
		return TraceShowResultV1{}, false, nil
	}
	events := filterTraceEvents(agg.events, query.EventTypes)
	total := len(events)
	offset, limit := traceRange(query, total)
	end := offset - 1 + limit
	if end > total {
		end = total
	}
	if offset-1 > end {
		end = offset - 1
	}
	resp := TraceShowResultV1{
		Trace:  agg.summary,
		Events: append([]TraceEventV1(nil), events[offset-1:end]...),
		Spans:  traceSpanList(agg.spans),
		Range: TraceRangeV1{
			TotalEvents:    total,
			ReturnedEvents: end - (offset - 1),
			Offset:         offset,
			Limit:          limit,
			AroundEvent:    query.AroundEvent,
		},
	}
	return resp, true, nil
}

func TraceBundleFromShow(show TraceShowResultV1, beaconVersion string) TraceBundleV1 {
	return TraceBundleV1{
		SchemaVersion: TraceSchemaVersion,
		ID:            show.Trace.ID,
		Summary:       show.Trace,
		Events:        show.Events,
		Spans:         show.Spans,
		Range:         &show.Range,
		Provenance: TraceProvenanceV1{
			Source:        "runtime_jsonl",
			GeneratedAt:   asymptoteobserve.FormatTimestamp(time.Now()),
			BeaconVersion: beaconVersion,
		},
	}
}

func readTraceAggregates(path string, query EventQuery) (map[string]*traceAggregate, error) {
	query.NoLimit = true
	result, err := ReadEvents(path, query)
	if err != nil {
		return nil, err
	}
	SortRecordsAppendOrder(result.Events)
	traces := map[string]*traceAggregate{}
	for _, record := range result.Events {
		event := record.Event
		id := traceProjectionID(event)
		agg := traces[id]
		if agg == nil {
			agg = &traceAggregate{
				summary: newTraceSummary(id, event),
				spans:   map[string]*TraceSpanV1{},
				models:  map[string]bool{},
				methods: map[string]bool{},
			}
			traces[id] = agg
		}
		te := traceEventFromRecord(record, len(agg.events)+1)
		agg.events = append(agg.events, te)
		agg.update(event, te)
	}
	for _, agg := range traces {
		agg.finish()
	}
	return traces, nil
}

func newTraceSummary(id string, event schema.Event) TraceSummaryV1 {
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

func (a *traceAggregate) update(event schema.Event, traceEvent TraceEventV1) {
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
	if a.summary.Title == "" {
		a.summary.Title = traceTitle(event, traceEvent)
	}
	if a.summary.Preview == "" {
		a.summary.Preview = tracePreview(traceEvent)
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
		if event.Trace.SpanID != "" {
			span := a.spans[event.Trace.SpanID]
			if span == nil {
				span = &TraceSpanV1{ID: event.Trace.SpanID, ParentSpanID: event.Trace.ParentSpanID, TraceID: event.Trace.ID}
				a.spans[event.Trace.SpanID] = span
			}
			if span.Name == "" {
				span.Name = firstNonEmpty(event.Message, event.Event.Action)
			}
			span.EventIDs = append(span.EventIDs, traceEvent.ID)
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
	if event.Model != "" {
		model := asymptoteobserve.NormalizeModelName(event.Model)
		if model != "" {
			a.models[model] = true
		}
	}
	if traceEvent.Usage != nil {
		if a.summary.TokenUsage == nil {
			a.summary.TokenUsage = &TraceUsageV1{}
		}
		addTraceUsage(a.summary.TokenUsage, *traceEvent.Usage)
	}
	updateContentSummary(&a.summary.Content, event.Content)
	a.updateRawMetadata(event.Raw)
}

func (a *traceAggregate) updateRawMetadata(raw map[string]interface{}) {
	if raw == nil {
		return
	}
	if namespaceID := rawString(raw, "namespace_id"); namespaceID != "" {
		if a.summary.Namespace == nil {
			a.summary.Namespace = &TraceNamespaceV1{}
		}
		a.summary.Namespace.ID = namespaceID
	}
	if namespaceSlug := rawString(raw, "namespace_slug"); namespaceSlug != "" {
		if a.summary.Namespace == nil {
			a.summary.Namespace = &TraceNamespaceV1{}
		}
		a.summary.Namespace.Slug = namespaceSlug
	}
	if namespaceName := rawString(raw, "namespace_name"); namespaceName != "" {
		if a.summary.Namespace == nil {
			a.summary.Namespace = &TraceNamespaceV1{}
		}
		a.summary.Namespace.Name = namespaceName
	}
	if title := firstNonEmpty(rawString(raw, "remote_title"), rawString(raw, "remote_ai_title")); title != "" {
		if a.summary.Remote == nil {
			a.summary.Remote = &TraceRemoteV1{}
		}
		a.summary.Remote.Title = title
	}
	if url := firstNonEmpty(rawString(raw, "shared_url"), rawString(raw, "sharedUrl")); url != "" {
		a.summary.Sharing.URL = url
		a.summary.Sharing.State = "shared"
	}
	if visibility := firstNonEmpty(rawString(raw, "visibility"), rawString(raw, "shared_visibility")); visibility != "" {
		a.summary.Sharing.Visibility = visibility
	}
	if sharedAt := rawString(raw, "shared_at"); sharedAt != "" {
		a.summary.Sharing.SharedAt = sharedAt
	}
	if refreshedAt := firstNonEmpty(rawString(raw, "last_refreshed_at"), rawString(raw, "synced_at")); refreshedAt != "" {
		a.summary.Sharing.LastRefreshedAt = refreshedAt
		if a.summary.Remote == nil {
			a.summary.Remote = &TraceRemoteV1{}
		}
		a.summary.Remote.SyncedAt = refreshedAt
	}
	if remoteEvents := intFromRaw(raw, "remote_event_count"); remoteEvents > 0 {
		a.summary.RemoteEventCount = remoteEvents
	}
	if sharedEvents := intFromRaw(raw, "shared_event_count"); sharedEvents > 0 {
		a.summary.SharedEventCount = sharedEvents
	}
	if remoteMessages := intFromRaw(raw, "remote_message_count"); remoteMessages > 0 {
		a.summary.RemoteMessageCount = remoteMessages
	}
}

func (a *traceAggregate) finish() {
	a.summary.Harness.CollectionMethods = sortedKeys(a.methods)
	models := sortedKeys(a.models)
	if len(models) > 0 {
		a.summary.Model = &TraceModelV1{Names: models, Primary: models[0]}
	}
	if a.summary.Title == "" {
		a.summary.Title = a.summary.ID
	}
	if a.summary.Preview == "" {
		a.summary.Preview = a.summary.Title
	}
	if a.summary.Content.Retention == "" {
		a.summary.Content.Retention = "metadata"
	}
	if a.summary.SharedEventCount == 0 && a.summary.RemoteEventCount > 0 {
		a.summary.SharedEventCount = a.summary.RemoteEventCount
	}
	if a.summary.RemoteEventCount > 0 && a.summary.RemoteEventCount != a.summary.LocalEventCount {
		a.summary.Sharing.State = "stale"
	}
}

func traceEventFromRecord(record EventRecord, number int) TraceEventV1 {
	event := record.Event
	id := event.Event.ID
	if id == "" {
		id = record.ID
	}
	te := TraceEventV1{
		ID:            id,
		Number:        number,
		Timestamp:     event.Timestamp,
		Type:          traceEventType(event),
		Action:        event.Event.Action,
		Category:      event.Event.Category,
		Fidelity:      event.Event.Fidelity,
		Actor:         traceActor(event),
		Title:         traceEventTitle(event),
		Summary:       event.Message,
		Model:         asymptoteobserve.NormalizeModelName(event.Model),
		SourceEventID: id,
	}
	if event.Trace != nil {
		te.Trace = &TraceEventTraceV1{ID: event.Trace.ID, SpanID: event.Trace.SpanID, ParentSpanID: event.Trace.ParentSpanID}
	}
	if event.GenAI != nil && event.GenAI.Tool != nil && event.GenAI.Tool.Call != nil {
		te.ToolCallID = event.GenAI.Tool.Call.ID
	}
	if content := eventContent(event); content != nil {
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
	if event.Approval != nil {
		te.Approval = &TraceApprovalV1{Required: event.Approval.Required, Decision: event.Approval.Decision, Reason: event.Approval.Reason}
	}
	if usage := traceUsage(event); usage != nil {
		te.Usage = usage
	}
	return te
}

func traceProjectionID(event schema.Event) string {
	if event.Trace != nil && strings.TrimSpace(event.Trace.ID) != "" {
		return "trace:" + strings.TrimSpace(event.Trace.ID)
	}
	if event.Session != nil && strings.TrimSpace(event.Session.ID) != "" {
		harness := asymptoteobserve.NormalizeHarnessName(event.Harness.Name)
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

func traceEventType(event schema.Event) string {
	action := strings.ToLower(event.Event.Action)
	category := strings.ToLower(event.Event.Category)
	switch {
	case category == "prompt" || strings.HasPrefix(action, "prompt."):
		return "user_message"
	case action == "agent.reasoning" || strings.Contains(action, "reasoning"):
		return "agent_reasoning"
	case action == "agent.message" || action == "assistant.message":
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

func traceActor(event schema.Event) string {
	switch traceEventType(event) {
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

func traceTitle(event schema.Event, traceEvent TraceEventV1) string {
	content := ""
	if traceEvent.Content != nil {
		content = traceEvent.Content.Text
	}
	for _, value := range []string{content, traceEvent.Title, traceEvent.Summary, event.Message} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return truncateString(trimmed, 80)
		}
	}
	return traceEvent.Type
}

func tracePreview(event TraceEventV1) string {
	if event.Content != nil && event.Content.Text != "" {
		return truncateString(event.Content.Text, 160)
	}
	return truncateString(firstNonEmpty(event.Summary, event.Title), 160)
}

func traceEventTitle(event schema.Event) string {
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

func eventContent(event schema.Event) *TraceContentV1 {
	if event.Prompt != nil && event.Prompt.Text != "" {
		return contentFromText(event.Prompt.Text, event.Content)
	}
	if event.Message != "" && (traceEventType(event) == "agent_message" || traceEventType(event) == "agent_reasoning") {
		return contentFromText(event.Message, event.Content)
	}
	return contentFromContentInfo(event.Content)
}

func contentFromText(text string, info *schema.ContentInfo) *TraceContentV1 {
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

func contentFromContentInfo(info *schema.ContentInfo) *TraceContentV1 {
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

func traceTool(event schema.Event) *TraceToolV1 {
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

func traceUsage(event schema.Event) *TraceUsageV1 {
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

func addTraceUsage(u *TraceUsageV1, delta TraceUsageV1) {
	u.InputTokens += delta.InputTokens
	u.OutputTokens += delta.OutputTokens
	u.CacheReadInputTokens += delta.CacheReadInputTokens
	u.CacheCreationInputTokens += delta.CacheCreationInputTokens
	u.ReasoningOutputTokens += delta.ReasoningOutputTokens
	u.CostUSD += delta.CostUSD
}

func updateContentSummary(summary *TraceContentSummaryV1, info *schema.ContentInfo) {
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

func filterTraceSummaries(traces map[string]*traceAggregate, query TraceQuery) []TraceSummaryV1 {
	out := make([]TraceSummaryV1, 0, len(traces))
	for _, agg := range traces {
		if query.State != "" && !strings.EqualFold(agg.summary.Sharing.State, query.State) {
			continue
		}
		if query.Visibility != "" && !strings.EqualFold(agg.summary.Sharing.Visibility, query.Visibility) {
			continue
		}
		out = append(out, agg.summary)
	}
	return out
}

func sortTraceSummaries(traces []TraceSummaryV1) {
	sort.SliceStable(traces, func(i, j int) bool {
		it, _ := schema.ParseTimestamp(traces[i].UpdatedAt)
		jt, _ := schema.ParseTimestamp(traces[j].UpdatedAt)
		if !it.Equal(jt) {
			return it.After(jt)
		}
		return traces[i].ID < traces[j].ID
	})
}

func filterTraceEvents(events []TraceEventV1, eventTypes []string) []TraceEventV1 {
	if len(eventTypes) == 0 {
		return events
	}
	allowed := map[string]bool{}
	for _, eventType := range eventTypes {
		allowed[strings.ToLower(strings.TrimSpace(eventType))] = true
	}
	out := make([]TraceEventV1, 0, len(events))
	for _, event := range events {
		if allowed[strings.ToLower(event.Type)] {
			out = append(out, event)
		}
	}
	return out
}

func traceRange(query TraceQuery, total int) (int, int) {
	limit := normalizeLimit(query.Limit)
	offset := query.Offset
	if offset <= 0 {
		offset = 1
	}
	if query.AroundEvent > 0 {
		before, after := query.Before, query.After
		if before <= 0 {
			before = 3
		}
		if after <= 0 {
			after = 3
		}
		offset = query.AroundEvent - before
		if offset < 1 {
			offset = 1
		}
		limit = before + 1 + after
	}
	if total == 0 {
		return 1, 0
	}
	if offset > total {
		offset = total + 1
		return offset, 0
	}
	if limit > total-offset+1 {
		limit = total - offset + 1
	}
	return offset, limit
}

func traceSpanList(spans map[string]*TraceSpanV1) []TraceSpanV1 {
	out := make([]TraceSpanV1, 0, len(spans))
	for _, span := range spans {
		out = append(out, *span)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func traceSummaryMatches(summary TraceSummaryV1, query string) bool {
	haystack := strings.ToLower(strings.Join([]string{
		summary.ID,
		summary.Title,
		summary.Preview,
		summary.Harness.Name,
		summary.Harness.Version,
		valueOrEmpty(summary.Session, func(s *TraceSessionV1) string { return s.ID + " " + s.WorkingDirectory }),
		valueOrEmpty(summary.Repository, func(r *TraceRepositoryV1) string { return r.RemoteURL + " " + r.Branch + " " + r.Ref + " " + r.Path }),
		valueOrEmpty(summary.Namespace, func(n *TraceNamespaceV1) string { return n.ID + " " + n.Slug + " " + n.Name }),
		summary.Sharing.State,
		summary.Sharing.Visibility,
		summary.Sharing.URL,
	}, "\x00"))
	for _, term := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}

func traceEventMatches(event TraceEventV1, query string) bool {
	haystack := strings.ToLower(strings.Join([]string{
		event.ID,
		event.Type,
		event.Action,
		event.Category,
		event.Actor,
		event.Title,
		event.Summary,
		event.Model,
		valueOrEmpty(event.Content, func(c *TraceContentV1) string { return c.Text + " " + c.Hash }),
		valueOrEmpty(event.Tool, func(t *TraceToolV1) string {
			return t.Name + " " + t.Command + " " + t.Path + " " + fmt.Sprint(t.Arguments) + " " + fmt.Sprint(t.Result)
		}),
		valueOrEmpty(event.Command, func(c *TraceCommandV1) string {
			return c.Command + " " + valueOrEmpty(c.Output, func(o *TraceContentV1) string { return o.Text })
		}),
		valueOrEmpty(event.File, func(f *TraceFileV1) string {
			return f.Path + " " + f.Operation + " " + f.Language + " " + f.DiffHash + " " + valueOrEmpty(f.Diff, func(d *TraceContentV1) string { return d.Text })
		}),
		valueOrEmpty(event.MCP, func(m *TraceMCPV1) string { return m.Server + " " + m.Tool + " " + m.Method + " " + m.ResourceURI }),
		valueOrEmpty(event.Approval, func(a *TraceApprovalV1) string { return a.Decision + " " + a.Reason }),
	}, "\x00"))
	for _, term := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}

func traceSnippet(event TraceEventV1, query string) string {
	candidates := []string{event.Summary, event.Title}
	if event.Content != nil {
		candidates = append([]string{event.Content.Text}, candidates...)
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if query == "" || strings.Contains(strings.ToLower(candidate), strings.ToLower(strings.Fields(query)[0])) {
			return truncateString(candidate, 240)
		}
	}
	return ""
}

func activeTraceFilters(query TraceQuery) map[string]string {
	filters := activeFilters(query.EventQuery)
	if filters == nil {
		filters = map[string]string{}
	}
	add := func(key, value string) {
		if strings.TrimSpace(value) != "" {
			filters[key] = strings.TrimSpace(value)
		}
	}
	add("state", query.State)
	add("visibility", query.Visibility)
	add("result_level", query.ResultLevel)
	if len(query.EventTypes) > 0 {
		filters["event_type"] = strings.Join(query.EventTypes, ",")
	}
	if len(filters) == 0 {
		return nil
	}
	return filters
}

func ParseTraceQuery(r *http.Request, fallbackLimit int) TraceQuery {
	q := r.URL.Query()
	query := TraceQuery{
		EventQuery:  parseQuery(r, fallbackLimit),
		State:       q.Get("state"),
		Visibility:  q.Get("visibility"),
		ResultLevel: q.Get("result_level"),
		EventTypes:  splitCSV(q.Get("event_type")),
	}
	query.Limit = query.EventQuery.Limit
	query.Page, _ = strconv.Atoi(q.Get("page"))
	query.Offset, _ = strconv.Atoi(q.Get("offset"))
	query.AroundEvent, _ = strconv.Atoi(q.Get("around_event"))
	query.Before, _ = strconv.Atoi(q.Get("before"))
	query.After, _ = strconv.Atoi(q.Get("after"))
	return query
}

func withoutFreeText(query EventQuery) EventQuery {
	query.Q = ""
	return query
}

func splitCSV(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func truncateString(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func intFromRaw(raw map[string]interface{}, key string) int {
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

func valueOrEmpty[T any](value *T, render func(*T) string) string {
	if value == nil {
		return ""
	}
	return render(value)
}
