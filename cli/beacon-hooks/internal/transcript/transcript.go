// Package transcript reads the recent tail of a Claude Code session transcript
// (the JSONL file at the hook payload's transcript_path) to give the policy
// judge context: what the developer asked for and what the agent just did.
//
// Everything returned is masked with secretscan and clipped, because it is sent
// to the judge. The transcript is written asynchronously and may lag by a turn;
// the context is advisory, so a short or empty result is fine.
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/secretscan"
)

const (
	tailBytes     = 1 << 20
	promptChars   = 400
	toolCallChars = 300
)

// Recent is the context the judge sees.
type Recent struct {
	Prompts   []string
	ToolCalls []string
}

type line struct {
	Type    string `json:"type"`
	IsMeta  bool   `json:"isMeta"`
	Message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type block struct {
	Type  string                 `json:"type"`
	Text  string                 `json:"text"`
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input"`
}

// Read returns up to maxPrompts developer prompts and maxCalls tool calls from
// the end of the transcript, oldest first, skipping the call with skipToolUseID
// (the one being judged).
func Read(path string, maxPrompts, maxCalls int, skipToolUseID string) Recent {
	var out Recent
	if strings.TrimSpace(path) == "" {
		return out
	}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > tailBytes {
		_, _ = f.Seek(info.Size()-tailBytes, io.SeekStart)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return out
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for scanner.Scan() {
		var l line
		if json.Unmarshal(scanner.Bytes(), &l) != nil {
			continue // includes the partial first line of a tail read
		}
		switch {
		case l.Type == "user" && !l.IsMeta:
			if text := promptText(l.Message.Content); text != "" {
				out.Prompts = append(out.Prompts, clip(secretscan.Mask(text), promptChars))
			}
		case l.Type == "assistant":
			var blocks []block
			if json.Unmarshal(l.Message.Content, &blocks) != nil {
				continue
			}
			for _, b := range blocks {
				if b.Type != "tool_use" || (skipToolUseID != "" && b.ID == skipToolUseID) {
					continue
				}
				out.ToolCalls = append(out.ToolCalls, clip(secretscan.Mask(describe(b)), toolCallChars))
			}
		}
	}
	out.Prompts = last(out.Prompts, maxPrompts)
	out.ToolCalls = last(out.ToolCalls, maxCalls)
	return out
}

// promptText returns what the developer typed: a string content, or the text
// blocks of a list that carries no tool results. Harness-generated user lines
// (command caveats, slash-command wrappers, summaries) are skipped.
func promptText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		var blocks []block
		if json.Unmarshal(raw, &blocks) != nil {
			return ""
		}
		var parts []string
		for _, b := range blocks {
			switch b.Type {
			case "tool_result":
				return ""
			case "text":
				parts = append(parts, b.Text)
			}
		}
		text = strings.Join(parts, "\n")
	}
	text = strings.TrimSpace(text)
	if text == "" || strings.HasPrefix(text, "<local-command") || strings.HasPrefix(text, "<command-") ||
		strings.HasPrefix(text, "This session is being continued") || strings.HasPrefix(text, "[Request interrupted") {
		return ""
	}
	return text
}

func describe(b block) string {
	for _, key := range []string{"command", "file_path", "pattern", "url", "path"} {
		if v, ok := b.Input[key].(string); ok && v != "" {
			return b.Name + ": " + v
		}
	}
	if raw, err := json.Marshal(b.Input); err == nil {
		return b.Name + ": " + string(raw)
	}
	return b.Name
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func last(items []string, n int) []string {
	if n <= 0 || len(items) <= n {
		return items
	}
	return items[len(items)-n:]
}

// ToolResult is what the transcript recorded for one tool call.
type ToolResult struct {
	IsError bool
	// Content is the text the agent received as the call's result.
	Content string
	// After is any text in the same message after the result, where a comment
	// the developer attached to an approval may land.
	After string
}

type resultBlock struct {
	Type      string          `json:"type"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
	Text      string          `json:"text"`
}

// ToolResults returns the recorded results for the given tool_use_ids, read
// from the tail of the transcript. IDs with no result yet are absent.
func ToolResults(path string, ids map[string]bool) map[string]ToolResult {
	out := map[string]ToolResult{}
	if strings.TrimSpace(path) == "" || len(ids) == 0 {
		return out
	}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > 4*tailBytes {
		_, _ = f.Seek(info.Size()-4*tailBytes, io.SeekStart)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return out
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for scanner.Scan() {
		var l line
		if json.Unmarshal(scanner.Bytes(), &l) != nil || l.Type != "user" {
			continue
		}
		var blocks []resultBlock
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			continue
		}
		for i, b := range blocks {
			if b.Type != "tool_result" || !ids[b.ToolUseID] {
				continue
			}
			res := ToolResult{IsError: b.IsError, Content: blockText(b.Content)}
			var after []string
			for _, next := range blocks[i+1:] {
				if next.Type == "text" && strings.TrimSpace(next.Text) != "" {
					after = append(after, strings.TrimSpace(next.Text))
				}
			}
			res.After = strings.Join(after, "\n")
			out[b.ToolUseID] = res
		}
	}
	return out
}

func blockText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []block
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var texts []string
	for _, p := range parts {
		if p.Type == "text" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}
