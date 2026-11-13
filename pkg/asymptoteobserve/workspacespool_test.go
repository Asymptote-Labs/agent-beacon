package asymptoteobserve

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestValidSpoolName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"60fdb1c1-0000-5000-8000-000000000000", true},
		{"session-abc_123.v2", true},
		{"claude_code", true},
		{"", false},
		{".", false},
		{"..", false},
		{".hidden", false},
		{"a/b", false},
		{"a\\b", false},
		{"../escape", false},
		{"has space", false},
		{"line\nbreak", false},
		{string(make([]byte, 129)), false},
	}
	for _, tc := range cases {
		if got := ValidSpoolName(tc.name); got != tc.want {
			t.Errorf("ValidSpoolName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The writer and every drainer must derive the same directory from the same inputs, and an unsafe
// name must yield none on every side rather than one side's sanitized guess.
func TestWorkspaceSpoolDirAgreesOnSafeAndUnsafeNames(t *testing.T) {
	workspace := t.TempDir()
	dir, ok := WorkspaceSpoolDir(workspace, "opencode", "sess-1")
	if !ok {
		t.Fatal("safe names produced no directory")
	}
	if want := filepath.Join(workspace, ".beacon", "spool", "opencode", "sess-1"); dir != want {
		t.Errorf("WorkspaceSpoolDir = %q, want %q", dir, want)
	}
	for _, tc := range []struct{ workspace, harness, session string }{
		{workspace, "opencode", "../evil"},
		{workspace, "../evil", "sess-1"},
		{workspace, "", "sess-1"},
		{"  ", "opencode", "sess-1"},
	} {
		if _, ok := WorkspaceSpoolDir(tc.workspace, tc.harness, tc.session); ok {
			t.Errorf("WorkspaceSpoolDir(%q, %q, %q) produced a directory", tc.workspace, tc.harness, tc.session)
		}
	}
	if dir, ok := LegacyDSHSpoolDir(workspace, "sess-1"); !ok || dir != filepath.Join(workspace, ".beacon", "dsh-spool", "sess-1") {
		t.Errorf("LegacyDSHSpoolDir = %q, %v", dir, ok)
	}
	if _, ok := LegacyDSHSpoolDir(workspace, "../evil"); ok {
		t.Error("LegacyDSHSpoolDir produced a directory for an unsafe id")
	}
}

// A workspace spool is writable by whatever can write the workspace, so a drain admits only what a
// Beacon hook or plugin of that very session stages.
func TestAdmitStagedLine(t *testing.T) {
	line := func(mutate func(map[string]interface{})) []byte {
		event := map[string]interface{}{
			"vendor":  Vendor,
			"product": Product,
			"harness": map[string]interface{}{"name": "opencode", "collection_method": CollectionMethodPlugin},
			"session": map[string]interface{}{"id": "sess-1"},
			"event":   map[string]interface{}{"action": "prompt.submitted"},
		}
		if mutate != nil {
			mutate(event)
		}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	if !AdmitStagedLine(line(nil), "opencode", "sess-1") {
		t.Fatal("a plugin event of the session was refused")
	}
	if !AdmitStagedLine(line(func(e map[string]interface{}) {
		e["harness"] = map[string]interface{}{"name": "opencode", "collection_method": CollectionMethodHook}
	}), "opencode", "sess-1") {
		t.Fatal("a hook event of the session was refused")
	}
	for name, tc := range map[string]struct {
		line             []byte
		harness, session string
	}{
		"another session": {line(nil), "opencode", "sess-2"},
		"another harness": {line(nil), "claude_code", "sess-1"},
		"no session":      {line(func(e map[string]interface{}) { delete(e, "session") }), "opencode", "sess-1"},
		"poll event": {line(func(e map[string]interface{}) {
			e["harness"].(map[string]interface{})["collection_method"] = CollectionMethodPoll
		}), "opencode", "sess-1"},
		"Beacon-only action": {line(func(e map[string]interface{}) { e["event"] = map[string]interface{}{"action": ActionShareRequested} }), "opencode", "sess-1"},
		"no action":          {line(func(e map[string]interface{}) { e["event"] = map[string]interface{}{} }), "opencode", "sess-1"},
		"another vendor":     {line(func(e map[string]interface{}) { e["vendor"] = "someone-else" }), "opencode", "sess-1"},
		"torn line":          {line(nil)[:20], "opencode", "sess-1"},
	} {
		if AdmitStagedLine(tc.line, tc.harness, tc.session) {
			t.Errorf("%s: admitted", name)
		}
	}
}
