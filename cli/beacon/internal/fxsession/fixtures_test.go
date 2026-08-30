package fxsession

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures in this file are raw JSON text rather than structs marshalled by Beacon's own types.
//
// That is deliberate and it is the whole point of the file. Beacon does not control this format:
// fx encodes its session records by hand in Zig, field by field, with no shared schema to generate
// from. A fixture built by marshalling Beacon's structs would prove only that Beacon can read what
// Beacon wrote, and would keep passing after fx renamed a field. These lines are transcribed from
// fx's writers (writeHistoryTurn, writeExecutionMemory, writePersistedToolResult,
// writeCommittedFilePresentation, writeToolCall, writeUserTurn, writeTurnSummary in
// src/core/session/session_codec.zig; encodeFrame in session_event.zig; encodeManifest in
// session_projection.zig), so a test that fails here is telling you fx moved.

const (
	testGeneration = "1f4b2c8e6d3a5a7c9b613f0d5e8c2a14"
	testSessionID  = "1770000000000-1770000000000000000-a1b2c3d4e5f60718"
)

// frame wraps a payload in fx's envelope. Envelope fields are written in fx's own order so the
// fixture reads like a line pulled off a real log.
func frame(seq int, timestampMS int64, kind, payload string) string {
	return fmt.Sprintf(
		`{"schema_version":1,"log_generation":%q,"seq":%d,"event_id":"%032x","timestamp_ms":%d,"kind":%q,"payload":%s}`,
		testGeneration, seq, seq, timestampMS, kind, payload,
	)
}

func sessionStartedLine(seq int, workspace string) string {
	payload := fmt.Sprintf(
		`{"id":%q,"created_at_ms":1770000000000,"origin_workspace_root":%q,"workspace_root":%q,`+
			`"conversation_language":"en","preferences":{"provider":"gateway","model":"anthropic/claude-opus-4","effort":"medium","fast_mode":false},`+
			`"usage":null}`,
		testSessionID, workspace, workspace,
	)
	return frame(seq, 1770000000000, KindSessionStarted, payload)
}

// assistantTurnLine is a full turn: a prompt, a read, an edit that committed a diff, and a command
// that exited non-zero. It carries every sub-record the mapper reads, so one fixture exercises the
// decoder end to end rather than a different fixture per field.
func assistantTurnLine(seq int) string {
	payload := `{"conversation_language":"en","total_input_tokens":4200,"total_output_tokens":880,"work_id":"work-7","turn":{` +
		`"kind":"assistant",` +
		`"user":{"text":"add a health endpoint and run the tests","images":[]},` +
		`"assistant":"Added the endpoint; the suite fails on an unrelated case.",` +
		`"execution":{"schema_version":5,"tool_steps":[` +
		`{"assistant":"Reading the server first.","tool_calls":[` +
		`{"id":"call_read_1","name":"read_file","arguments_json":"{\"path\":\"src/server.ts\"}","provider_result":null}` +
		`],"tool_results":[` +
		`{"tool_call_id":"call_read_1","tool_name":"read_file","status":"success","output":"export function serve() {}",` +
		`"output_handle":null,"preview":null,"output_bytes":26,"stored_output_bytes":26,"truncated":false,` +
		`"provider_native":false,"created_at_ms":1770000001000,"permission_feedback":[],` +
		`"committed_file_presentation":null,"command_output_replay":null,"command_process_presentation":null,` +
		`"terminal_action_presentation":null}` +
		`]},` +
		`{"assistant":null,"tool_calls":[` +
		`{"id":"call_edit_1","name":"edit_file","arguments_json":"{\"path\":\"src/server.ts\",\"old\":\"serve\",\"new\":\"serveHealth\"}","provider_result":null},` +
		`{"id":"call_cmd_1","name":"run_command","arguments_json":"{\"command\":\"npm test\"}","provider_result":null}` +
		`],"tool_results":[` +
		`{"tool_call_id":"call_edit_1","tool_name":"edit_file","status":"success","output":"edited src/server.ts",` +
		`"output_handle":null,"preview":null,"output_bytes":19,"stored_output_bytes":19,"truncated":false,` +
		`"provider_native":false,"created_at_ms":1770000002000,"permission_feedback":[],` +
		`"committed_file_presentation":{"path":"src/server.ts","kind":"edited","lines":[` +
		`{"kind":"context","old_line":1,"new_line":1,"text":"import http from \"http\";"},` +
		`{"kind":"deletion","old_line":2,"new_line":null,"text":"export function serve() {}"},` +
		`{"kind":"addition","old_line":null,"new_line":2,"text":"export function serveHealth() {}"}` +
		`],"additions":1,"deletions":1,"truncated":false,"previous_content":"old","after_content":"new",` +
		`"lifecycle_id":{"turn_id":7,"call_id":"call_edit_1"}},` +
		`"command_output_replay":null,"command_process_presentation":null,"terminal_action_presentation":null},` +
		`{"tool_call_id":"call_cmd_1","tool_name":"run_command","status":"failure","output":"1 test failed",` +
		`"output_handle":"handle-1","preview":"1 test failed","output_bytes":4096,"stored_output_bytes":13,"truncated":true,` +
		`"provider_native":false,"created_at_ms":1770000003000,"permission_feedback":["approved by rule"],` +
		`"committed_file_presentation":null,"command_output_replay":{"kind":"available","handle":"handle-1","framed_bytes":4096},` +
		`"command_process_presentation":{"kind":"exit_code","value":1},"terminal_action_presentation":null}` +
		`]}` +
		`],"files":[` +
		`{"path":"src/server.ts","new_path":null,"tool_call_id":"call_read_1","tool_name":"read_file","action":"read","status":"success","model_view_covers_full_file":true,"stale":false},` +
		`{"path":"src/server.ts","new_path":null,"tool_call_id":"call_edit_1","tool_name":"edit_file","action":"edit","status":"success","model_view_covers_full_file":true,"stale":false}` +
		`],"turn_summary":{"started_at_ms":1770000000500,"completed_at_ms":1770000004000,"thinking_duration_ms":900,"turn_duration_ms":3500,` +
		`"token_progress":{"input_tokens":1200,"output_tokens":340,"input_exact":true,"output_exact":false}}}}}`
	return frame(seq, 1770000004000, KindHistoryTurnCommitted, payload)
}

func usageCheckpointLine(seq int, inputTokens, outputTokens int64, cost float64) string {
	payload := fmt.Sprintf(
		`{"usage":{"billing":"complete","api_duration_complete":true,"wall_duration_complete":true,"code_complete":true,`+
			`"next_sequence":3,"settled_through_sequence":2,"api_duration_ms":5100,"wall_duration_ms":7200,"total_cost":%v,`+
			`"input_tokens":%d,"output_tokens":%d,"cache_read_tokens":900,"cache_write_tokens":120,`+
			`"billable_web_search_calls":0,"lines_added":12,"lines_removed":3,"models":[`+
			`{"model":"anthropic/claude-opus-4","total_cost":%v,"input_tokens":%d,"output_tokens":%d,`+
			`"cache_read_tokens":900,"cache_write_tokens":120,"reasoning_tokens":64}]}}`,
		cost, inputTokens, outputTokens, cost, inputTokens, outputTokens,
	)
	return frame(seq, 1770000005000, KindUsageCheckpointed, payload)
}

func manifestJSON(generation string, eventLogBytes int64, lastSeq int, workspace string) string {
	return fmt.Sprintf(
		`{"schema_version":3,"storage_format":"event_log_v1","id":%q,"authority_id":"%032x","log_generation":%q,`+
			`"created_at_ms":1770000000000,"updated_at_ms":1770000005000,"origin_workspace_root":%q,"workspace_root":%q,`+
			`"conversation_language":"en","history_len":1,"total_input_tokens":4200,"total_output_tokens":880,`+
			`"last_event_seq":%d,"event_log_bytes":%d,"event_log_stat_fingerprint":"%032x",`+
			`"generation_base_seq":0,"generation_base_bytes":0,"checkpoint_seq":null,"checkpoint_sha256":null,`+
			`"preferences":{"provider":"gateway","model":"anthropic/claude-opus-4","effort":"medium","fast_mode":false}}`,
		testSessionID, 1, generation, workspace, workspace, lastSeq, eventLogBytes, 2,
	)
}

// writeSession lays out one fx session directory: the lines joined with trailing newlines, plus a
// manifest whose watermark covers exactly the bytes written unless the caller overrides it.
func writeSession(t *testing.T, root, id string, lines []string, manifest string) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create session dir: %v", err)
	}
	body := ""
	if len(lines) > 0 {
		body = strings.Join(lines, "\n") + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, EventsFileName), []byte(body), 0o600); err != nil {
		t.Fatalf("write events log: %v", err)
	}
	if manifest != "" {
		if err := os.WriteFile(filepath.Join(dir, ManifestFileName), []byte(manifest), 0o600); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
	}
	return dir
}

// logBytes is the on-disk length of a set of lines, which is what fx's committed watermark holds.
func logBytes(lines []string) int64 {
	total := 0
	for _, line := range lines {
		total += len(line) + 1
	}
	return int64(total)
}
