package writer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestAppendEventWritesSingleJSONLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := schema.NewEvent(schema.NewEventOptions{
		Action:  "agent.detected",
		Harness: schema.HarnessInfo{Name: "test"},
		Message: "test event",
	})
	written, err := AppendEvent(event, Options{Path: path})
	if err != nil {
		t.Fatalf("AppendEvent returned error: %v", err)
	}
	if written != path {
		t.Fatalf("expected path %q, got %q", path, written)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected one line, got %d", len(lines))
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &decoded); err != nil {
		t.Fatalf("line is not JSON: %v", err)
	}
	if decoded["vendor"] != schema.Vendor {
		t.Fatalf("unexpected vendor: %v", decoded["vendor"])
	}
}

func TestAppendEventRedactsSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := schema.NewEvent(schema.NewEventOptions{
		Action:  "tool.invoked",
		Harness: schema.HarnessInfo{Name: "test"},
		Message: "token=super-secret-value",
	})
	if _, err := AppendEvent(event, Options{Path: path}); err != nil {
		t.Fatalf("AppendEvent returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.Contains(string(data), "super-secret-value") {
		t.Fatalf("secret was not redacted: %s", string(data))
	}
}

func TestAppendEventRedactsNestedSlices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := schema.NewEvent(schema.NewEventOptions{
		Action:  "tool.invoked",
		Harness: schema.HarnessInfo{Name: "test"},
		Message: "nested event",
	})
	event.Raw = map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"value": "authorization: Bearer secret-token"},
		},
	}
	if _, err := AppendEvent(event, Options{Path: path}); err != nil {
		t.Fatalf("AppendEvent returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.Contains(string(data), "secret-token") {
		t.Fatalf("nested secret was not redacted: %s", string(data))
	}
}

func TestAppendEventRotatesExistingLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.jsonl")
	if err := os.WriteFile(path, []byte("old log contents"), 0644); err != nil {
		t.Fatalf("write existing log: %v", err)
	}
	event := schema.NewEvent(schema.NewEventOptions{
		Action:  "agent.detected",
		Harness: schema.HarnessInfo{Name: "test"},
		Message: "new event",
	})

	if _, err := AppendEvent(event, Options{Path: path, RotateSize: 1}); err != nil {
		t.Fatalf("AppendEvent returned error: %v", err)
	}
	rotated, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("read rotated log: %v", err)
	}
	if string(rotated) != "old log contents" {
		t.Fatalf("rotated log = %q", string(rotated))
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current log: %v", err)
	}
	if !strings.Contains(string(current), "new event") {
		t.Fatalf("current log missing new event: %s", string(current))
	}
}

func TestAppendEventPrunesNumberedArchives(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.jsonl")
	for i := 0; i <= 3; i++ {
		target := path
		if i > 0 {
			target = path + "." + strconv.Itoa(i)
		}
		if err := os.WriteFile(target, []byte("log-"+strconv.Itoa(i)), 0644); err != nil {
			t.Fatalf("write %s: %v", target, err)
		}
	}
	event := schema.NewEvent(schema.NewEventOptions{
		Action:  "agent.detected",
		Harness: schema.HarnessInfo{Name: "test"},
		Message: "new event",
	})

	if _, err := AppendEvent(event, Options{Path: path, RotateSize: 1, RotateArchives: 2}); err != nil {
		t.Fatalf("AppendEvent returned error: %v", err)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("expected .3 archive to be pruned, err=%v", err)
	}
	if data, err := os.ReadFile(path + ".1"); err != nil || string(data) != "log-0" {
		t.Fatalf(".1 = %q err=%v, want prior active log", string(data), err)
	}
	if data, err := os.ReadFile(path + ".2"); err != nil || string(data) != "log-1" {
		t.Fatalf(".2 = %q err=%v, want prior .1 archive", string(data), err)
	}
}

func TestAppendEventDedupesRuntimeEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := schema.NewEvent(schema.NewEventOptions{
		Action:  "mcp.tool_invoked",
		Harness: schema.HarnessInfo{Name: "cursor"},
		Message: "Tool execution observed",
	})
	event.Timestamp = "2026-06-18T21:11:24Z"
	event.Session = &schema.SessionInfo{ID: "s1"}
	event.MCP = &schema.MCPInfo{Server: "clickhouse", Tool: "execute_sql"}
	duplicate := event
	duplicate.Harness.Name = "claude"
	duplicate.Timestamp = "2026-06-18T21:11:25Z"

	if _, err := AppendEvent(event, Options{Path: path}); err != nil {
		t.Fatalf("first AppendEvent returned error: %v", err)
	}
	if _, err := AppendEvent(duplicate, Options{Path: path}); err != nil {
		t.Fatalf("second AppendEvent returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected duplicate event to be suppressed, got %d lines: %s", len(lines), string(data))
	}
}

func TestAppendEventSerializesConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := schema.NewEvent(schema.NewEventOptions{
		Action:  "agent.detected",
		Harness: schema.HarnessInfo{Name: "test"},
		Message: "concurrent event",
	})
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := AppendEvent(event, Options{Path: path, RotateSize: 64 * 1024})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("AppendEvent returned error: %v", err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 20 {
		t.Fatalf("expected 20 complete lines, got %d: %s", len(lines), string(data))
	}
	for _, line := range lines {
		var decoded map[string]interface{}
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("line is not JSON: %v line=%q", err, line)
		}
	}
}

func TestLastLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	if _, err := LastLine(path); err == nil {
		t.Fatal("expected missing file error")
	}
	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	last, err := LastLine(path)
	if err != nil {
		t.Fatalf("LastLine returned error: %v", err)
	}
	if last != "second" {
		t.Fatalf("LastLine = %q, want second", last)
	}
}

func TestAppendEventRejectsInvalidEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := schema.NewEvent(schema.NewEventOptions{
		Action:  "tool.invoked",
		Harness: schema.HarnessInfo{Name: "test"},
	})
	event.Event.Action = ""

	if _, err := AppendEvent(event, Options{Path: path}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestAppendEventRejectsOversizedEventAfterTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := schema.NewEvent(schema.NewEventOptions{
		Action:  "tool.invoked",
		Harness: schema.HarnessInfo{Name: strings.Repeat("h", 2048)},
		Message: strings.Repeat("m", 2048),
	})

	if _, err := AppendEvent(event, Options{Path: path, MaxBytes: 512}); err == nil {
		t.Fatal("expected oversized event error")
	}
}

func TestSanitizeEventRedactsToolPolicyAndRawValues(t *testing.T) {
	event := schema.NewEvent(schema.NewEventOptions{
		Action:  "tool.invoked",
		Harness: schema.HarnessInfo{Name: "test"},
		Message: "authorization=Bearer message-secret",
	})
	event.Tool = &schema.ToolInfo{
		Command: "curl -H 'Authorization: Bearer command-secret'",
		Path:    strings.Repeat("p", 3000),
	}
	event.Policy = &schema.PolicyInfo{Reason: "api_key=policy-secret"}
	event.Prompt = &schema.PromptInfo{Text: "token=prompt-secret"}
	event.Raw = map[string]interface{}{
		"nested": map[string]interface{}{"token": "token=raw-secret"},
	}

	sanitized := SanitizeEvent(event, MaxEventBytes)
	data, err := json.Marshal(sanitized)
	if err != nil {
		t.Fatalf("marshal sanitized event: %v", err)
	}
	text := string(data)
	for _, secret := range []string{"message-secret", "command-secret", "policy-secret", "prompt-secret", "raw-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("secret %q was not redacted: %s", secret, text)
		}
	}
	if len(sanitized.Tool.Path) > 2048 {
		t.Fatalf("tool path was not truncated: %d", len(sanitized.Tool.Path))
	}
}

var uuidV5Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestAppendEventStampsAStableEventID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := schema.NewEvent(schema.NewEventOptions{
		Action:  "command.executed",
		Harness: schema.HarnessInfo{Name: "claude_code"},
		Message: "Shell command executed",
	})
	event.Timestamp = "2026-08-21T18:00:01Z"
	event.Session = &schema.SessionInfo{ID: "s1"}
	event.Command = &schema.CommandInfo{Command: "echo hi"}
	event.GenAI = &schema.GenAIInfo{Tool: &schema.GenAIToolInfo{Call: &schema.GenAIToolCallInfo{ID: "toolu_1"}}}

	if _, err := AppendEvent(event, Options{Path: path}); err != nil {
		t.Fatalf("AppendEvent returned error: %v", err)
	}

	line, err := LastLine(path)
	if err != nil {
		t.Fatalf("read last line: %v", err)
	}
	var written schema.Event
	if err := json.Unmarshal([]byte(line), &written); err != nil {
		t.Fatalf("decode written event: %v", err)
	}
	if !uuidV5Pattern.MatchString(written.Event.ID) {
		t.Fatalf("event.id = %q, want an RFC 4122 version 5 UUID", written.Event.ID)
	}
	// The ID has to describe the line it is on, so re-deriving it from that line
	// with the field cleared has to land on the same value.
	bare := written
	bare.Event.ID = ""
	data, err := json.Marshal(bare)
	if err != nil {
		t.Fatalf("re-marshal event: %v", err)
	}
	if got := asymptoteobserve.EventIDForLine(data); got != written.Event.ID {
		t.Fatalf("event.id = %s, but the written line derives %s", written.Event.ID, got)
	}
}

// The regression the call ID exists to fix: after harness normalization both
// capture paths report claude_code, and the collector's batch lands seconds
// after the hook's write. Nothing but a shared call ID can collapse that pair.
func TestAppendEventCollapsesTheHookAndCollectorReportsOfOneCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	hook := schema.NewEvent(schema.NewEventOptions{
		Action:  "command.executed",
		Harness: schema.HarnessInfo{Name: "claude_code"},
		Message: "Shell command executed",
	})
	hook.Timestamp = "2026-08-21T18:00:01Z"
	hook.Session = &schema.SessionInfo{ID: "s1"}
	hook.Command = &schema.CommandInfo{Command: "echo hi"}
	hook.GenAI = &schema.GenAIInfo{Tool: &schema.GenAIToolInfo{Call: &schema.GenAIToolCallInfo{ID: "toolu_1"}}}

	collector := hook
	collector.Timestamp = "2026-08-21T18:00:07Z"
	collector.Message = "Tool execution observed"

	if _, err := AppendEvent(hook, Options{Path: path}); err != nil {
		t.Fatalf("first AppendEvent returned error: %v", err)
	}
	if _, err := AppendEvent(collector, Options{Path: path}); err != nil {
		t.Fatalf("second AppendEvent returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if lines := strings.Split(strings.TrimSpace(string(data)), "\n"); len(lines) != 1 {
		t.Fatalf("one Bash call was recorded %d times: %s", len(lines), string(data))
	}
}
