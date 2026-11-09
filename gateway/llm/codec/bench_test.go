package codec

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/gateway/internal/synth"
	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

// Benchmark bodies follow a long agent session: a 12 KB system prompt, 40 tool definitions and
// 60 turns, each a file-read tool call answered by a ~3 KB result, about 250 KB in all.
const (
	benchTools = 40
	benchTurns = 60
)

func benchSystem() string {
	var b strings.Builder
	for b.Len() < 12<<10 {
		b.WriteString("You are an interactive CLI tool that helps users with software engineering tasks. ")
		b.WriteString("Use the instructions below and the tools available to you to assist the user.\n")
	}
	return b.String()
}

// benchFile is a file as a read tool returns it: numbered lines, ~3 KB.
func benchFile(turn int) string {
	var b strings.Builder
	for i := 1; b.Len() < 3<<10; i++ {
		fmt.Fprintf(&b, "%6d→\tif err := handle(ctx, req%d); err != nil { return fmt.Errorf(\"turn %d: %%w\", err) }\n", i, i, turn)
	}
	return b.String()
}

func benchSchema(i int) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string", "description": fmt.Sprintf("The absolute path to the file for tool %d", i)},
			"offset":    map[string]any{"type": "number", "description": "The line number to start reading from"},
			"limit":     map[string]any{"type": "number", "description": "The number of lines to read"},
		},
		"required":             []string{"file_path"},
		"additionalProperties": false,
	}
}

func benchBody(b *testing.B, v any) []byte {
	body, err := json.Marshal(v)
	if err != nil {
		b.Fatal(err)
	}
	return body
}

func benchAnthropicRequest(b *testing.B) []byte {
	sys := benchSystem()
	tools := make([]any, benchTools)
	for i := range tools {
		tools[i] = map[string]any{"name": fmt.Sprintf("Tool%d", i), "description": "Reads a file from the local filesystem.", "input_schema": benchSchema(i)}
	}
	msgs := []any{map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": "<system-reminder>\nAs you answer, follow CLAUDE.md.\n</system-reminder>"},
		map[string]any{"type": "text", "text": "Review every handler in the repository."},
	}}}
	for t := range benchTurns {
		id := fmt.Sprintf("toolu_%04d", t)
		msgs = append(msgs,
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "Reading the next handler."},
				map[string]any{"type": "tool_use", "id": id, "name": "Read", "input": map[string]any{"file_path": fmt.Sprintf("/repo/internal/handler%d.go", t)}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": id, "content": []any{map[string]any{"type": "text", "text": benchFile(t)}}},
			}},
		)
	}
	return benchBody(b, map[string]any{
		"model":      "claude-sonnet-4-5-20250929",
		"max_tokens": 32000,
		"stream":     true,
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude.", "cache_control": map[string]any{"type": "ephemeral"}},
			map[string]any{"type": "text", "text": sys, "cache_control": map[string]any{"type": "ephemeral"}},
		},
		"messages": msgs,
		"tools":    tools,
		"metadata": map[string]any{"user_id": "user_abc_account_123_session_456"},
		"thinking": map[string]any{"type": "enabled", "budget_tokens": 31999},
	})
}

func benchResponsesRequest(b *testing.B) []byte {
	tools := make([]any, benchTools)
	for i := range tools {
		tools[i] = map[string]any{"type": "function", "name": fmt.Sprintf("tool_%d", i), "description": "Reads a file.", "strict": false, "parameters": benchSchema(i)}
	}
	input := []any{
		map[string]any{"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": "<permissions instructions>sandbox_mode: workspace-write</permissions instructions>"}}},
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Review every handler in the repository."}}},
	}
	for t := range benchTurns {
		id := fmt.Sprintf("call_%04d", t)
		input = append(input,
			map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "Reading the next handler."}}, "encrypted_content": "gAAAAABo"},
			map[string]any{"type": "function_call", "call_id": id, "name": "shell", "arguments": fmt.Sprintf(`{"command":["cat","/repo/internal/handler%d.go"]}`, t)},
			map[string]any{"type": "function_call_output", "call_id": id, "output": benchFile(t)},
		)
	}
	return benchBody(b, map[string]any{
		"model":        "gpt-5-codex",
		"instructions": benchSystem(),
		"input":        input,
		"tools":        tools,
		"tool_choice":  "auto",
		"reasoning":    map[string]any{"effort": "medium", "summary": "auto"},
		"store":        false,
		"stream":       true,
		"include":      []string{"reasoning.encrypted_content"},
	})
}

func benchChatRequest(b *testing.B) []byte {
	tools := make([]any, benchTools)
	for i := range tools {
		tools[i] = map[string]any{"type": "function", "function": map[string]any{"name": fmt.Sprintf("tool_%d", i), "description": "Reads a file.", "parameters": benchSchema(i)}}
	}
	msgs := []any{
		map[string]any{"role": "system", "content": benchSystem()},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "Review every handler in the repository."}}},
	}
	for t := range benchTurns {
		id := fmt.Sprintf("call_%04d", t)
		msgs = append(msgs,
			map[string]any{"role": "assistant", "content": "Reading the next handler.", "tool_calls": []any{
				map[string]any{"id": id, "type": "function", "function": map[string]any{"name": "read_file", "arguments": fmt.Sprintf(`{"path":"/repo/internal/handler%d.go"}`, t)}},
			}},
			map[string]any{"role": "tool", "tool_call_id": id, "content": benchFile(t)},
		)
	}
	return benchBody(b, map[string]any{
		"model":          "gpt-4o",
		"messages":       msgs,
		"tools":          tools,
		"tool_choice":    "auto",
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	})
}

func BenchmarkDecodeRequest(b *testing.B) {
	for _, bc := range []struct {
		protocol llm.Protocol
		body     func(*testing.B) []byte
	}{
		{llm.AnthropicMessages, benchAnthropicRequest},
		{llm.OpenAIResponses, benchResponsesRequest},
		{llm.OpenAIChat, benchChatRequest},
	} {
		b.Run(string(bc.protocol), func(b *testing.B) {
			body := bc.body(b)
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				req, err := DecodeRequest(bc.protocol, body)
				if err != nil {
					b.Fatal(err)
				}
				if len(req.Messages) < 2*benchTurns {
					b.Fatalf("decoded %d messages", len(req.Messages))
				}
			}
		})
	}
}

// BenchmarkSkimRequest compares skimming with a full decode on the request of turn 300 of a
// synthetic session, ~1 MB of resent history in each protocol.
func BenchmarkSkimRequest(b *testing.B) {
	s := synth.NewScript(1, 0, 300, 3000)
	for _, p := range []llm.Protocol{llm.AnthropicMessages, llm.OpenAIChat, llm.OpenAIResponses} {
		body := synthRequest(s, p, 300, synth.Benign)
		for _, bc := range []struct {
			name   string
			decode func(llm.Protocol, []byte) (*llm.Request, error)
		}{{"decode", DecodeRequest}, {"skim", SkimRequest}} {
			b.Run(string(p)+"/"+bc.name, func(b *testing.B) {
				b.SetBytes(int64(len(body)))
				b.ReportAllocs()
				for b.Loop() {
					if _, err := bc.decode(p, body); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkSkimMemo skims the request of turn 10 and turn 300 of a synthetic session with the
// memo cold (emptied) and warm (holding what skimming the session's previous call cached: the
// body is that one plus a turn).
func BenchmarkSkimMemo(b *testing.B) {
	s := synth.NewScript(1, 0, 300, 3000)
	for _, turn := range []int{10, 300} {
		for _, p := range []llm.Protocol{llm.AnthropicMessages, llm.OpenAIChat, llm.OpenAIResponses} {
			prev, body := synthRequest(s, p, turn-1, synth.Benign), synthRequest(s, p, turn, synth.Benign)
			for _, warm := range []bool{false, true} {
				name := "cold"
				if warm {
					name = "warm"
				}
				b.Run(fmt.Sprintf("turn%d/%s/%s", turn, p, name), func(b *testing.B) {
					b.SetBytes(int64(len(body)))
					b.ReportAllocs()
					for b.Loop() {
						b.StopTimer()
						resetSkimMemo()
						if warm {
							if _, err := SkimRequest(p, prev); err != nil {
								b.Fatal(err)
							}
						}
						b.StartTimer()
						if _, err := SkimRequest(p, body); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		}
	}
}
