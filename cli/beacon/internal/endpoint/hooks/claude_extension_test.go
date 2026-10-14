package hooks

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The extension points are nil in this build, and with them nil the settings Beacon writes must
// not change by a byte. The golden file was captured from the installer before they existed.
func TestInstallClaudeSettingsUnchangedWithoutExtensions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	existing := `{"env":{"EXISTING":"1"},"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo keep"}]}],"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"http","url":"https://guard.example/check","headers":{"X":"1"}}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}
	if err := installClaudeSettings(path, "/opt/beacon hooks/beacon-hooks", "/var/log/beacon-agent/runtime.jsonl", "/Library/Application Support/Beacon/Endpoint/config.json"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "claude_settings_golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	// A Windows checkout turns the golden file's line endings into CRLF; Beacon writes LF.
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(got, want) {
		t.Fatalf("settings changed without extensions:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestClaudeExtraHookGroupsFollowBeaconHooks(t *testing.T) {
	type change struct {
		path      string
		installed bool
	}
	var changes []change
	claudeExtraHookGroups = func(prefix string) map[string][]settingsHookGroup {
		return map[string][]settingsHookGroup{
			"PreToolUse":          {{Matcher: "mcp__.*", Hooks: []settingsHookRef{{Type: "command", Command: prefix + " extra-tool", Timeout: 15}}}},
			"UserPromptExpansion": {{Matcher: "extra-command", Hooks: []settingsHookRef{{Type: "command", Command: prefix + " extra-expansion", Timeout: 10}}}},
		}
	}
	claudeSettingsChanged = func(path string, installed bool) error {
		changes = append(changes, change{path, installed})
		return nil
	}
	t.Cleanup(func() {
		claudeExtraHookGroups = nil
		claudeSettingsChanged = nil
	})

	path := filepath.Join(t.TempDir(), "settings.json")
	existing := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo keep"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	// Installing twice must leave one copy of each group: the second install removes the first
	// install's groups, extra ones included, before writing them again.
	for i := 0; i < 2; i++ {
		if err := installClaudeSettings(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
			t.Fatal(err)
		}
	}
	settings, err := readSettingsHooks(path)
	if err != nil {
		t.Fatal(err)
	}
	pre := settings.hooks["PreToolUse"]
	if len(pre) != 3 {
		t.Fatalf("PreToolUse groups = %d, want the user's, Beacon's and the extra one: %+v", len(pre), pre)
	}
	if pre[0].Hooks[0].Command != "echo keep" || !strings.HasSuffix(pre[1].Hooks[0].Command, " pre-tool") || pre[2].Matcher != "mcp__.*" || !strings.HasSuffix(pre[2].Hooks[0].Command, " extra-tool") {
		t.Fatalf("PreToolUse groups out of order: %+v", pre)
	}
	expansion := settings.hooks["UserPromptExpansion"]
	if len(expansion) != 1 || expansion[0].Matcher != "extra-command" || expansion[0].Hooks[0].Timeout != 10 {
		t.Fatalf("UserPromptExpansion = %+v", expansion)
	}
	if !isClaudeInstalledAt(path) {
		t.Fatal("status does not see the installed hooks")
	}

	if _, err := removeClaudeEndpointHooks(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "beacon-hooks") || strings.Contains(string(data), "UserPromptExpansion") {
		t.Fatalf("uninstall left Beacon's or the extra hooks behind:\n%s", data)
	}
	if !strings.Contains(string(data), "echo keep") {
		t.Fatalf("uninstall removed the user's hook:\n%s", data)
	}

	want := []change{{path, true}, {path, true}, {path, false}}
	if len(changes) != len(want) {
		t.Fatalf("settings-changed calls = %+v, want %+v", changes, want)
	}
	for i := range want {
		if changes[i] != want[i] {
			t.Fatalf("settings-changed calls = %+v, want %+v", changes, want)
		}
	}
}
