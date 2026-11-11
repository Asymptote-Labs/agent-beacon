package threatrules

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// BenchmarkScanCorpus scans every fixture event of the open corpus and the embedded baseline
// with every rule, as `beacon scan` does.
func BenchmarkScanCorpus(b *testing.B) {
	var compiled []*CompiledRule
	var events []asymptoteobserve.Event
	for _, r := range corpusRules(b) {
		c, err := Compile(r)
		if err != nil {
			b.Fatalf("%s: %v", r.ID, err)
		}
		compiled = append(compiled, c)
		for _, f := range r.Tests {
			events = append(events, f.EventList()...)
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ScanEvents(compiled, events); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkScanEvent judges one event with every single-event rule of the open corpus and the
// embedded baseline, as a caller that evaluates events as they arrive does: a file.read carrying
// a 3 KB result, the session.context of a 12 KB system prompt, and a command.executed.
func BenchmarkScanEvent(b *testing.B) {
	var single []*CompiledRule
	for _, r := range corpusRules(b) {
		if r.Correlation != nil {
			continue
		}
		c, err := Compile(r)
		if err != nil {
			b.Fatalf("%s: %v", r.ID, err)
		}
		single = append(single, c)
	}
	scanner, err := NewEventScanner(single)
	if err != nil {
		b.Fatal(err)
	}
	for _, bc := range scanBenchEvents(b) {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := scanner.ScanEvent(bc.event); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

type namedEvent struct {
	name  string
	event asymptoteobserve.Event
}

// scanBenchEvents are three events of an ordinary coding-agent session.
func scanBenchEvents(tb testing.TB) []namedEvent {
	tb.Helper()
	source := []string{
		"\treturn zero, false",
		"}",
		"\tif c.ll.Len() > c.cap {",
		"\t\tc.evict(time.Now())",
		"\t}",
		"\t// Expired entries are dropped lazily on the next Put.",
		"\tfor e := c.ll.Back(); e != nil; e = e.Prev() {",
		"\t\tentry := e.Value.(*cacheEntry[K, V])",
		"\t\tif now.Before(entry.expires) {",
		"\t\t\tbreak",
	}
	var read strings.Builder
	for i := 0; read.Len() < 3000; i++ {
		fmt.Fprintf(&read, "%6d\t%s\n", i+1, source[i%len(source)])
	}
	prompt := "You are an interactive CLI tool that helps users with software engineering tasks. " +
		"Use the instructions below and the tools available to you to assist the user. " +
		"When reading files, prefer the Read tool over cat; when searching, prefer Grep. " +
		"Never commit changes unless the user explicitly asks you to. Keep responses short. "
	var system strings.Builder
	for system.Len() < 12000 {
		system.WriteString(prompt)
	}
	common := `"timestamp":"2023-11-14T22:13:20.000000000Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","severity":"info","endpoint":{"hostname":"dev-laptop","os":"darwin"},"harness":{"name":"claude_code","version":"2.1.306"},"origin":"local","session":{"id":"a0"},"content":{"retention":"full","included":true},"model":"claude-sonnet-4-5-20250929"`
	quote := func(s string) string {
		raw, _ := json.Marshal(s)
		return string(raw)
	}
	lines := []namedEvent{
		{"file.read_3KB", decodeBenchEvent(tb, `{`+common+`,"event":{"kind":"agent_runtime","action":"file.read","category":"file","fidelity":"inferred"},"tool":{"name":"Read"},"gen_ai":{"conversation":{"id":"a0"},"tool":{"name":"Read","call":{"id":"toolu_7c20e02dae62425abf1f477f","result":`+quote(read.String()[:3000])+`}}}}`)},
		{"session.context_12KB", decodeBenchEvent(tb, `{`+common+`,"event":{"kind":"agent_runtime","action":"session.context","category":"session","fidelity":"observed"},"gen_ai":{"conversation":{"id":"a0"},"system_instructions":[{"type":"text","content":`+quote(system.String())+`}]}}`)},
		{"command.executed", decodeBenchEvent(tb, `{`+common+`,"event":{"kind":"agent_runtime","action":"command.executed","category":"command","fidelity":"inferred"},"tool":{"name":"Bash","command":"git status"},"command":{"command":"git status"},"gen_ai":{"conversation":{"id":"a0"},"tool":{"name":"Bash","call":{"id":"toolu_cebc22facd3550b05ae88599","arguments":"{\"command\":\"git status\",\"description\":\"Run git\"}"}}}}`)},
	}
	return lines
}

func decodeBenchEvent(tb testing.TB, line string) asymptoteobserve.Event {
	tb.Helper()
	var e asymptoteobserve.Event
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		tb.Fatal(err)
	}
	return e
}
