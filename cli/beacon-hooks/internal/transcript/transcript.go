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
