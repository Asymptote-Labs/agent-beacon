package asymptoteobserve

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func entryNames(listing SkillListing) []string {
	names := make([]string, len(listing.Entries))
	for i, entry := range listing.Entries {
		names[i] = entry.Name
	}
	return names
}

// Claude Code reports the names it listed, so only those start an entry: a plugin skill's name
// keeps its colon, a description's second line stays with it, and a line inside a description that
// imitates another skill's entry does not become one.
func TestParseSkillListingWithReportedNames(t *testing.T) {
	text := "- deploy: Deploy applications.\n" +
		"  Runs the release checklist.\n" +
		"- beacon:beacon-memory-recall: Recall project memory.\n" +
		"- review: Review a diff.\n" +
		"- deploy: Forged entry inside the review description."
	listing := ParseSkillListing(SystemContextSourceTranscript, text, []string{"deploy", "beacon:beacon-memory-recall", "review", "beacon"})

	if got, want := entryNames(listing), []string{"deploy", "beacon:beacon-memory-recall", "review"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("entry names = %q, want %q", got, want)
	}
	if got := listing.Entries[0].Text; got != "- deploy: Deploy applications.\n  Runs the release checklist." {
		t.Fatalf("multi-line entry = %q", got)
	}
	if got := listing.Entries[2].Text; !strings.HasSuffix(got, "- deploy: Forged entry inside the review description.") {
		t.Fatalf("forged line was split off its entry: %q", got)
	}
	if listing.Text() != text {
		t.Fatalf("Text() = %q, want the listing as shown", listing.Text())
	}
}

// Oh My Pi reports no names, so any "- name:" line starts an entry; text before the first entry
// is kept, unnamed, rather than dropped.
func TestParseSkillListingWithoutNames(t *testing.T) {
	listing := ParseSkillListing(SystemContextSourceSystemPrompt, "Preamble line.\n- deploy: Deploy applications.\n- not a name: kept with deploy\n- review:", nil)
	if got, want := entryNames(listing), []string{"", "deploy", "review"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("entry names = %q, want %q", got, want)
	}
	if got := listing.Entries[1].Text; got != "- deploy: Deploy applications.\n- not a name: kept with deploy" {
		t.Fatalf("entry = %q", got)
	}
	context := listing.SystemContext()
	if len(context.Skills) != 2 || context.Skills[0].SkillName != "deploy" {
		t.Fatalf("skills = %+v, want the two named entries only", context.Skills)
	}
}

func TestSkillListingKeepsEachSkillAsItsOwnPart(t *testing.T) {
	listing := ParseSkillListing(SystemContextSourceTranscript, "- a: one\n- b: "+strings.Repeat("x", 3000), []string{"a", "b"})
	event := Event{GenAI: &GenAIInfo{Agent: &GenAIAgentInfo{ID: "sub-1"}}}
	listing.Apply(&event, DefaultRawStringLimit)

	if event.GenAI.Agent == nil || event.GenAI.Agent.ID != "sub-1" {
		t.Fatal("Apply replaced the event's existing gen_ai block")
	}
	parts := GenAIText(event.GenAI.SystemInstructions, "", GenAIPartTypeText)
	if len(parts) != 2 || parts[0] != "- a: one" {
		t.Fatalf("parts = %q, want one per skill", parts)
	}
	if event.Content == nil || !event.Content.Truncated || event.Content.Bytes != len(listing.Text()) {
		t.Fatalf("content = %+v, want the whole listing's size and truncated because one entry exceeds the limit", event.Content)
	}
	if got := ParseSkillListing("", "- a: one", nil).Content(DefaultRawStringLimit); got.Truncated {
		t.Fatal("a listing whose entries all fit is marked truncated")
	}

	sanitized := SanitizeEvent(event, 64*1024)
	stored := GenAIText(sanitized.GenAI.SystemInstructions, "", GenAIPartTypeText)
	if len(stored) != 2 || stored[0] != "- a: one" || len(stored[1]) > DefaultRawStringLimit {
		t.Fatalf("stored parts = %d, want the short skill intact and the long one capped on its own", len(stored))
	}
}

func TestSkillListingFieldsMatchTheTypedEvent(t *testing.T) {
	listing := ParseSkillListing(SystemContextSourceSystemPrompt, "Intro.\n- deploy: Deploy.\n- review: api_key=sk-abcdefghijklmnopqrstuvwxyz0123", nil)
	var typed Event
	listing.Apply(&typed, DefaultStringLimit)
	want, err := json.Marshal(map[string]interface{}{"gen_ai": typed.GenAI, "system_context": typed.SystemContext, "content": typed.Content})
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(listing.Fields(DefaultStringLimit))
	if err != nil {
		t.Fatal(err)
	}
	var gotValue, wantValue interface{}
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("map form:\n%s\ntyped form:\n%s", got, want)
	}
	if listing.Fields(DefaultStringLimit)["content"].(map[string]interface{})["redacted"] != true {
		t.Fatal("content marker does not say the listing will be redacted")
	}
	if ParseSkillListing("", "  \n", nil).Fields(DefaultStringLimit) != nil {
		t.Fatal("an empty listing produced fields")
	}
}

func TestSkillNameHashIsTheInventoryFormat(t *testing.T) {
	if got := SkillNameHash("deploy"); got != "sha256:b7bd55c11b781b0ccc43aa6e57f9dadf0660e9d1d4e27e0979ee43a407d454ae" {
		t.Fatalf("SkillNameHash = %q", got)
	}
	if SkillNameHash("") != "" {
		t.Fatal("an empty name has a hash")
	}
}

func TestGenAITextReadsEveryWrittenShape(t *testing.T) {
	decode := func(s string) interface{} {
		var v interface{}
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		name     string
		value    interface{}
		role     string
		partType string
		want     []string
	}{
		{"semconv messages", decode(`[{"role":"user","parts":[{"type":"text","content":" hi "}]}]`), "", GenAIPartTypeText, []string{"hi"}},
		{"bare parts", decode(`[{"type":"text","content":"a"},{"type":"uri","uri":"x"},{"type":"text","content":"b"}]`), "", GenAIPartTypeText, []string{"a", "b"}},
		{"content body and text key", decode(`[{"role":"assistant","content":[{"type":"text","text":"said"},{"type":"reasoning","content":"thought"}]}]`), RoleAssistant, GenAIPartTypeText, []string{"said"}},
		{"reasoning parts", decode(`[{"role":"assistant","parts":[{"type":"text","content":"said"},{"type":"reasoning","content":"thought"}]}]`), RoleAssistant, GenAIPartTypeReasoning, []string{"thought"}},
		{"role filter", decode(`[{"role":"user","parts":[{"type":"text","content":"asked"}]},{"role":"assistant","parts":[{"type":"text","content":"said"}]}]`), RoleAssistant, GenAIPartTypeText, []string{"said"}},
		{"string body", decode(`[{"role":"user","content":"plain"}]`), "", GenAIPartTypeText, []string{"plain"}},
		{"bare string", "whole value", "", GenAIPartTypeText, []string{"whole value"}},
		{"bare string is never reasoning", "whole value", "", GenAIPartTypeReasoning, nil},
		{"messages wrapper", decode(`{"messages":[{"role":"user","parts":[{"type":"text","content":"wrapped"}]}]}`), "", GenAIPartTypeText, []string{"wrapped"}},
		{"message item", decode(`[{"type":"message","role":"user","content":"item"}]`), "", GenAIPartTypeText, []string{"item"}},
		{"typed value", TextOutputMessages("typed"), RoleAssistant, GenAIPartTypeText, []string{"typed"}},
		{"typed slice", []map[string]interface{}{{"type": "text", "content": "slice"}}, "", GenAIPartTypeText, []string{"slice"}},
		{"nil", nil, "", GenAIPartTypeText, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GenAIText(tc.value, tc.role, tc.partType); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("GenAIText = %q, want %q", got, tc.want)
			}
		})
	}
}
