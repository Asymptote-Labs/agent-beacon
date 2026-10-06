package threatrules

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const skillCanary = "BCN-2AFE23-C01I"

func systemContextEvent(instructions interface{}) asymptoteobserve.Event {
	return asymptoteobserve.Event{
		Event:   asymptoteobserve.EventInfo{Action: "session.context"},
		Session: &asymptoteobserve.SessionInfo{ID: "s1"},
		GenAI:   &asymptoteobserve.GenAIInfo{SystemInstructions: instructions},
	}
}

func decodeEvent(t *testing.T, line string) asymptoteobserve.Event {
	t.Helper()
	var event asymptoteobserve.Event
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		t.Fatalf("decode %s: %v", line, err)
	}
	return event
}

func TestSystemInstructionsTextIsAddressableInCEL(t *testing.T) {
	var found bool
	for _, f := range EventFields() {
		if f.Path == SystemInstructionsTextPath {
			found = true
			if f.Type != "string" {
				t.Fatalf("%s type = %q, want string", f.Path, f.Type)
			}
		}
	}
	if !found {
		t.Fatalf("%s missing from EventFields", SystemInstructionsTextPath)
	}
	if _, err := CompileMatch(`e.gen_ai.system_instructions == "x"`); err == nil {
		t.Fatal("gen_ai.system_instructions compiled; want only the derived text in the CEL schema")
	}
}

// Beacon's own events and OTLP-sourced ones carry the same semconv shape, so one rule matches both.
func TestSystemInstructionsTextReadsTheSemconvShapes(t *testing.T) {
	const expr = `e.gen_ai.system_instructions_text.contains("` + skillCanary + `")`
	cases := map[string]asymptoteobserve.Event{
		"typed parts from a mapper": systemContextEvent(asymptoteobserve.SystemInstructionParts("- oracle-index: " + skillCanary)),
		"parts from a log line":     decodeEvent(t, `{"gen_ai":{"system_instructions":[{"type":"text","content":"`+skillCanary+`"}]}}`),
		"bare string from OTLP":     decodeEvent(t, `{"gen_ai":{"system_instructions":"`+skillCanary+`"}}`),
		"text part among others": decodeEvent(t, `{"gen_ai":{"system_instructions":[`+
			`{"type":"uri","uri":"https://example.test"},{"type":"text","content":"`+skillCanary+`"}]}}`),
	}
	for name, event := range cases {
		t.Run(name, func(t *testing.T) {
			if !mustMatch(t, expr, event) {
				t.Fatalf("no match; derived text = %q", SystemInstructionsText(event))
			}
		})
	}

	nonText := decodeEvent(t, `{"gen_ai":{"system_instructions":[{"type":"uri","uri":"`+skillCanary+`"}]}}`)
	if got := SystemInstructionsText(nonText); got != "" {
		t.Fatalf("non-text part derived %q, want empty", got)
	}
	multi := systemContextEvent([]interface{}{
		map[string]interface{}{"type": "text", "content": "first"},
		map[string]interface{}{"type": "text", "content": "second"},
	})
	if got := SystemInstructionsText(multi); got != "first\nsecond" {
		t.Fatalf("multi-part text = %q, want parts joined in order", got)
	}
}

func TestSystemInstructionsTextEmptyWhenContentNotRetained(t *testing.T) {
	for name, content := range map[string]*asymptoteobserve.ContentInfo{
		"not included":  {Retention: asymptoteobserve.ContentRetentionFull, Included: false},
		"metadata only": {Retention: asymptoteobserve.ContentRetentionMetadata, Included: true},
	} {
		t.Run(name, func(t *testing.T) {
			event := systemContextEvent(asymptoteobserve.SystemInstructionParts(skillCanary))
			event.Content = content
			if got := SystemInstructionsText(event); got != "" {
				t.Fatalf("SystemInstructionsText = %q, want empty", got)
			}
			if mustMatch(t, `e.gen_ai.system_instructions_text != ""`, event) {
				t.Fatal("system_instructions_text non-empty in CEL")
			}
		})
	}
}

func TestSystemInstructionsTextIsRedactedAndCapped(t *testing.T) {
	const secret = "sk-abcdefghijklmnopqrstuvwxyz0123"
	event := systemContextEvent(asymptoteobserve.SystemInstructionParts("skill index\napi_key=" + secret))
	text := SystemInstructionsText(event)
	if strings.Contains(text, secret) || !strings.Contains(text, "[REDACTED]") {
		t.Fatalf("derived text = %q, want the redacted copy", text)
	}
	if mustMatch(t, `e.gen_ai.system_instructions_text.contains("`+secret+`")`, event) {
		t.Fatal("a rule matched the raw secret")
	}

	long := systemContextEvent(asymptoteobserve.SystemInstructionParts(strings.Repeat("a", SystemInstructionsPartLimit*3)))
	if got := SystemInstructionsText(long); len(got) > SystemInstructionsPartLimit {
		t.Fatalf("one part derived %d bytes, want <= %d", len(got), SystemInstructionsPartLimit)
	}
	many := make([]interface{}, 40)
	for i := range many {
		many[i] = map[string]interface{}{"type": "text", "content": strings.Repeat("b", SystemInstructionsPartLimit)}
	}
	if got := SystemInstructionsText(systemContextEvent(many)); len(got) > SystemInstructionsTextLimit {
		t.Fatalf("whole value derived %d bytes, want <= %d", len(got), SystemInstructionsTextLimit)
	}
}

// The case the per-part bound exists for: Claude Code's real index ran to about 6 KB for fifteen
// skills, and the writer caps each stored string at 2 KB. One part kept the first four skills; one
// part per skill keeps all of them, so a skill listed last is as visible as one listed first.
func TestSystemInstructionsTextSeesEverySkillOfALongIndex(t *testing.T) {
	names := make([]string, 15)
	var lines []string
	for i := range names {
		names[i] = fmt.Sprintf("skill-%02d", i)
		lines = append(lines, "- "+names[i]+": "+strings.Repeat("Describes when to use this skill. ", 12))
	}
	lines[14] += " " + skillCanary
	listing := asymptoteobserve.ParseSkillListing(asymptoteobserve.SystemContextSourceTranscript, strings.Join(lines, "\n"), names)
	if len(listing.Text()) < 6000 {
		t.Fatalf("fixture is %d bytes, want a realistic 6 KB index", len(listing.Text()))
	}
	event := systemContextEvent(nil)
	listing.Apply(&event, asymptoteobserve.DefaultRawStringLimit)

	line, err := json.Marshal(asymptoteobserve.SanitizeEvent(event, 64*1024))
	if err != nil {
		t.Fatal(err)
	}
	fromLog := decodeEvent(t, string(line))
	if !mustMatch(t, `e.gen_ai.system_instructions_text.contains("`+skillCanary+`")`, fromLog) {
		t.Fatalf("the last skill is invisible to rules after the writer stored the index")
	}
	if !mustMatch(t, `e.system_context.kind == "skill_listing" && e.system_context.skills.exists(s, s.skill_name == "skill-14")`, fromLog) {
		t.Fatal("system_context does not name the listed skills")
	}
}

func TestSystemInstructionsTextIsNeverSerialized(t *testing.T) {
	event := systemContextEvent(asymptoteobserve.SystemInstructionParts("plain"))
	event.GenAI.SystemInstructionsText = "asserted"
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "system_instructions_text") || strings.Contains(string(data), "asserted") {
		t.Fatalf("system_instructions_text serialized: %s", data)
	}
	decoded := decodeEvent(t, `{"gen_ai":{"system_instructions_text":"`+skillCanary+`"}}`)
	if decoded.GenAI.SystemInstructionsText != "" {
		t.Fatalf("a log line set system_instructions_text: %q", decoded.GenAI.SystemInstructionsText)
	}
	if mustMatch(t, `e.gen_ai.system_instructions_text == "asserted"`, event) {
		t.Fatal("engine used a caller-asserted system_instructions_text")
	}
}

func TestCompiledRuleDerivesSystemInstructionsTextWithoutMutatingEvents(t *testing.T) {
	rule := validSingleEventRule()
	rule.Match = `e.gen_ai.system_instructions_text.contains("` + skillCanary + `")`
	compiled, err := Compile(rule)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !compiled.derived {
		t.Fatal("rule referencing system_instructions_text not marked as needing derivation")
	}
	events := []asymptoteobserve.Event{systemContextEvent(asymptoteobserve.SystemInstructionParts("Skill index " + skillCanary))}
	verdict, err := compiled.Evaluate(events)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if verdict != VerdictMatch {
		t.Fatalf("verdict = %s, want match", verdict)
	}
	if got := events[0].GenAI.SystemInstructionsText; got != "" {
		t.Fatalf("caller's event mutated: system_instructions_text = %q", got)
	}
}
