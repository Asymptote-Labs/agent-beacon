package asymptoteobserve

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestRetainedContentDescribesTheOriginalText(t *testing.T) {
	text := "the quick brown fox"
	sum := sha256.Sum256([]byte(text))

	info := RetainedContent(text, DefaultStringLimit)
	if info == nil {
		t.Fatal("RetainedContent returned nil for non-empty text")
	}
	if info.Retention != ContentRetentionFull || !info.Included {
		t.Fatalf("marker = %#v, want full/included", info)
	}
	if info.Hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash = %q, want %q", info.Hash, hex.EncodeToString(sum[:]))
	}
	if info.Bytes != len(text) {
		t.Fatalf("bytes = %d, want %d", info.Bytes, len(text))
	}
	if info.Truncated || info.Redacted {
		t.Fatalf("short clean text should be neither truncated nor redacted: %#v", info)
	}
}

func TestRetainedContentEmptyTextGetsNoMarker(t *testing.T) {
	// An event that retained nothing must not claim it retained the empty string.
	if info := RetainedContent("", DefaultStringLimit); info != nil {
		t.Fatalf("RetainedContent(\"\") = %#v, want nil", info)
	}
}

func TestRetainedContentTruncationFollowsTheWritersLimit(t *testing.T) {
	// The same text is truncated by one writer and not the other, so the marker
	// has to answer against the limit it is given rather than a constant.
	text := strings.Repeat("a", DefaultRawStringLimit+1)

	atRawLimit := RetainedContent(text, DefaultRawStringLimit)
	if !atRawLimit.Truncated {
		t.Fatalf("text over the raw limit should be marked truncated: %#v", atRawLimit)
	}
	atStringLimit := RetainedContent(text, DefaultStringLimit)
	if atStringLimit.Truncated {
		t.Fatalf("text under the string limit should not be marked truncated: %#v", atStringLimit)
	}
	if atRawLimit.Bytes != len(text) || atStringLimit.Bytes != len(text) {
		t.Fatalf("byte count must describe the original text regardless of limit: %#v %#v", atRawLimit, atStringLimit)
	}
	if RetainedContent(text, 0).Truncated {
		t.Fatal("limit 0 means no truncation claim")
	}
}

func TestRetainedContentFlagsRedaction(t *testing.T) {
	info := RetainedContent("deploy with token=hunter2hunter2", DefaultStringLimit)
	if !info.Redacted {
		t.Fatalf("text carrying a secret should be marked redacted: %#v", info)
	}
}

func TestRetainedContentFieldsMatchesTheTypedMarker(t *testing.T) {
	// The map form is what the hook adapter writes and the typed form is what the
	// collector writes. They are the same object or a reader has to know which
	// writer produced an event before it can read the marker.
	text := strings.Repeat("b", DefaultStringLimit+1) + " password=abcdefghij"
	fields := RetainedContentFields(text, DefaultStringLimit)

	data, err := json.Marshal(RetainedContent(text, DefaultStringLimit))
	if err != nil {
		t.Fatalf("marshal typed marker: %v", err)
	}
	var want map[string]interface{}
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatalf("decode typed marker: %v", err)
	}
	gotJSON, _ := json.Marshal(fields)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("map marker = %s, want %s", gotJSON, wantJSON)
	}
	if fields["truncated"] != true || fields["redacted"] != true {
		t.Fatalf("map marker lost its flags: %#v", fields)
	}
	if RetainedContentFields("", DefaultStringLimit) != nil {
		t.Fatal("empty text should yield no map marker")
	}
}

func TestGenAIMessagesUseTheSemconvShape(t *testing.T) {
	for _, tc := range []struct {
		name     string
		messages []interface{}
		wantRole string
		wantType string
	}{
		{"output", TextOutputMessages("said"), RoleAssistant, GenAIPartTypeText},
		{"input", TextInputMessages("asked"), RoleUser, GenAIPartTypeText},
		{"reasoning", ReasoningOutputMessages("thought"), RoleAssistant, GenAIPartTypeReasoning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.messages) != 1 {
				t.Fatalf("messages = %#v, want one message", tc.messages)
			}
			message := tc.messages[0].(map[string]interface{})
			if message["role"] != tc.wantRole {
				t.Fatalf("role = %v, want %s", message["role"], tc.wantRole)
			}
			parts := message["parts"].([]interface{})
			if len(parts) != 1 {
				t.Fatalf("parts = %#v, want one part", parts)
			}
			part := parts[0].(map[string]interface{})
			if part["type"] != tc.wantType {
				t.Fatalf("part type = %v, want %s", part["type"], tc.wantType)
			}
			if part["content"] == "" {
				t.Fatalf("part carries no content: %#v", part)
			}
		})
	}
}

func TestSanitizeEventCleansRetainedDiffAndCommandOutput(t *testing.T) {
	// Both fields were written to the log by the hook adapter long before this
	// struct declared them, which meant SanitizeEvent never saw them: a
	// credential printed by a command reached the typed path unredacted.
	longDiff := strings.Repeat("c", DefaultStringLimit+100)
	event := Event{
		Vendor:        Vendor,
		Product:       Product,
		SchemaVersion: SchemaVersion,
		Event:         EventInfo{Kind: "agent_runtime", Action: "command.executed"},
		Severity:      SeverityInfo,
		Endpoint:      EndpointInfo{OS: "linux"},
		Harness:       HarnessInfo{Name: "claude_code"},
		Command:       &CommandInfo{Command: "printenv", Output: "GITHUB_TOKEN=ghp_wJalrXUtnFEMI"},
		File:          &FileInfo{Path: "/tmp/x.go", Diff: longDiff},
	}

	out := SanitizeEvent(event, DefaultMaxEventBytes)
	if strings.Contains(out.Command.Output, "ghp_wJalrXUtnFEMI") {
		t.Fatalf("command output kept its secret: %q", out.Command.Output)
	}
	if len(out.File.Diff) > DefaultStringLimit {
		t.Fatalf("diff length = %d, want <= %d", len(out.File.Diff), DefaultStringLimit)
	}
}

func TestRetainedContentFieldsSurviveTheTypedRoundTrip(t *testing.T) {
	// The regression these two fields exist to fix: the hook adapter assembles
	// events as maps, so file.diff and command.output reached the log while every
	// reader that decodes into Event -- the dashboard, beacon scan, the rules
	// engine -- dropped them on parse.
	line := []byte(`{"vendor":"beacon","product":"endpoint-agent","schema_version":"1.0",` +
		`"event":{"kind":"agent_runtime","action":"file.modified"},"severity":"info",` +
		`"endpoint":{"os":"darwin"},"harness":{"name":"cursor"},` +
		`"file":{"path":"/repo/main.go","diff":"@@ -1 +1 @@\n-a\n+b\n"},` +
		`"command":{"command":"go test","output":"ok\n"}}`)

	var event Event
	if err := json.Unmarshal(line, &event); err != nil {
		t.Fatalf("decode endpoint line: %v", err)
	}
	if event.File == nil || event.File.Diff == "" {
		t.Fatalf("file.diff did not survive decoding: %#v", event.File)
	}
	if event.Command == nil || event.Command.Output != "ok\n" {
		t.Fatalf("command.output did not survive decoding: %#v", event.Command)
	}
}
