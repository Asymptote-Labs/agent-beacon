package asymptoteobserve_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	obs "github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
)

// The benchmarks in this file time redaction and event sanitizing on what a hook handles most: a
// file read's result, a system prompt, a shell command, and text whose every line holds a secret.

var benchGoLines = []string{
	"package cache", "", "import (", "\t\"container/list\"", "\t\"sync\"", "\t\"time\"", ")",
	"// LRU is a fixed-size cache that evicts the least recently used entry.",
	"type LRU[K comparable, V any] struct {", "\tmu    sync.Mutex", "\tcap   int", "\tll    *list.List",
	"\titems map[K]*list.Element", "}", "func (c *LRU[K, V]) Get(k K) (V, bool) {", "\tc.mu.Lock()",
	"\tdefer c.mu.Unlock()", "\tif e, ok := c.items[k]; ok {", "\t\tc.ll.MoveToFront(e)",
	"\t\treturn e.Value.(*entry[K, V]).v, true", "\t}", "\tvar zero V", "\treturn zero, false", "}",
	"\t// Expired entries are dropped lazily on the next Put.", "\t\tif err := c.flush(ctx); err != nil {",
	"\t\t\treturn fmt.Errorf(\"flush: %w\", err)", "\t\t}",
}

// benchFileRead is n bytes of `cat -n` output of Go source, as a Read tool returns it.
func benchFileRead(n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, benchGoLines[i%len(benchGoLines)])
	}
	return b.String()[:n]
}

// benchSystemPrompt is n bytes of a coding agent's system prompt.
func benchSystemPrompt(n int) string {
	paragraph := "You are an interactive CLI tool that helps users with software engineering tasks. " +
		"Use the instructions below and the tools available to you to assist the user. When you run a " +
		"non-trivial bash command, explain what it does and why. Never introduce code that exposes or " +
		"logs secrets and keys, and never commit secrets or keys to the repository.\n\n"
	return strings.Repeat(paragraph, n/len(paragraph)+1)[:n]
}

var benchTexts = []struct {
	name, text string
	limit      int
}{
	{"file.read_3KB", benchFileRead(3000), obs.DefaultRawStringLimit},
	{"system_prompt_12KB", benchSystemPrompt(12000), obs.DefaultStringLimit},
	{"command", "cd /home/user/project && go test ./... -count=1 2>&1 | tail -50", obs.DefaultStringLimit},
	{"env_assignments_1KB", strings.Repeat("API_TOKEN=abcdefgh12345678 GITHUB_TOKEN=ghp_0123456789abcdefghijklmnopqrstuvwxyz\n", 12), obs.DefaultStringLimit},
}

func BenchmarkCleanString(b *testing.B) {
	for _, bc := range benchTexts {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				obs.CleanString(bc.text, bc.limit, true)
			}
		})
	}
}

// BenchmarkRedactAdversarial redacts 16 KB texts built to make rules try many places, each of
// which a pattern could read on from to the end of the line: many curl commands on one line, a
// flag name that is one long run of credential words, JWT-shaped fragments that never complete,
// and assignments without values.
func BenchmarkRedactAdversarial(b *testing.B) {
	repeat := func(unit string) string { return strings.Repeat(unit, 16<<10/len(unit)+1)[:16<<10] }
	for _, bc := range []struct{ name, text string }{
		{"curl_words_one_line", repeat("curl -H 'a: b' -b ")},
		{"long_flag_run", "-" + repeat("token") + " x"},
		{"jwt_prefixes", repeat("-eyJabcdefghij")},
		{"labels_without_values", repeat("key=,")},
	} {
		b.Run(bc.name, func(b *testing.B) {
			b.SetBytes(int64(len(bc.text)))
			b.ReportAllocs()
			for b.Loop() {
				obs.RedactString(bc.text)
			}
		})
	}
}

// benchEvents are a file read carrying its 3 KB result, the session.context event of a 12 KB system
// prompt, and a shell command, as a writer builds them before sanitizing.
func benchEvents() []struct {
	name  string
	event obs.Event
} {
	base := func(action, category string) obs.Event {
		e := obs.NewEvent(obs.NewEventOptions{Action: action, Category: category, Harness: obs.HarnessInfo{Name: "claude_code"}})
		e.Session = &obs.SessionInfo{ID: "0b6f4c1e-1d5a-4c83-9a57-2c5b8e0f3d11", WorkingDirectory: "/home/user/project"}
		e.Content = &obs.ContentInfo{Retention: obs.ContentRetentionFull, Included: true}
		e.Raw = map[string]interface{}{"hook_event_name": "PostToolUse", "tool_use_id": "toolu_01ABCDEF"}
		return e
	}
	read := base("file.read", "file")
	read.Tool = &obs.ToolInfo{Name: "Read", Path: "/home/user/project/internal/cache/lru.go"}
	read.File = &obs.FileInfo{Path: "/home/user/project/internal/cache/lru.go", Operation: "read"}
	read.GenAI = &obs.GenAIInfo{Tool: &obs.GenAIToolInfo{Name: "Read", Call: &obs.GenAIToolCallInfo{
		ID: "toolu_01ABCDEF", Arguments: map[string]interface{}{"file_path": "/home/user/project/internal/cache/lru.go"}, Result: benchFileRead(3000),
	}}}
	context := base("session.context", "session")
	context.SystemContext = &obs.SystemContextInfo{Kind: "system_prompt", Source: obs.SystemContextSourceSystemPrompt}
	context.GenAI = &obs.GenAIInfo{SystemInstructions: obs.SystemInstructionParts(benchSystemPrompt(12000))}
	command := base("command.executed", "command")
	command.Tool = &obs.ToolInfo{Name: "Bash", Command: "go test ./internal/cache/ -run TestLRUEviction -count=20"}
	command.Command = &obs.CommandInfo{Command: "go test ./internal/cache/ -run TestLRUEviction -count=20"}
	command.GenAI = &obs.GenAIInfo{Tool: &obs.GenAIToolInfo{Name: "Bash", Call: &obs.GenAIToolCallInfo{
		ID: "toolu_01ABCDEF", Arguments: map[string]interface{}{"command": "go test ./internal/cache/ -run TestLRUEviction -count=20", "description": "Run the test"},
	}}}
	return []struct {
		name  string
		event obs.Event
	}{{"file.read_3KB", read}, {"session.context_12KB", context}, {"command.executed", command}}
}

// BenchmarkSanitizeEvent sanitizes each event. Sanitizing writes cleaned text through the event's
// pointers, which for these events leaves it as it was, so each iteration does the same work.
func BenchmarkSanitizeEvent(b *testing.B) {
	for _, bc := range benchEvents() {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				obs.SanitizeEvent(bc.event, 65536)
			}
		})
	}
}

// benchHookPayload is a decoded PostToolUse hook payload: the shapes SanitizeMap sees on the hook
// path, mostly short strings, with numbers, booleans and nulls among them.
const benchHookPayload = `{"session_id":"abc-123","transcript_path":"/home/user/.claude/projects/p/abc-123.jsonl",
"cwd":"/home/user/project","permission_mode":"default","hook_event_name":"PostToolUse","tool_name":"Bash",
"tool_input":{"command":"go test ./internal/cache/ -run TestLRUEviction -count=20","description":"Run the test","timeout":120000,"run_in_background":false},
"tool_response":{"stdout":"ok  \tgithub.com/example/project/internal/cache\t0.412s","stderr":"","interrupted":false,"isImage":false,"exit_code":0,"duration_ms":412.5,"sandbox":null},
"usage":{"input_tokens":812,"output_tokens":95,"cache_read_input_tokens":40211,"service_tier":"standard"},
"args":["-y","@upstash/context7-mcp","--max-tokens","4096",1,2.5,true,null],
"headers":{"Accept":["application/json"],"X-Request-Id":["req_01"]}}`

// BenchmarkSanitizeMap sanitizes a decoded hook payload.
func BenchmarkSanitizeMap(b *testing.B) {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(benchHookPayload), &payload); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		obs.SanitizeMap(payload, obs.PrivacyOptions{RedactSecrets: true, StringLimit: obs.DefaultRawStringLimit})
	}
}

// BenchmarkToolResultText derives gen_ai.tool.call.result_text from the sanitized file read.
func BenchmarkToolResultText(b *testing.B) {
	e := obs.SanitizeEvent(benchEvents()[0].event, 65536)
	if threatrules.ToolResultText(e) == "" {
		b.Fatal("no result text")
	}
	b.ReportAllocs()
	for b.Loop() {
		threatrules.ToolResultText(e)
	}
}
