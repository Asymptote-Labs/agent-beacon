package asymptoteobserve_test

import (
	"fmt"
	"strings"
	"testing"

	obs "github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// The benchmarks in this file time redaction on the text a hook redacts most: a file read's result,
// a system prompt, a shell command, and text whose every line holds a secret.

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
