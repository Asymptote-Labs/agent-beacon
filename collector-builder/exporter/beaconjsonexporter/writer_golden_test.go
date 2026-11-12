package beaconjsonexporter

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldenEvent builds a fixture event from JSON, with every machine-dependent field fixed, so the
// written line is the same on every host.
func goldenEvent(t *testing.T, harness string, fields map[string]interface{}) beaconEvent {
	t.Helper()
	event := map[string]interface{}{
		"timestamp":      "2026-11-12T10:00:00.000000000Z",
		"sequence":       7,
		"vendor":         "beacon",
		"product":        "endpoint-agent",
		"schema_version": "1.0",
		"event":          map[string]interface{}{"kind": "agent_runtime", "action": "tool.completed", "category": "tool"},
		"severity":       "info",
		"endpoint":       map[string]interface{}{"hostname": "golden-host", "os": "linux"},
		"user":           map[string]interface{}{"name": "golden-user", "uid": "1000"},
		"harness":        map[string]interface{}{"name": harness, "collection_method": "otlp"},
		"session":        map[string]interface{}{"id": "golden-session"},
	}
	for key, value := range fields {
		event[key] = value
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var out beaconEvent
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// everySanitizedField touches every field the writer sanitizes, with secrets and strings past both
// the raw-attribute and the prompt-text limits.
func everySanitizedField() map[string]interface{} {
	secret := "Authorization: Bearer sk-golden-0123456789abcdef0123456789abcdef"
	long := strings.Repeat("L", 20*1024)
	return map[string]interface{}{
		"message":    "ran with " + secret,
		"tool":       map[string]interface{}{"name": "bash", "command": "curl -H '" + secret + "' " + long, "path": "/repo/" + long},
		"command":    map[string]interface{}{"command": "export API_KEY=sk-golden-abcdef0123456789", "output": secret + "\n" + long},
		"file":       map[string]interface{}{"path": "/repo/main.go", "diff": "+password=hunter2hunter2\n" + long},
		"approval":   map[string]interface{}{"decision": "allow", "reason": "token=sk-golden-approval-0123456789"},
		"policy":     map[string]interface{}{"decision": "deny", "reason": "api_key=sk-golden-policy-0123456789"},
		"prompt":     map[string]interface{}{"text": secret + " " + long},
		"run":        map[string]interface{}{"provider": "github_actions", "branch": "feature/" + secret, "workflow": long},
		"mcp":        map[string]interface{}{"server": "notes", "tool": "search " + secret},
		"model":      "model-" + long,
		"repository": "github.com/acme/" + long,
		"branch":     "branch-" + secret,
		"gen_ai": map[string]interface{}{
			"input":  map[string]interface{}{"messages": []interface{}{map[string]interface{}{"role": "user", "content": secret + long}}},
			"output": map[string]interface{}{"messages": []interface{}{map[string]interface{}{"role": "assistant", "content": long}}},
		},
		"raw": map[string]interface{}{"attributes": map[string]interface{}{"http.request.header.authorization": secret, "note": long}},
	}
}

func goldenWriterLines(t *testing.T) []byte {
	t.Helper()
	browserAnswer := strings.Repeat("A", 10*1024)
	cases := []struct {
		name     string
		event    beaconEvent
		redact   bool
		maxBytes int
	}{
		{"every sanitized field", goldenEvent(t, "cursor", everySanitizedField()), true, defaultMaxEventBytes},
		{"redaction off", goldenEvent(t, "cursor", everySanitizedField()), false, defaultMaxEventBytes},
		{"browser answer past the raw limit", goldenEvent(t, "claude_web", map[string]interface{}{
			"message": "assistant answered",
			"gen_ai": map[string]interface{}{
				"output": map[string]interface{}{"messages": []interface{}{map[string]interface{}{"role": "assistant", "content": browserAnswer}}},
			},
			"raw": map[string]interface{}{"attributes": map[string]interface{}{
				"gen_ai.output.messages": browserAnswer,
				"gen_ai.system":          "anthropic",
			}},
		}), true, defaultMaxEventBytes},
		{"coding agent answer past the raw limit", goldenEvent(t, "codex", map[string]interface{}{
			"gen_ai": map[string]interface{}{
				"output": map[string]interface{}{"messages": []interface{}{map[string]interface{}{"role": "assistant", "content": browserAnswer}}},
			},
			"raw": map[string]interface{}{"attributes": map[string]interface{}{"gen_ai.output.messages": browserAnswer}},
		}), true, defaultMaxEventBytes},
		{"over the event limit", goldenEvent(t, "cursor", everySanitizedField()), true, 16 * 1024},
	}
	var out bytes.Buffer
	for _, tc := range cases {
		path := filepath.Join(t.TempDir(), "runtime.jsonl")
		writer := jsonlWriter{
			path:           path,
			maxEventBytes:  tc.maxBytes,
			rotateBytes:    defaultRotateBytes,
			rotateArchives: defaultRotateArchives,
			redactSecrets:  tc.redact,
		}
		if err := writer.append(tc.event); err != nil {
			t.Fatalf("%s: append: %v", tc.name, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		out.Write(data)
	}
	return out.Bytes()
}

// The exporter writes exactly what it wrote before it sanitized through asymptoteobserve: secret
// redaction on and off, the 16 KiB browser-answer limit and its raw-attribute dedupe, a coding
// agent's answer at the raw-attribute limit, and the size fallback. testdata/writer_golden.jsonl was
// captured from the exporter's own sanitizer and is never regenerated by a test run.
func TestJSONLWriterOutputIsPinned(t *testing.T) {
	got := goldenWriterLines(t)
	want, err := os.ReadFile(filepath.Join("testdata", "writer_golden.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// A Windows checkout turns the golden file's line endings into CRLF; the exporter writes LF.
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(got, want) {
		gotLines, wantLines := bytes.Split(got, []byte("\n")), bytes.Split(want, []byte("\n"))
		for i := range wantLines {
			if i >= len(gotLines) || !bytes.Equal(gotLines[i], wantLines[i]) {
				t.Fatalf("line %d changed:\ngot:  %.600s\nwant: %.600s", i+1, lineAt(gotLines, i), wantLines[i])
			}
		}
		t.Fatalf("output has %d lines, want %d", len(gotLines), len(wantLines))
	}
}

func lineAt(lines [][]byte, i int) []byte {
	if i < len(lines) {
		return lines[i]
	}
	return nil
}
