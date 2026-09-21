package dshsession

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/klauspost/compress/zstd"
)

func TestStoreReadsPlainAndMapsDeepSeekSession(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "s1")
	writeSession(t, sessionDir, SessionFileJSON,
		record("session", map[string]interface{}{"id": "s1", "cwd": "/repo"}),
		record("request/header", map[string]interface{}{"header": map[string]interface{}{"config": map[string]interface{}{"model": "deepseek-chat"}}}),
		record("user/message", map[string]interface{}{"message": map[string]interface{}{"content": "fix tests"}}),
		record("assistant/message", map[string]interface{}{"message": map[string]interface{}{
			"source": map[string]interface{}{"model": "deepseek-chat"},
			"content": []interface{}{
				map[string]interface{}{"type": "reasoning", "text": "Need to inspect failure."},
				map[string]interface{}{"type": "text", "text": "I will run tests."},
			},
			"usage": map[string]interface{}{"inputTokens": 11, "outputTokens": 7, "cacheReadTokens": 3, "cacheWriteTokens": 2, "reasoningTokens": 5},
		}}),
		record("tool/call", map[string]interface{}{"callId": "call_1", "name": "bash", "arguments": map[string]interface{}{"command": "go test ./..."}}),
		record("tool/result", map[string]interface{}{"callId": "call_1", "toolName": "bash", "content": "FAIL\n[exit code: 1]"}),
		record("tool/call", map[string]interface{}{"callId": "call_2", "name": "write", "arguments": map[string]interface{}{"file_path": "/repo/new.go", "content": "package main\n"}}),
		record("tool/result", map[string]interface{}{"callId": "call_2", "toolName": "write", "content": "permission denied", "isError": true}),
	)

	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].ID != "s1" || refs[0].Meta.CWD != "/repo" {
		t.Fatalf("refs = %+v", refs)
	}
	records, stats, err := store.Read(refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if stats.Decoded != 8 {
		t.Fatalf("decoded = %d, want 8", stats.Decoded)
	}
	mapped := MapSession(refs[0], records, MapOptions{})
	actions := actions(mapped)
	for _, want := range []string{"session.started", "prompt.submitted", "agent.reasoning", "agent.message", "token.usage", "command.executed", "tool.failed"} {
		if !contains(actions, want) {
			t.Fatalf("actions = %v, missing %s", actions, want)
		}
	}
	command := findAction(t, mapped, "command.executed")
	if command.Command == nil || command.Command.Command != "go test ./..." {
		t.Fatalf("command event = %+v", command)
	}
	if command.Command.ExitCode == nil || *command.Command.ExitCode != 1 {
		t.Fatalf("exit code = %+v, want 1", command.Command)
	}
	usage := findAction(t, mapped, "token.usage")
	if usage.GenAI == nil || usage.GenAI.Usage == nil || usage.GenAI.Usage.InputTokens == nil || *usage.GenAI.Usage.InputTokens != 11 {
		t.Fatalf("usage = %+v", usage.GenAI)
	}
	failed := findAction(t, mapped, "tool.failed")
	if failed.File != nil && failed.File.Diff != "" {
		t.Fatalf("failed write carried a diff: %+v", failed.File)
	}
}

func TestStoreReadsCompleteZstdFramesAndDefersPartialTail(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "z1")
	full := zstdFrame(t, []byte(record("session", map[string]interface{}{"id": "z1", "cwd": "/repo"})+"\n"))
	partial := zstdFrame(t, []byte(record("user/message", map[string]interface{}{"message": map[string]interface{}{"content": "later"}})+"\n"))
	if len(partial) < 8 {
		t.Fatal("zstd frame unexpectedly short")
	}
	writeBytes(t, filepath.Join(sessionDir, SessionFileZstd), append(full, partial[:len(partial)/2]...))
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	records, stats, err := store.Read(refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Kind != "session" {
		t.Fatalf("records = %+v", records)
	}
	if !stats.PartialFrame || !stats.PartialTail {
		t.Fatalf("stats = %+v, want partial frame/tail", stats)
	}
}

func TestCollectPrintDoesNotAdvanceState(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "s-print")
	writeSession(t, sessionDir, SessionFileJSON,
		record("session", map[string]interface{}{"id": "s-print", "cwd": "/repo"}),
		record("user/message", map[string]interface{}{"message": map[string]interface{}{"content": "hello"}}),
	)
	statePath := filepath.Join(t.TempDir(), "state.json")
	var out bytes.Buffer
	summary, err := CollectOnce(CollectOptions{DSHHome: root, Print: true, Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if summary.EventsEmitted != 2 || !strings.Contains(out.String(), `"prompt.submitted"`) {
		t.Fatalf("summary=%+v out=%s", summary, out.String())
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("--print wrote state file: %v", err)
	}
}

func record(kind string, data map[string]interface{}) string {
	line, _ := json.Marshal(map[string]interface{}{"type": kind, "time": "2026-09-21T10:00:00Z", "data": data})
	return string(line)
}

func writeSession(t *testing.T, dir, name string, lines ...string) {
	t.Helper()
	writeBytes(t, filepath.Join(dir, name), []byte(strings.Join(lines, "\n")+"\n"))
}

func writeBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func zstdFrame(t *testing.T, data []byte) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	return enc.EncodeAll(data, nil)
}

func actions(mapped []MappedEvent) []string {
	out := make([]string, 0, len(mapped))
	for _, item := range mapped {
		out = append(out, item.Event.Event.Action)
	}
	return out
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func findAction(t *testing.T, mapped []MappedEvent, action string) schema.Event {
	t.Helper()
	for _, item := range mapped {
		if item.Event.Event.Action == action {
			return item.Event
		}
	}
	t.Fatalf("missing action %s in %v", action, actions(mapped))
	return schema.Event{}
}
