package asymptoteobserve

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestGenAITextLegacyBrowserMessages(t *testing.T) {
	cases := []struct {
		name, input, role, partType string
		want                        []string
	}{
		{"assistant", `[{"role":"assistant","text":" retained answer "}]`, RoleAssistant, GenAIPartTypeText, []string{"retained answer"}},
		{"input roles", `[{"role":"system","text":"policy"},{"role":"user","text":"question"}]`, "", GenAIPartTypeText, []string{"policy", "question"}},
		{"role filter", `[{"role":"user","text":"question"},{"role":"tool","text":"result"},{"role":"assistant","text":"answer"}]`, RoleAssistant, GenAIPartTypeText, []string{"answer"}},
		{"not reasoning", `[{"role":"assistant","text":"answer"}]`, RoleAssistant, GenAIPartTypeReasoning, nil},
		{"parts precedence", `[{"role":"assistant","text":"legacy","content":"content","parts":[{"type":"text","content":"parts"},{"type":"reasoning","content":"thought"}]}]`, RoleAssistant, GenAIPartTypeText, []string{"parts"}},
		{"content precedence", `[{"role":"assistant","text":"legacy","content":"content"}]`, RoleAssistant, GenAIPartTypeText, []string{"content"}},
		{"empty parts", `[{"role":"assistant","text":"must not resurrect","parts":[]}]`, RoleAssistant, GenAIPartTypeText, nil},
		{"nil parts override content", `[{"role":"assistant","text":"legacy","parts":null,"content":"content"}]`, RoleAssistant, GenAIPartTypeText, nil},
		{"empty content", `[{"role":"assistant","text":"must not resurrect","content":""}]`, RoleAssistant, GenAIPartTypeText, nil},
		{"nil content", `[{"role":"assistant","text":"must not resurrect","content":null}]`, RoleAssistant, GenAIPartTypeText, nil},
		{"reasoning only parts", `[{"role":"assistant","text":"must not resurrect","parts":[{"type":"reasoning","content":"thought"}]}]`, RoleAssistant, GenAIPartTypeText, nil},
		{"reasoning request", `[{"role":"assistant","text":"legacy","parts":[{"type":"text","content":"answer"},{"type":"reasoning","content":"thought"}]}]`, RoleAssistant, GenAIPartTypeReasoning, []string{"thought"}},
		{"part filters", `[{"type":"reasoning","text":"thought"},{"type":"tool_call","text":"args"},{"type":"text","text":"answer"}]`, RoleAssistant, GenAIPartTypeText, []string{"answer"}},
		{"nonmessage typed map", `[{"role":"assistant","type":"reasoning","text":"thought"}]`, RoleAssistant, GenAIPartTypeText, nil},
		{"no role", `[{"text":"not a legacy browser message"}]`, RoleAssistant, GenAIPartTypeText, nil},
		{"empty text", `[{"role":"assistant","text":"  "},{"role":"assistant"}]`, RoleAssistant, GenAIPartTypeText, nil},
		{"nonstring text", `[{"role":"assistant","text":{"content":"not text"}}]`, RoleAssistant, GenAIPartTypeText, nil},
		{"wrapped legacy", `{"messages":[{"role":"assistant","text":"[email] retained prefix"}]}`, RoleAssistant, GenAIPartTypeText, []string{"[email] retained prefix"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var value interface{}
			if err := json.Unmarshal([]byte(tc.input), &value); err != nil {
				t.Fatal(err)
			}
			if got := GenAIText(value, tc.role, tc.partType); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("GenAIText = %q, want %q", got, tc.want)
			}
		})
	}
	t.Run("typed legacy", func(t *testing.T) {
		value := []struct {
			Role string `json:"role"`
			Text string `json:"text"`
		}{{RoleAssistant, "typed answer"}}
		if got := GenAIText(value, RoleAssistant, GenAIPartTypeText); !reflect.DeepEqual(got, []string{"typed answer"}) {
			t.Fatalf("typed legacy text = %q", got)
		}
	})
}
