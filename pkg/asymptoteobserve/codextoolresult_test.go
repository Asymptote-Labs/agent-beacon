package asymptoteobserve

import "testing"

func TestCodexExitCodeIsReadFromTheHeaderOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		output string
		code   int
		ok     bool
	}{
		"exited nonzero": {"Chunk ID: a\nWall time: 0.1 seconds\nProcess exited with code 2\nOutput:\nboom\n", 2, true},
		// shell and shell_command write their own header form.
		"shell header": {"Exit code: 1\nWall time: 0.2 seconds\nOutput:\nboom\n", 1, true},
		// A command that prints the header's wording is not its own exit status.
		"line in output": {"Chunk ID: a\nProcess running with session ID 7\nOutput:\nProcess exited with code 0\n", 0, false},
		"no header":      {"plain text\n", 0, false},
	} {
		code, ok := codexExitCode(tc.output)
		if code != tc.code || ok != tc.ok {
			t.Errorf("%s: codexExitCode = %d, %v; want %d, %v", name, code, ok, tc.code, tc.ok)
		}
	}
}

// The rollout records no success flag, so a nonzero exit is what marks a failed command on both
// capture paths alike.
func TestCodexShellResultWithNonzeroExitIsAFailure(t *testing.T) {
	got := NormalizeCodexToolResult(CodexToolResult{
		Name: "exec_command", Arguments: `{"cmd":"npm test"}`,
		Output: "Process exited with code 1\nOutput:\nfailed\n",
	})
	if got.Severity != SeverityMedium || got.Command == nil || got.Command.ExitCode == nil || *got.Command.ExitCode != 1 {
		t.Fatalf("fields = %#v, want medium severity and exit code 1", got)
	}
	if got.GenAITool.Call.Result != nil {
		t.Fatalf("result = %v, want none: shell output is command.output", got.GenAITool.Call.Result)
	}
}

// An MCP server may name a tool like a shell. Its namespace says whose tool it is.
func TestCodexMCPToolNamedLikeAShellIsNotACommand(t *testing.T) {
	got := NormalizeCodexToolResult(CodexToolResult{
		Name: "shell", Namespace: "mcp__remote", Arguments: `{"command":"ls"}`, Output: "a\nb\n",
	})
	if got.Action != "mcp.tool_invoked" || got.Command != nil || got.MCP == nil || got.MCP.Server != "remote" {
		t.Fatalf("fields = %#v, want an MCP call on remote", got)
	}
}

// A call reported under the already-qualified name and no namespace is still an MCP call.
func TestCodexQualifiedMCPNameWithoutNamespaceIsAnMCPCall(t *testing.T) {
	got := NormalizeCodexToolResult(CodexToolResult{Name: "mcp__notes__save", Output: "saved"})
	if got.Action != "mcp.tool_invoked" || got.MCP == nil || got.MCP.Server != "notes" || got.MCP.Tool != "save" || got.Tool.Name != "mcp__notes__save" {
		t.Fatalf("fields = %#v mcp = %#v, want an MCP call on notes/save", got, got.MCP)
	}
}

// write_stdin returns a long-running exec_command's later output and exit status. It is not
// outside content, but it is the only record of that output.
func TestCodexNonShellResultKeepsItsOutput(t *testing.T) {
	got := NormalizeCodexToolResult(CodexToolResult{Name: "write_stdin", Output: "Process exited with code 0\nOutput:\ndone\n"})
	if got.Action != "tool.completed" || got.GenAITool.Call.Result != "Process exited with code 0\nOutput:\ndone\n" || got.Content == nil {
		t.Fatalf("fields = %#v, want tool.completed keeping the output as the result", got)
	}
}
