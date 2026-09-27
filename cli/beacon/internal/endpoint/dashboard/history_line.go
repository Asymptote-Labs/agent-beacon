package dashboard

import (
	"bytes"
	"compress/flate"
	"encoding/json"
	"io"
	"sync"
)

// historyRawKeys are the keys of an event's raw payload that the trace projection reads. The raw
// payload is the runtime's own record of the event, usually as large as everything else in the
// line together, and the rest of the event already holds what Beacon kept from it, redacted and
// bounded. The store keeps these keys and drops the rest, which halves what it stores. A test fails
// if the dashboard package reads a raw key that is missing here.
var historyRawKeys = map[string]bool{
	"first_prompt":         true,
	"last_refreshed_at":    true,
	"metric_name":          true,
	"namespace_id":         true,
	"namespace_name":       true,
	"namespace_slug":       true,
	"remote_ai_title":      true,
	"remote_event_count":   true,
	"remote_message_count": true,
	"remote_title":         true,
	"shared_at":            true,
	"shared_event_count":   true,
	"shared_url":           true,
	"shared_visibility":    true,
	"sharedUrl":            true,
	"synced_at":            true,
	"visibility":           true,
}

// storedLine is the line the store keeps for an event: every field of the log line except the
// parts of raw the projection never reads. Fields this Beacon version does not know stay, so a
// later version that reads them can rebuild from the store.
func storedLine(line []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return nil, err
	}
	if raw, ok := fields["raw"]; ok {
		var values map[string]json.RawMessage
		if err := json.Unmarshal(raw, &values); err == nil {
			kept := map[string]json.RawMessage{}
			for key, value := range values {
				if historyRawKeys[key] {
					kept[key] = value
				}
			}
			if len(kept) == 0 {
				delete(fields, "raw")
			} else if encoded, err := json.Marshal(kept); err == nil {
				fields["raw"] = encoded
			}
		}
	}
	return json.Marshal(fields)
}

// historyLineDictionary primes the compressor with the shape of a Beacon event, so each stored line
// compresses on its own as well as a run of lines would. Lines are stored with sorted keys, the
// order storedLine marshals them in, and so is this.
//
// Stored lines can only be read with the dictionary they were written with, so this text must never
// change. A different dictionary is a new historyLineCodec, with a migration.
const historyLineDictionary = `{"branch":"main","command":{"command":"go test ./...","duration_ms":12,"exit_code":0,"output":""},"content":{"bytes":1174,"hash":"68924de4069ab59e961113ef4e90fb8db4215cb3f9cb611438d2ebbc38e48461","included":true,"retention":"full"},"endpoint":{"hostname":"Mac","os":"darwin"},"event":{"action":"prompt.submitted","category":"prompt","fidelity":"observed","id":"228a0964-3886-536e-ac8d-f32435cb4172","kind":"agent_runtime"},"gen_ai":{"input":{"messages":[{"parts":[{"content":"","type":"text"}],"role":"user"}]}},"harness":{"collection_method":"hook","name":"claude_code","version":"2.1.270"},"message":"claude_code.user_prompt","product":"endpoint-agent","prompt":{"text":""},"repository":"agent-beacon","schema_version":"1.0","sequence":29600,"session":{"id":"8d035bed-4d8d-4966-9a97-bf237e9b2e31","working_directory":"/Users/"},"severity":"info","timestamp":"2026-09-22T21:08:52.347000000Z","trace":{"id":"session:claude_code:8d035bed-4d8d-4966-9a97-bf237e9b2e31","parent_span_id":"","span_id":""},"user":{"name":""},"vendor":"beacon"}
{"endpoint":{"hostname":"Mac","os":"darwin"},"event":{"action":"session.activity","category":"session","fidelity":"observed","id":"c49c4cc0-413e-5e16-9a24-6466315fca40","kind":"agent_runtime"},"harness":{"collection_method":"otlp","name":"claude_code","version":"2.1.270"},"message":"claude_code.hook_execution_complete","product":"endpoint-agent","schema_version":"1.0","sequence":1438,"session":{"id":"fcf451af-d600-48c9-9bf1-649471e70bf5"},"severity":"info","timestamp":"2026-09-22T20:59:13.412000000Z","user":{"name":""},"vendor":"beacon"}
{"endpoint":{"hostname":"Mac","os":"darwin"},"event":{"action":"token.usage","category":"metric","fidelity":"observed","id":"fd994f18-70ae-561c-b31c-9dbd6ea7ad06","kind":"agent_runtime"},"gen_ai":{"token":{"type":"input"},"usage":{"cache_creation":{"input_tokens":0},"cache_read":{"input_tokens":0},"cost_usd":0.0123,"input_tokens":52,"output_tokens":0}},"harness":{"collection_method":"otlp","name":"claude_code","version":"2.1.270"},"message":"claude_code.token.usage","model":"claude-opus-5[1m]","product":"endpoint-agent","raw":{"metric_name":"claude_code.cost.usage"},"schema_version":"1.0","sequence":1807,"session":{"id":""},"severity":"info","timestamp":"2026-09-22T20:59:41.577000000Z","user":{"name":""},"vendor":"beacon"}
{"approval":{"decision":"allowed","reason":"","required":true},"branch":"main","command":{"command":""},"endpoint":{"hostname":"Mac","os":"darwin"},"event":{"action":"approval.allowed","category":"approval","fidelity":"inferred","id":"","kind":"agent_runtime"},"gen_ai":{"tool":{"call":{"arguments":{"command":"","description":""},"id":"toolu_01S5orE4VdnGrQNVfd8BvmW1"},"name":"Bash"}},"harness":{"collection_method":"hook","name":"codex_cli","version":""},"message":"Tool execution observed","product":"endpoint-agent","repository":"","schema_version":"1.0","sequence":0,"session":{"id":"","working_directory":""},"severity":"info","timestamp":"","tool":{"command":"","name":"Bash","path":""},"trace":{"id":"","parent_span_id":"","span_id":""},"user":{"name":""},"vendor":"beacon"}
{"branch":"main","endpoint":{"hostname":"Mac","os":"darwin"},"event":{"action":"command.executed","category":"command","fidelity":"observed","id":"999d2760-4edd-505e-91f9-d2fcf0ec9f72","kind":"agent_runtime"},"file":{"diff":"","language":"go","operation":"modify","path":""},"gen_ai":{"output":{"messages":[{"parts":[{"content":"","type":"text"}],"role":"assistant"}]},"tool":{"call":{"id":"call_","result":""},"name":"shell"}},"harness":{"collection_method":"otlp","name":"cursor","version":""},"mcp":{"method":{"name":"tools/call"},"server":"","tool":""},"message":"claude_code.tool_result","product":"endpoint-agent","repository":"","schema_version":"1.0","sequence":1437,"session":{"id":"999d2760-4edd-505e-91f9-d2fcf0ec9f72"},"severity":"info","timestamp":"2026-09-22T20:59:13.412000000Z","tool":{"command":"","name":"Bash"},"trace":{"id":"","parent_span_id":"","span_id":""},"user":{"name":""},"vendor":"beacon"}`

// historyLineCodec names how stored lines are encoded, recorded in the store's meta table.
const historyLineCodec = "flate-dict-1"

var historyWriters = sync.Pool{New: func() any {
	w, _ := flate.NewWriterDict(io.Discard, flate.DefaultCompression, []byte(historyLineDictionary))
	return w
}}

func compressLine(line []byte) []byte {
	var buf bytes.Buffer
	w := historyWriters.Get().(*flate.Writer)
	w.Reset(&buf)
	_, _ = w.Write(line)
	_ = w.Close()
	historyWriters.Put(w)
	return buf.Bytes()
}

func decompressLine(data []byte) ([]byte, error) {
	r := flate.NewReaderDict(bytes.NewReader(data), []byte(historyLineDictionary))
	defer r.Close()
	return io.ReadAll(r)
}
