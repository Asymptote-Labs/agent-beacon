package asymptoteobserve

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func decodeLine(t *testing.T, line []byte) map[string]interface{} {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	var event map[string]interface{}
	if err := decoder.Decode(&event); err != nil {
		t.Fatalf("line is not JSON: %v\n%s", err, line)
	}
	return event
}

func stampMap(event map[string]interface{}) func(string) ([]byte, error) {
	return func(id string) ([]byte, error) {
		event["event"].(map[string]interface{})["id"] = id
		return json.Marshal(event)
	}
}

// A line that fits is the writer's own encoding plus its id: no decode, no reordering, nothing cut.
func TestFitEventLineKeepsAFittingLineAndStampsIt(t *testing.T) {
	event := map[string]interface{}{"event": map[string]interface{}{"action": "prompt.submitted"}, "message": "hi"}
	line, _ := json.Marshal(event)

	got, err := FitEventLine(line, DefaultMaxEventBytes, stampMap(event))
	if err != nil {
		t.Fatal(err)
	}
	if id := decodeLine(t, got)["event"].(map[string]interface{})["id"]; id != EventIDForLine(line) {
		t.Fatalf("event.id = %v, want the id derived from the unstamped line", id)
	}
	if again, _ := FitEventLine(line, DefaultMaxEventBytes, nil); !bytes.Equal(again, line) {
		t.Fatalf("an event that already has an id was re-encoded:\n%s", again)
	}
}

// Retained content is the second thing to go: the event keeps what it was about, says its content
// was held back, and does not carry an emptied gen_ai object.
func TestFitEventLineDropsRetainedContentBeforeIdentity(t *testing.T) {
	long := strings.Repeat("x", 8192)
	event := map[string]interface{}{
		"event":   map[string]interface{}{"action": "prompt.submitted"},
		"raw":     map[string]interface{}{"body": long},
		"message": long,
		"prompt":  map[string]interface{}{"text": long, "id": "prompt-1"},
		"command": map[string]interface{}{"command": "make", "output": long},
		"file":    map[string]interface{}{"path": "/repo/a.go", "diff": long},
		"gen_ai":  map[string]interface{}{"input": []interface{}{long}},
		"content": map[string]interface{}{"included": true, "bytes": 8192},
	}
	line, _ := json.Marshal(event)

	got, err := FitEventLine(line, 4096, stampMap(event))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 4096 {
		t.Fatalf("line is %d bytes, over the limit", len(got))
	}
	fitted := decodeLine(t, got)
	if fitted["field_truncated"] != true || fitted["raw"] != nil {
		t.Fatalf("field_truncated/raw = %v/%v, want true/absent", fitted["field_truncated"], fitted["raw"])
	}
	prompt := fitted["prompt"].(map[string]interface{})
	if _, ok := prompt["text"]; ok || prompt["id"] != "prompt-1" {
		t.Fatalf("prompt = %v, want the text gone and the id kept", prompt)
	}
	if fitted["command"].(map[string]interface{})["command"] != "make" {
		t.Fatalf("command lost its identity: %v", fitted["command"])
	}
	if _, ok := fitted["gen_ai"]; ok {
		t.Fatalf(`an emptied gen_ai was written: %v`, fitted["gen_ai"])
	}
	content := fitted["content"].(map[string]interface{})
	if content["included"] != false || content["truncated"] != true || content["bytes"] != json.Number("8192") {
		t.Fatalf("content marker = %v, want included=false truncated=true and the original byte count", content)
	}
	if id, _ := fitted["event"].(map[string]interface{})["id"].(string); id == "" {
		t.Fatal("a fitted event went without an id")
	}
}

// The last step keeps only identifying blocks. Losing the ordering keys there would leave exactly
// the events most worth ordering unorderable, and losing a provenance marker silently turns an
// inferred event into an unmarked one, which reads as trusted.
func TestFitEventLineMinimalEventKeepsOrderingAndProvenance(t *testing.T) {
	huge := make([]interface{}, 64)
	for i := range huge {
		huge[i] = strings.Repeat("y", 1024)
	}
	line := []byte(`{"timestamp":"2026-08-21T10:00:00.000000000Z","sequence":18446744073709551615,` +
		`"event":{"action":"approval.allowed","fidelity":"inferred"},` +
		`"harness":{"name":"cursor","collection_method":"hook"},` +
		`"gen_ai":{"tool":{"name":"edit","call":{"id":"call_1"}},"usage":{"input_tokens":3}},"extra":`)
	extra, _ := json.Marshal(huge)
	line = append(append(line, extra...), '}')

	got, err := FitEventLine(line, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	fitted := decodeLine(t, got)
	if fitted["sequence"] != json.Number("18446744073709551615") || fitted["timestamp"] != "2026-08-21T10:00:00.000000000Z" {
		t.Fatalf("ordering keys = %v/%v, want them exactly as written", fitted["sequence"], fitted["timestamp"])
	}
	if fitted["event"].(map[string]interface{})["fidelity"] != "inferred" ||
		fitted["harness"].(map[string]interface{})["collection_method"] != "hook" {
		t.Fatalf("provenance lost: event=%v harness=%v", fitted["event"], fitted["harness"])
	}
	tool := fitted["gen_ai"].(map[string]interface{})["tool"].(map[string]interface{})
	if tool["call"].(map[string]interface{})["id"] != "call_1" {
		t.Fatalf("tool call identity lost: %v", tool)
	}
	if _, ok := fitted["extra"]; ok {
		t.Fatal("a block outside the minimal set survived")
	}
}

func TestFitEventLineRefusesAnEventWhoseIdentityAloneIsTooLarge(t *testing.T) {
	line, _ := json.Marshal(map[string]interface{}{
		"event":   map[string]interface{}{"action": "tool.invoked"},
		"harness": map[string]interface{}{"name": strings.Repeat("h", 2048)},
	})
	if _, err := FitEventLine(line, 512, nil); err == nil {
		t.Fatal("an event over the limit after every cut was accepted")
	}
}
