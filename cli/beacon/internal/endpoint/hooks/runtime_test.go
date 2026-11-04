package hooks

import (
	"encoding/json"
	"testing"
)

func TestConfigHasEndpointHookFindsCommandsAtAnyDepth(t *testing.T) {
	flags := `'/home/u/.beacon/endpoint/hooks/beacon-hooks' --platform cursor --log '/home/u/runtime.jsonl' pre-tool`
	legacy := `BEACON_ENDPOINT_MODE=1 /home/u/.beacon/endpoint/hooks/beacon-hooks --platform cursor pre-tool`
	other := `/usr/local/bin/some-linter --platform cursor`
	for name, tc := range map[string]struct {
		doc      string
		platform string
		want     bool
	}{
		"flags form, nested JSON": {`{"version":1,"hooks":{"beforeShellExecution":[{"command":` + jsonQuote(flags) + `}]}}`, "cursor", true},
		"legacy prefix":           {`{"hooks":{"stop":[{"command":` + jsonQuote(legacy) + `}]}}`, "cursor", true},
		"another platform":        {`{"hooks":{"stop":[{"command":` + jsonQuote(flags) + `}]}}`, "antigravity", false},
		"any platform":            {`{"hooks":{"stop":[{"command":` + jsonQuote(flags) + `}]}}`, "", true},
		"someone else's hook":     {`{"hooks":{"stop":[{"command":` + jsonQuote(other) + `}]}}`, "cursor", false},
		"YAML":                    {"hooks:\n  pre_tool_call:\n    - command: " + jsonQuote(flags) + "\n", "cursor", true},
	} {
		got, err := ConfigHasEndpointHook([]byte(tc.doc), tc.platform)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %v, err %v, want %v", name, got, err, tc.want)
		}
	}
	if _, err := ConfigHasEndpointHook([]byte("{not json"), "cursor"); err == nil {
		t.Error("an unparsable config must return its error")
	}
}

func jsonQuote(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}
