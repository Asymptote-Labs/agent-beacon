package dashboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/codexsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/learning"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const codexAnswerLabel = "Codex assistant message"

// writeCodexRollout writes a Codex rollout file with one user prompt and one
// assistant answer under a fake ~/.codex, runs the real `codex sync` collector
// into a runtime log through the real writer, and returns the log path.
func writeCodexRollout(t *testing.T, sessionID, answer string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	codexDir := filepath.Join(dir, ".codex")
	rollout := filepath.Join(codexDir, "sessions", "2026", "09", "22", "rollout-2026-09-22T00-00-00-"+sessionID+".jsonl")
	lines := []string{
		`{"timestamp":"2026-09-22T00:00:00.000Z","type":"session_meta","payload":{"session_id":` + jsonString(sessionID) + `,"id":` + jsonString(sessionID) + `,"timestamp":"2026-09-22T00:00:00.000Z","cwd":"/tmp/repo","originator":"codex-tui","cli_version":"0.153.4","source":"cli"}}`,
		`{"timestamp":"2026-09-22T00:00:01.000Z","type":"response_item","payload":{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"What does the repro check?"}]}}`,
		`{"timestamp":"2026-09-22T00:00:02.000Z","type":"response_item","payload":{"type":"message","id":"a1","role":"assistant","content":[{"type":"output_text","text":` + jsonString(answer) + `}]}}`,
	}
	if err := os.MkdirAll(filepath.Dir(rollout), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rollout, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "logs", "runtime.jsonl")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	summary, err := codexsession.CollectOnce(codexsession.CollectOptions{
		CodexDir:  codexDir,
		StatePath: filepath.Join(dir, "state", "codex.json"),
		LogPath:   logPath,
		Write:     true,
		UserMode:  true,
	})
	if err != nil {
		t.Fatalf("codex CollectOnce: %v", err)
	}
	if summary.EventsEmitted == 0 {
		t.Fatal("codex sync emitted no events")
	}
	return logPath
}

func jsonString(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}

func assistantTraceEvent(t *testing.T, show TraceShowResultV1) TraceEventV1 {
	t.Helper()
	var found []TraceEventV1
	for _, event := range show.Events {
		if event.Type == "agent_message" {
			found = append(found, event)
		}
	}
	if len(found) != 1 {
		t.Fatalf("agent_message events = %d in %#v, want 1", len(found), show.Events)
	}
	return found[0]
}

func showCodexTrace(t *testing.T, logPath, sessionID string) TraceShowResultV1 {
	t.Helper()
	show, ok, err := ShowTrace(logPath, "session:codex_cli:"+sessionID, TraceQuery{Limit: 100})
	if err != nil || !ok {
		t.Fatalf("ShowTrace ok=%v err=%v", ok, err)
	}
	return show
}

// The issue's reproduction, run through the real pipeline: a Codex rollout is
// collected by `codex sync`, projected by `traces show`, and handed to the
// learning projection that Jev reads. The answer must survive every step, and
// the label must stay the event summary.
func TestCodexAssistantAnswerReachesTraceAndLearningProjection(t *testing.T) {
	answer := "BEACON_REPRO: the actual assistant answer"
	logPath := writeCodexRollout(t, "answer-text-repro", answer)

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), answer) {
		t.Fatalf("precondition: runtime log does not retain the answer:\n%s", raw)
	}

	show := showCodexTrace(t, logPath, "answer-text-repro")
	event := assistantTraceEvent(t, show)
	if event.Content == nil || event.Content.Text != answer {
		t.Fatalf("assistant content = %#v, want text %q", event.Content, answer)
	}
	if event.Summary != codexAnswerLabel {
		t.Fatalf("assistant summary = %q, want the event label %q", event.Summary, codexAnswerLabel)
	}
	if event.Content.Retention != "full" || !event.Content.Included || event.Content.Bytes != len(answer) || event.Content.Hash == "" {
		t.Fatalf("content marker not carried through: %#v", event.Content)
	}

	projection := learning.BuildProjection(show)
	var projected []string
	for _, pe := range projection.Events {
		if pe.Type == "agent_message" {
			projected = append(projected, pe.Content)
		}
	}
	if len(projected) != 1 || projected[0] != answer {
		t.Fatalf("learning projection assistant content = %q, want [%q]", projected, answer)
	}
}

// Retained text keeps the limits the writer applied: a credential in the answer
// is redacted before it reaches the trace or the projection, and an answer
// longer than the stored limit arrives bounded and marked truncated.
func TestCodexAssistantAnswerKeepsRedactionAndSizeLimits(t *testing.T) {
	secret := "api_key=abcdef0123456789secretvalue"
	long := "LONG_ANSWER_START " + secret + " " + strings.Repeat("x", asymptoteobserve.DefaultRawStringLimit*2)
	logPath := writeCodexRollout(t, "answer-limits", long)

	show := showCodexTrace(t, logPath, "answer-limits")
	event := assistantTraceEvent(t, show)
	if event.Content == nil || !strings.HasPrefix(event.Content.Text, "LONG_ANSWER_START") {
		t.Fatalf("assistant content = %#v, want the retained answer", event.Content)
	}
	if strings.Contains(event.Content.Text, "abcdef0123456789secretvalue") {
		t.Fatalf("trace content leaked the credential: %q", event.Content.Text)
	}
	if !strings.Contains(event.Content.Text, "[REDACTED]") {
		t.Fatalf("trace content lost the redaction marker: %q", event.Content.Text)
	}
	if len(event.Content.Text) > asymptoteobserve.DefaultRawStringLimit {
		t.Fatalf("trace content is %d bytes, over the stored limit %d", len(event.Content.Text), asymptoteobserve.DefaultRawStringLimit)
	}
	if !event.Content.Truncated || !event.Content.Redacted || event.Content.Bytes != len(long) {
		t.Fatalf("content marker = %#v, want truncated, redacted and the original byte count %d", event.Content, len(long))
	}

	for _, pe := range learning.BuildProjection(show).Events {
		if pe.Type == "agent_message" && strings.Contains(pe.Content, "abcdef0123456789secretvalue") {
			t.Fatalf("learning projection leaked the credential: %q", pe.Content)
		}
	}
}

func assistantEvent(action, message string, messages interface{}) schema.Event {
	event := testSchemaEvent("2026-09-22T00:00:00Z", "codex_cli", action, "agent", "repo-a")
	event.Event.ID = "evt-" + action
	event.Session = &schema.SessionInfo{ID: "s1"}
	event.Message = message
	if messages != nil {
		event.GenAI = &schema.GenAIInfo{Output: &schema.GenAIOutputInfo{Messages: messages}}
	}
	return event
}

// Through the JSONL log, so the messages arrive as a reader decodes them rather
// than as the Go values a mapper built.
func projectedContent(t *testing.T, event schema.Event) *TraceContentV1 {
	t.Helper()
	path := traceStoreIndexedLog(t, marshalEvents(t, event))
	show, ok, err := ShowTrace(path, "session:codex_cli:s1", TraceQuery{Limit: 10})
	if err != nil || !ok || len(show.Events) != 1 {
		t.Fatalf("ShowTrace ok=%v err=%v events=%d", ok, err, len(show.Events))
	}
	return show.Events[0].Content
}

func TestTraceContentPrefersRetainedOutputTextOverLabel(t *testing.T) {
	cases := []struct {
		name     string
		action   string
		label    string
		messages interface{}
		want     string
	}{
		{
			name:     "assistant text part",
			action:   "agent.message",
			label:    "Codex assistant message",
			messages: asymptoteobserve.TextOutputMessages("the answer"),
			want:     "the answer",
		},
		{
			name:   "several text parts and messages join in order",
			action: "agent.message",
			label:  "Codex assistant message",
			messages: []interface{}{
				map[string]interface{}{"role": "assistant", "parts": []interface{}{
					map[string]interface{}{"type": "text", "content": "first"},
					map[string]interface{}{"type": "tool_call", "name": "shell"},
					map[string]interface{}{"type": "text", "content": "second"},
				}},
				map[string]interface{}{"role": "assistant", "parts": []interface{}{
					map[string]interface{}{"type": "text", "content": "third"},
				}},
			},
			want: "first\n\nsecond\n\nthird",
		},
		{
			name:     "reasoning part on a reasoning event",
			action:   "agent.reasoning",
			label:    "Claude Code reasoning observed",
			messages: asymptoteobserve.ReasoningOutputMessages("thinking it through"),
			want:     "thinking it through",
		},
		{
			// The copilot and dsh session mappers write content/text rather than parts/content.
			name:   "content/text message shape",
			action: "agent.reasoning",
			label:  "DeepSeek assistant reasoning",
			messages: []map[string]interface{}{{
				"role":    "assistant",
				"content": []map[string]string{{"type": "reasoning", "text": "dsh reasoning"}},
			}},
			want: "dsh reasoning",
		},
		{
			name:     "bare string messages value is the text",
			action:   "agent.message",
			label:    "Codex assistant message",
			messages: "plain otlp answer",
			want:     "plain otlp answer",
		},
		{
			name:     "no retained output falls back to the label",
			action:   "agent.message",
			label:    "Codex assistant message",
			messages: nil,
			want:     "Codex assistant message",
		},
		{
			name:     "a message event never shows reasoning as what it said",
			action:   "agent.message",
			label:    "Codex assistant message",
			messages: asymptoteobserve.ReasoningOutputMessages("private thought"),
			want:     "Codex assistant message",
		},
		{
			name:     "a reasoning event does not show the answer as its reasoning",
			action:   "agent.reasoning",
			label:    "Agent reasoning captured",
			messages: asymptoteobserve.TextOutputMessages("the answer"),
			want:     "Agent reasoning captured",
		},
		{
			name:     "non-assistant messages are ignored",
			action:   "agent.message",
			label:    "Codex assistant message",
			messages: asymptoteobserve.GenAIMessages("tool", "text", "tool output"),
			want:     "Codex assistant message",
		},
		{
			name:     "whitespace-only text falls back to the label",
			action:   "agent.message",
			label:    "Codex assistant message",
			messages: asymptoteobserve.TextOutputMessages("   \n"),
			want:     "Codex assistant message",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := projectedContent(t, assistantEvent(tc.action, tc.label, tc.messages))
			if content == nil || content.Text != tc.want {
				t.Fatalf("content = %#v, want text %q", content, tc.want)
			}
		})
	}
}

// Parts are bounded one at a time when written, so an event with many of them
// can join to more than any single stored field. The joined copy is held to the
// prompt.text limit and marked truncated.
func TestTraceContentBoundsJoinedOutputText(t *testing.T) {
	part := strings.Repeat("y", asymptoteobserve.DefaultRawStringLimit)
	var parts []interface{}
	for i := 0; i < 4; i++ {
		parts = append(parts, map[string]interface{}{"type": "text", "content": part})
	}
	messages := []interface{}{map[string]interface{}{"role": "assistant", "parts": parts}}
	content := projectedContent(t, assistantEvent("agent.message", codexAnswerLabel, messages))
	if content == nil || !strings.HasPrefix(content.Text, "yyyy") {
		t.Fatalf("content = %#v, want the joined answer", content)
	}
	if len(content.Text) > asymptoteobserve.DefaultStringLimit || !content.Truncated {
		t.Fatalf("joined content is %d bytes (truncated=%v), want at most %d and truncated", len(content.Text), content.Truncated, asymptoteobserve.DefaultStringLimit)
	}
}

// Prompt text still wins on a prompt event even if the event also carries
// output messages.
func TestTraceContentKeepsPromptText(t *testing.T) {
	event := assistantEvent("prompt.submitted", "Prompt submitted to Codex", asymptoteobserve.TextOutputMessages("not the prompt"))
	event.Event.Category = "prompt"
	event.Prompt = &schema.PromptInfo{Text: "the prompt"}
	content := projectedContent(t, event)
	if content == nil || content.Text != "the prompt" {
		t.Fatalf("content = %#v, want the prompt text", content)
	}
}

// The answer is event text, so it is searchable, and the index and the JSONL
// fallback agree on it.
func TestTraceSearchFindsRetainedAssistantText(t *testing.T) {
	lines := marshalEvents(t, assistantEvent("agent.message", codexAnswerLabel, asymptoteobserve.TextOutputMessages("rotate the flaky fixture")))
	for name, path := range map[string]string{
		"index":    traceStoreIndexedLog(t, lines),
		"fallback": traceStoreBlockedLog(t, lines),
	} {
		t.Run(name, func(t *testing.T) {
			result, err := SearchTraces(path, TraceQuery{EventQuery: EventQuery{Q: "flaky fixture"}, ResultLevel: "event", Limit: 10})
			if err != nil {
				t.Fatalf("SearchTraces: %v", err)
			}
			if result.TotalMatched != 1 || len(result.Events) != 1 || result.Events[0].Event.Type != "agent_message" {
				t.Fatalf("search results = %#v, want the assistant event", result)
			}
		})
	}
}

// A trace index written before this change holds the label as the assistant
// content, and its fingerprint still matches an unchanged log, so it would
// keep serving the label. The schema version bump makes it rebuild.
func TestTraceStoreRebuildsIndexFromBeforeRetainedOutputText(t *testing.T) {
	answer := "the retained answer"
	path := traceStoreIndexedLog(t, marshalEvents(t, assistantEvent("agent.message", codexAnswerLabel, asymptoteobserve.TextOutputMessages(answer))))
	if _, err := TraceStoreStatus(path); err != nil {
		t.Fatalf("index trace store: %v", err)
	}

	// Rewrite the index as the previous build left it: schema version 3, with
	// the label projected as the content.
	db, err := openTraceStore(path).db()
	if err != nil {
		t.Fatalf("open trace store: %v", err)
	}
	stale, err := json.Marshal(TraceEventV1{ID: "evt-agent.message", Number: 1, Type: "agent_message", Action: "agent.message", Summary: codexAnswerLabel, Content: &TraceContentV1{Text: codexAnswerLabel, Retention: "full", Included: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE trace_events SET event_json = ? WHERE event_type = 'agent_message'`, string(stale)); err != nil {
		t.Fatalf("write stale event: %v", err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 3`); err != nil {
		t.Fatalf("stamp old version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	show, ok, err := ShowTrace(path, "session:codex_cli:s1", TraceQuery{Limit: 10})
	if err != nil || !ok || len(show.Events) != 1 {
		t.Fatalf("ShowTrace ok=%v err=%v events=%d", ok, err, len(show.Events))
	}
	if content := show.Events[0].Content; content == nil || content.Text != answer {
		t.Fatalf("content = %#v, want the rebuilt %q rather than the stale label", content, answer)
	}
}
