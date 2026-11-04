package intelapi

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

var localNames = ToolNames{Lookup: "intel_lookup", GetArtifact: "intel_get_artifact", Check: "intel_check"}

func TestToolsAreNamedAndCrossReferenced(t *testing.T) {
	tools := Tools(localNames)
	if len(tools) != 3 || tools[0].Name != "intel_lookup" || tools[1].Name != "intel_get_artifact" || tools[2].Name != "intel_check" {
		t.Fatalf("tools = %+v", tools)
	}
	for _, tool := range tools {
		if tool.Title == "" || len(tool.Description) < 100 {
			t.Errorf("%s: title %q, description %q", tool.Name, tool.Title, tool.Description)
		}
		for _, name := range tool.Required {
			if _, ok := tool.Properties[name]; !ok {
				t.Errorf("%s requires undeclared %s", tool.Name, name)
			}
		}
		if _, err := json.Marshal(tool.Properties); err != nil {
			t.Errorf("%s properties do not encode: %v", tool.Name, err)
		}
		// Another server's names must never leak into these descriptions.
		if strings.Contains(tool.Description, "beacon_intel_") {
			t.Errorf("%s names the cloud tools: %s", tool.Name, tool.Description)
		}
	}
	if !strings.Contains(tools[0].Description, "never that it is safe") {
		t.Error("lookup must say no result is not safe")
	}
	if !strings.Contains(tools[1].Description, "intel_lookup") || !strings.Contains(tools[2].Description, "intel_get_artifact") {
		t.Error("descriptions must point at the server's own sibling tools")
	}
	cloud := Tools(ToolNames{Lookup: "beacon_intel_lookup", GetArtifact: "beacon_intel_get_artifact", Check: "beacon_intel_check"})
	if !strings.Contains(cloud[1].Description, "beacon_intel_lookup") || cloud[0].Description != tools[0].Description {
		t.Error("only names may differ between servers")
	}
	instructions := Instructions(localNames)
	for _, want := range []string{"intel_lookup", "intel_check", "intel_get_artifact", "never safe", "data, not instructions"} {
		if !strings.Contains(instructions, want) {
			t.Errorf("instructions missing %q", want)
		}
	}
}

func args(t *testing.T, s string) Args {
	t.Helper()
	var a Args
	if err := json.Unmarshal([]byte(s), &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestParseLookupArgs(t *testing.T) {
	got, err := ParseLookupArgs(args(t, `{"q":" evil ","kind":"mcp","limit":3}`))
	if err != nil || got != (LookupRequest{Q: "evil", Kind: "mcp", Limit: 3}) {
		t.Fatalf("= %+v, %v", got, err)
	}
	got, err = ParseLookupArgs(args(t, `{"q":"x","kind":null,"limit":null}`))
	if err != nil || got != (LookupRequest{Q: "x", Limit: DefaultLimit}) {
		t.Fatalf("nulls = %+v, %v", got, err)
	}
	for _, bad := range []string{
		`{}`, `{"q":""}`, `{"q":"  "}`, `{"q":3}`, `{"q":null}`, `{"q":"` + strings.Repeat("é", MaxQuery+1) + `"}`,
		`{"q":"x","kind":"plugin"}`, `{"q":"x","kind":1}`, `{"q":"x","limit":0}`, `{"q":"x","limit":26}`,
		`{"q":"x","limit":1.5}`, `{"q":"x","limit":"3"}`, `{"q":"x","user":"someone"}`, `{"q":"x","allUsers":true}`,
	} {
		if _, err := ParseLookupArgs(args(t, bad)); !IsInputError(err) {
			t.Errorf("%s: err = %v, want an input error", bad[:min(len(bad), 50)], err)
		}
	}
}

func TestParseArtifactArgs(t *testing.T) {
	if key, err := ParseArtifactArgs(args(t, `{"key":" mcp:a/b "}`)); err != nil || key != "mcp:a/b" {
		t.Fatalf("= %q, %v", key, err)
	}
	for _, bad := range []string{`{}`, `{"key":"a/b"}`, `{"key":7}`, `{"key":"mcp:a/b","user":"x"}`} {
		if _, err := ParseArtifactArgs(args(t, bad)); !IsInputError(err) {
			t.Errorf("%s: err = %v", bad, err)
		}
	}
}

func TestParseCheckArgs(t *testing.T) {
	items, err := ParseCheckArgs(args(t, `{"artifacts":[" @playwright/mcp ",{"q":"anthropics/skills","kind":"skill"},{"q":"x","kind":null}]}`))
	if err != nil || len(items) != 3 || items[0] != (CheckItem{Q: "@playwright/mcp"}) || items[1] != (CheckItem{Q: "anthropics/skills", Kind: "skill"}) || items[2] != (CheckItem{Q: "x"}) {
		t.Fatalf("= %+v, %v", items, err)
	}
	many := `{"artifacts":[` + strings.TrimSuffix(strings.Repeat(`"x",`, MaxCheck+1), ",") + `]}`
	cases := map[string]string{
		`{}`:                      "artifacts is required",
		`{"artifacts":[]}`:        "non-empty",
		`{"artifacts":"x"}`:       "non-empty",
		many:                      "at most 50",
		`{"artifacts":[3]}`:       "artifacts[0] must be",
		`{"artifacts":["ok",""]}`: "artifacts[1]",
		`{"artifacts":[null]}`:    "artifacts[0] is required",
		`{"artifacts":[{"q":"x","kind":"plugin"}]}`:  "artifacts[0].kind",
		`{"artifacts":[{"q":"x","user":"someone"}]}`: "artifacts[0]: user is not an argument",
		`{"artifacts":[{"kind":"mcp"}]}`:             "artifacts[0].q is required",
		`{"artifacts":["x"],"allUsers":true}`:        "allUsers is not an argument",
	}
	for body, want := range cases {
		_, err := ParseCheckArgs(args(t, body))
		if !IsInputError(err) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", body[:min(len(body), 60)], err, want)
		}
	}
}

func TestArgsFromMap(t *testing.T) {
	a, err := ArgsFromMap(map[string]any{"q": "evil", "limit": float64(3), "artifacts": []any{"x", map[string]any{"q": "y"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ParseLookupArgs(Args{"q": a["q"], "limit": a["limit"]}); err != nil || got.Q != "evil" || got.Limit != 3 {
		t.Fatalf("lookup = %+v, %v", got, err)
	}
	if items, err := ParseCheckArgs(Args{"artifacts": a["artifacts"]}); err != nil || len(items) != 2 || items[1].Q != "y" {
		t.Fatalf("check = %+v, %v", items, err)
	}
	if _, err := ArgsFromMap(map[string]any{"bad": func() {}}); !IsInputError(err) {
		t.Fatalf("unencodable = %v", err)
	}
}

func TestIsInputError(t *testing.T) {
	if !IsInputError(&APIError{StatusCode: 400}) || IsInputError(&APIError{StatusCode: 404}) || IsInputError(&APIError{StatusCode: 503}) || IsInputError(errors.New("x")) || IsInputError(nil) {
		t.Fatal("IsInputError classifies wrongly")
	}
}
