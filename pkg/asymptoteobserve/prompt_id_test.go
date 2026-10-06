package asymptoteobserve

import (
	"encoding/json"
	"testing"
)

// Every runtime spelling of one prompt's identifier lands in the same field. This is the table the
// alias list exists for: a consumer filters on prompt.id and never learns that Cursor calls it
// generation_id or Codex turn_id.
func TestPromptIDFromNormalizesEveryRuntimeSpelling(t *testing.T) {
	for name, tc := range map[string]struct {
		payload map[string]interface{}
		want    string
	}{
		"claude code otlp":              {map[string]interface{}{"prompt.id": "7f1c"}, "7f1c"},
		"beacon canonical":              {map[string]interface{}{"beacon.prompt.id": "b-1"}, "b-1"},
		"claude code transcript":        {map[string]interface{}{"promptId": "7f1c"}, "7f1c"},
		"gemini cli":                    {map[string]interface{}{"prompt_id": "s########3"}, "s########3"},
		"codex otlp":                    {map[string]interface{}{"turn.id": "turn-a"}, "turn-a"},
		"codex hooks":                   {map[string]interface{}{"turn_id": "turn-a"}, "turn-a"},
		"kimi code number":              {map[string]interface{}{"turn_id": float64(3)}, "3"},
		"cursor hooks":                  {map[string]interface{}{"generation_id": "gen-1", "conversation_id": "conv"}, "gen-1"},
		"trimmed":                       {map[string]interface{}{"turn_id": "  t  "}, "t"},
		"canonical wins":                {map[string]interface{}{"prompt.id": "canon", "generation_id": "native"}, "canon"},
		"json number":                   {map[string]interface{}{"turn_id": json.Number("12")}, "12"},
		"fractional is not an id":       {map[string]interface{}{"turn_id": 1.5}, ""},
		"object is not an id":           {map[string]interface{}{"turn_id": map[string]interface{}{"id": "x"}}, ""},
		"bool is not an id":             {map[string]interface{}{"turn_id": true}, ""},
		"blank is not an id":            {map[string]interface{}{"prompt.id": "  ", "turn_id": "next"}, "next"},
		"session id is not a prompt id": {map[string]interface{}{"session_id": "s", "conversation_id": "c"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := PromptIDFrom(tc.payload); got != tc.want {
				t.Fatalf("PromptIDFrom = %q, want %q", got, tc.want)
			}
		})
	}
}

// GitHub Copilot opens a turn per model call inside one prompt, so its turn id would split one
// prompt's work into several. Its spellings stay out of the list.
func TestPromptIDKeysExcludeCopilotTurnIDs(t *testing.T) {
	for _, key := range PromptIDKeys {
		switch key {
		case "turnId", "github.copilot.turn_id":
			t.Fatalf("PromptIDKeys includes %q, a per-model-call turn id", key)
		}
	}
}

func TestSetPromptIDKeepsPromptText(t *testing.T) {
	event := &Event{Prompt: &PromptInfo{Text: "fix the build"}}
	SetPromptID(event, " p-1 ")
	if event.Prompt.Text != "fix the build" || event.Prompt.ID != "p-1" {
		t.Fatalf("prompt = %#v, want text kept and id p-1", event.Prompt)
	}
	if got := PromptID(event); got != "p-1" {
		t.Fatalf("PromptID = %q, want p-1", got)
	}

	bare := &Event{}
	SetPromptID(bare, "")
	if bare.Prompt != nil {
		t.Fatalf("an empty id created a prompt object: %#v", bare.Prompt)
	}
	SetPromptID(bare, "p-2")
	line, err := json.Marshal(bare.Prompt)
	if err != nil {
		t.Fatal(err)
	}
	if string(line) != `{"id":"p-2"}` {
		t.Fatalf("prompt = %s, want only the id", line)
	}
}
