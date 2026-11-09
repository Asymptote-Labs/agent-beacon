package harness

import "testing"

func TestBind(t *testing.T) {
	for _, tc := range []struct {
		name, args string
		want       Call
	}{
		{"Bash", `{"command":"ls"}`, Call{Kind: Shell, Name: "Bash", Command: "ls"}},
		{"exec_command", `{"cmd":"cat x"}`, Call{Kind: Shell, Name: "exec_command", Command: "cat x"}},
		{"shell", `{"command":["bash","-lc","ls"]}`, Call{Kind: Shell, Name: "shell", Command: "bash -lc ls"}},
		{"Read", `{"file_path":"/a/.env"}`, Call{Kind: Read, Name: "Read", Path: "/a/.env"}},
		{"read", `{"filePath":"/a/b"}`, Call{Kind: Read, Name: "read", Path: "/a/b"}},
		// omp's read fetches a URL: its result is outside content, not a file.
		{"read", `{"path":"https://example.com/p"}`, Call{Kind: Fetch, Name: "read", URL: "https://example.com/p"}},
		{"mcp__notes__get_note", `{}`, Call{Kind: MCP, Name: "mcp__notes__get_note", Server: "notes", Tool: "get_note"}},
		{"Write", `{"file_path":"/a"}`, Call{Kind: Other, Name: "Write"}},
	} {
		if got := Bind(tc.name, tc.args); got != tc.want {
			t.Errorf("Bind(%s, %s) = %+v, want %+v", tc.name, tc.args, got, tc.want)
		}
	}
}

// Harness names end in asymptoteobserve.NormalizeHarnessName, so a gateway and an endpoint name
// one harness the same way.
func TestIdentify(t *testing.T) {
	for ua, want := range map[string]Harness{
		"claude-cli/2.1.306 (external, cli)":        {"claude_code", "2.1.306"},
		"codex_cli_rs/0.163.0 (Mac OS 26.0; arm64)": {"codex_cli", "0.163.0"},
		"GitHubCopilotChat/0.30.0":                  {"vscode_copilot", "0.30.0"},
		"opencode/1.2.3":                            {"opencode", "1.2.3"},
		"Gemini-CLI/0.9":                            {"gemini_cli", "0.9"},
		"":                                          {Name: "unknown"},
		"My-Agent/1.0":                              {"my-agent", "1.0"},
	} {
		if got := Identify(ua); got != want {
			t.Errorf("Identify(%q) = %+v, want %+v", ua, got, want)
		}
	}
}
