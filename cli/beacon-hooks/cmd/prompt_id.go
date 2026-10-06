package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// applyPromptID records which user prompt a hook event belongs to, as prompt.id.
//
// The runtime's own identifier is read from the payload's top level through the shared alias list,
// so Cursor's generation_id, Codex's and Muse Code's turn_id and Kimi Code's numbered turn_id all
// land in the one field the collector fills from Claude Code's and Gemini CLI's OTLP attributes.
// The envelope only, for the reason tool call ids are read from it only: tool arguments are
// model-authored, and an argument that happened to be named turn_id is not the runtime naming its
// turn.
//
// Claude Code's hook payloads carry no prompt id, but they carry the transcript path, and Claude
// Code writes the prompt's id into that transcript; see claudeTranscriptPromptID.
//
// Every event a payload produces gets the id -- a tool call and the file edits it caused belong to
// the same prompt -- and an id a mapper already set is left alone.
func applyPromptID(fields, input map[string]interface{}) {
	if fields == nil || input == nil {
		return
	}
	if prompt, ok := fields["prompt"].(map[string]interface{}); ok {
		if id, _ := prompt["id"].(string); strings.TrimSpace(id) != "" {
			return
		}
	}
	id := asymptoteobserve.PromptIDFrom(input)
	if id == "" && platformFlag == "claude" && claudeTurnScopedHook(input) {
		id = claudeTranscriptPromptID(getFirstStr(input, "transcript_path", "transcriptPath"))
	}
	if id == "" {
		return
	}
	fields["prompt"] = mergeNested(fields["prompt"], map[string]interface{}{"id": id})
}

// claudeTurnScopedHook reports whether a Claude Code hook fires inside a prompt's turn, after the
// prompt has been written to the transcript.
//
// UserPromptSubmit is excluded deliberately. It runs before Claude Code records the prompt it is
// about, so the newest id in the transcript at that moment is the previous prompt's, and stamping
// it would put the new prompt in the old turn. That event's id comes from Claude Code's own OTLP
// user_prompt event, which carries it. Session start and end are not part of any one turn.
func claudeTurnScopedHook(input map[string]interface{}) bool {
	switch getFirstStr(input, "hook_event_name", "hookEventName") {
	case "PreToolUse", "PostToolUse", "PostToolUseFailure", "PermissionRequest", "PermissionDenied",
		"Stop", "StopFailure", "SubagentStart", "SubagentStop", "PreCompact", "PostCompact":
		return true
	}
	return false
}

// claudeTranscriptTailBytes bounds how much of a transcript one hook reads. A hook runs on every
// tool call, and a long session's transcript is many megabytes; the prompt and every tool result
// since carry the id, so the newest one is almost always in the last chunk read.
const (
	claudeTranscriptTailBytes  = 1 << 20
	claudeTranscriptChunkBytes = 64 << 10
)

// claudeTranscriptPromptID returns the newest promptId in a Claude Code session transcript.
//
// Claude Code stamps promptId on the prompt's entry and on every tool-result entry that follows
// it, and it is the same UUID its OTLP events carry as prompt.id, so a hook event reading it here
// joins the OTLP events of the same turn on one value. The file is read backwards a chunk at a
// time and the read stops at the first entry carrying one, so the usual cost is one small read
// however long the session is. Only a regular .jsonl file is read, and any failure returns
// nothing: a missing id costs one field, while a hook that failed or stalled would cost the event.
func claudeTranscriptPromptID(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || !strings.EqualFold(filepath.Ext(path), ".jsonl") {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	size := info.Size()
	window := size
	if window > claudeTranscriptTailBytes {
		window = claudeTranscriptTailBytes
	}
	// tail holds the last `read` bytes of the file. It grows a chunk at a time from the front, so
	// the common case -- an id within the last chunk -- reads and allocates one chunk.
	var tail []byte
	read := int64(0)
	for read < window {
		n := window - read
		if n > claudeTranscriptChunkBytes {
			n = claudeTranscriptChunkBytes
		}
		chunk := make([]byte, n, n+int64(len(tail)))
		if _, err := file.ReadAt(chunk, size-read-n); err != nil && err != io.EOF {
			return ""
		}
		tail = append(chunk, tail...)
		read += n
		if id := newestPromptID(tail, size > read); id != "" {
			return id
		}
	}
	return ""
}

// newestPromptID scans transcript lines from the end for the first entry carrying a promptId,
// without splitting the buffer. When the buffer starts mid-file its first line is a fragment and
// is never parsed.
//
// The marker cannot match inside a string value: JSON escapes a quote inside a string, so quoted
// text containing promptId appears as \"promptId\" and never as the bare key.
func newestPromptID(tail []byte, partialFirstLine bool) string {
	marker := []byte(`"promptId"`)
	end := len(tail)
	for end > 0 {
		at := bytes.LastIndex(tail[:end], marker)
		if at < 0 {
			return ""
		}
		lineStart := bytes.LastIndexByte(tail[:at], '\n') + 1
		if lineStart == 0 && partialFirstLine {
			return ""
		}
		lineEnd := len(tail)
		if i := bytes.IndexByte(tail[at:], '\n'); i >= 0 {
			lineEnd = at + i
		}
		var entry struct {
			PromptID string `json:"promptId"`
		}
		if json.Unmarshal(bytes.TrimSpace(tail[lineStart:lineEnd]), &entry) == nil {
			if id := strings.TrimSpace(entry.PromptID); id != "" {
				return id
			}
		}
		end = lineStart
	}
	return ""
}
