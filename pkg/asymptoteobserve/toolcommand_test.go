package asymptoteobserve

import "testing"

func TestShellCommandToolAndCommandLine(t *testing.T) {
	for _, tc := range []struct {
		name, tool string
		args       interface{}
		shell      bool
		want       string
	}{
		{"codex exec_command", "exec_command", `{"cmd":"go test ./...","workdir":"/repo"}`, true, "go test ./..."},
		{"codex shell argv", "shell", `{"command":["bash","-lc","git status"],"workdir":"/repo"}`, true, "bash -lc git status"},
		{"codex shell_command", "shell_command", `{"command":"git status","workdir":"/repo"}`, true, "git status"},
		{"claude Bash", "Bash", map[string]interface{}{"command": "npm test", "description": "run"}, true, "npm test"},
		{"antigravity run_command", "run_command", map[string]interface{}{"CommandLine": "npm test", "Cwd": "/repo"}, true, "npm test"},
		{"dsh terminal_send", "terminal_send", map[string]interface{}{"sessionId": "t1", "text": "ls -la", "submit": true}, true, "ls -la"},
		// `text` is an ordinary argument name; only terminal_send names its command line that way.
		{"text on another tool", "bash", map[string]interface{}{"text": "not a command"}, true, ""},
		// The argument is read for any tool; the editor guard that drops it is the caller's.
		{"str_replace_editor", "str_replace_editor", map[string]interface{}{"command": "view", "path": "/repo/a.go"}, false, "view"},
		{"mcp tool with a command argument", "mcp__deploy__run", `{"command":"status"}`, false, "status"},
		{"not json", "shell", "not json", true, ""},
	} {
		if got := IsShellCommandTool(tc.tool); got != tc.shell {
			t.Errorf("%s: IsShellCommandTool(%q) = %v, want %v", tc.name, tc.tool, got, tc.shell)
		}
		if got := ToolCommandLine(tc.tool, tc.args); got != tc.want {
			t.Errorf("%s: ToolCommandLine = %q, want %q", tc.name, got, tc.want)
		}
	}
}
