package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestRunAgentThoughtEmitsReasoningEvent(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	thought := "The failing test writes to a read-only dir; I should use t.TempDir() instead."
	out := runHookWithInput(t, runAgentThought, map[string]interface{}{
		"hook_event_name": "afterAgentThought",
		"conversation_id": "conv-thought",
		"generation_id":   "gen-42",
		"model":           "gpt-5.5",
		"text":            thought,
		"duration_ms":     float64(5000),
		"workspace_roots": []interface{}{"/repo"},
	})
	if out["continue"] != true {
		t.Fatalf("cursor agent-thought response = %#v, want continue=true", out)
	}

	event := lastEndpointEvent(t, logPath)
	eventInfo := event["event"].(map[string]interface{})
	if eventInfo["action"] != "agent.reasoning" || eventInfo["category"] != "session" {
		t.Fatalf("event = %#v, want agent.reasoning session", eventInfo)
	}
	if session := event["session"].(map[string]interface{}); session["id"] != "conv-thought" {
		t.Fatalf("session = %#v, want id conv-thought", session)
	}
	if event["model"] != "gpt-5.5" {
		t.Fatalf("model = %v, want gpt-5.5", event["model"])
	}

	genAI := event["gen_ai"].(map[string]interface{})
	messages := genAI["output"].(map[string]interface{})["messages"].([]interface{})
	if len(messages) != 1 {
		t.Fatalf("output messages = %#v, want one assistant message", messages)
	}
	message := messages[0].(map[string]interface{})
	if message["role"] != "assistant" {
		t.Fatalf("message role = %v, want assistant", message["role"])
	}
	part := message["parts"].([]interface{})[0].(map[string]interface{})
	if part["type"] != "reasoning" || part["content"] != thought {
		t.Fatalf("reasoning part = %#v", part)
	}

	sum := sha256.Sum256([]byte(thought))
	content := event["content"].(map[string]interface{})
	if content["retention"] != "full" || content["included"] != true {
		t.Fatalf("content marker = %#v", content)
	}
	if content["hash"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("content hash = %v, want %s", content["hash"], hex.EncodeToString(sum[:]))
	}
	if content["bytes"] != float64(len(thought)) {
		t.Fatalf("content bytes = %v, want %d", content["bytes"], len(thought))
	}
	if _, ok := content["truncated"]; ok {
		t.Fatalf("short thought should not be marked truncated: %#v", content)
	}

	raw := event["raw"].(map[string]interface{})["cursor"].(map[string]interface{})
	if raw["duration_ms"] != float64(5000) || raw["generation_id"] != "gen-42" {
		t.Fatalf("raw cursor metadata = %#v", raw)
	}
	if _, ok := raw["text"]; ok {
		t.Fatalf("raw metadata must not duplicate reasoning text: %#v", raw)
	}
}

func TestRunAgentThoughtSkipsEventWithoutText(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runAgentThought, map[string]interface{}{
		"hook_event_name": "afterAgentThought",
		"conversation_id": "conv-empty",
	})
	if out["continue"] != true {
		t.Fatalf("cursor agent-thought response = %#v, want continue=true", out)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("no endpoint event should be written without text: %v", err)
	}
}

func TestRunAgentThoughtCorrectsCursorPayloadWithDefaultPlatform(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "claude"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runAgentThought, map[string]interface{}{
		"hook_event_name": "afterAgentThought",
		"conversation_id": "conv-override",
		"text":            "reasoning through the plan",
	})
	if out["continue"] != true {
		t.Fatalf("response after cursor payload override = %#v, want continue=true", out)
	}
	event := lastEndpointEvent(t, logPath)
	if harness := event["harness"].(map[string]interface{}); harness["name"] != "cursor" {
		t.Fatalf("harness = %#v, want cursor", harness)
	}
}

func TestRunAgentThoughtMarksLongAndSecretBearingText(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	long := "api_key=sk-abcdefghijklmnopqrstuvwxyz123456 " + strings.Repeat("reasoning ", asymptoteobserve.DefaultStringLimit/8)
	runHookWithInput(t, runAgentThought, map[string]interface{}{
		"hook_event_name": "afterAgentThought",
		"conversation_id": "conv-long",
		"text":            long,
	})

	event := lastEndpointEvent(t, logPath)
	content := event["content"].(map[string]interface{})
	if content["truncated"] != true {
		t.Fatalf("content marker should flag truncation: %#v", content)
	}
	if content["redacted"] != true {
		t.Fatalf("content marker should flag redaction: %#v", content)
	}
	if content["bytes"] != float64(len(long)) {
		t.Fatalf("content bytes = %v, want original length %d", content["bytes"], len(long))
	}

	part := event["gen_ai"].(map[string]interface{})["output"].(map[string]interface{})["messages"].([]interface{})[0].(map[string]interface{})["parts"].([]interface{})[0].(map[string]interface{})
	stored := part["content"].(string)
	if strings.Contains(stored, "sk-abcdefghijklmnopqrstuvwxyz123456") {
		t.Fatalf("stored reasoning text was not redacted")
	}
	if len(stored) > asymptoteobserve.DefaultStringLimit+64 {
		t.Fatalf("stored reasoning text was not truncated: %d bytes", len(stored))
	}
}
