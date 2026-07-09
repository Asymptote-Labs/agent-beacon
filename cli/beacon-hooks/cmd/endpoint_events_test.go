package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
)

func writeGitHead(t *testing.T, repo, head string) {
	t.Helper()
	gitDir := filepath.Join(repo, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", gitDir, err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte(head), 0o644); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}
}

func TestResolveBranchPrefersRuntimeProvidedBranch(t *testing.T) {
	repo := t.TempDir()
	writeGitHead(t, repo, "ref: refs/heads/local-branch\n")

	got := resolveBranch(map[string]interface{}{"git_branch": "cloud-branch"}, repo)
	if got != "cloud-branch" {
		t.Fatalf("resolveBranch = %q, want runtime-provided cloud-branch", got)
	}
}

func TestResolveBranchReadsLocalCheckout(t *testing.T) {
	repo := t.TempDir()
	writeGitHead(t, repo, "ref: refs/heads/feature/local\n")

	got := resolveBranch(map[string]interface{}{}, repo)
	if got != "feature/local" {
		t.Fatalf("resolveBranch = %q, want feature/local", got)
	}
}

func TestResolveBranchOutsideRepo(t *testing.T) {
	if got := resolveBranch(map[string]interface{}{}, t.TempDir()); got != "" {
		t.Fatalf("resolveBranch = %q, want empty outside a repo", got)
	}
	if got := resolveBranch(map[string]interface{}{}, ""); got != "" {
		t.Fatalf("resolveBranch = %q, want empty without cwd", got)
	}
}

func TestResolveBranchDisabledByEnv(t *testing.T) {
	repo := t.TempDir()
	writeGitHead(t, repo, "ref: refs/heads/feature/local\n")

	for _, value := range []string{"1", "true", "YES"} {
		t.Setenv("BEACON_DISABLE_GIT_METADATA", value)
		if got := resolveBranch(map[string]interface{}{}, repo); got != "" {
			t.Fatalf("resolveBranch = %q, want empty with BEACON_DISABLE_GIT_METADATA=%s", got, value)
		}
		// Runtime-provided branch still passes through when disabled.
		if got := resolveBranch(map[string]interface{}{"git_branch": "cloud-branch"}, repo); got != "cloud-branch" {
			t.Fatalf("resolveBranch = %q, want runtime-provided branch to pass through", got)
		}
	}

	t.Setenv("BEACON_DISABLE_GIT_METADATA", "0")
	if got := resolveBranch(map[string]interface{}{}, repo); got != "feature/local" {
		t.Fatalf("resolveBranch = %q, want feature/local with disable flag off", got)
	}
}

func TestRunPromptSubmitEmitsLocalGitBranch(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	repo := t.TempDir()
	writeGitHead(t, repo, "ref: refs/heads/feature/local\n")

	runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"conversation_id": "conv-branch",
		"hook_event_name": "beforeSubmitPrompt",
		"workspace_roots": []interface{}{repo},
		"prompt":          "hello",
	})

	event := lastEndpointEvent(t, logPath)
	if got := event["branch"]; got != "feature/local" {
		t.Fatalf("branch = %q, want feature/local", got)
	}
	if got := event["repository"]; got != repo {
		t.Fatalf("repository = %q, want %q", got, repo)
	}
}

func TestRunPromptSubmitOmitsBranchOutsideRepo(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"conversation_id": "conv-no-repo",
		"hook_event_name": "beforeSubmitPrompt",
		"workspace_roots": []interface{}{t.TempDir()},
		"prompt":          "hello",
	})

	event := lastEndpointEvent(t, logPath)
	if branch, ok := event["branch"]; ok {
		t.Fatalf("branch = %q, want no branch field outside a repo", branch)
	}
}

func TestRunOpenCodeEventEmitsLocalGitBranch(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "opencode"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	repo := t.TempDir()
	writeGitHead(t, repo, "ref: refs/heads/feature/opencode\n")

	runHookWithInput(t, runOpenCodeEvent, map[string]interface{}{
		"type":       "command.executed",
		"session_id": "oc-branch",
		"cwd":        repo,
		"command":    "ls",
	})

	event := lastEndpointEvent(t, logPath)
	if got := event["branch"]; got != "feature/opencode" {
		t.Fatalf("branch = %q, want feature/opencode", got)
	}
}

func TestRunPostToolFileEditEmitsLocalGitBranch(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	repo := t.TempDir()
	writeGitHead(t, repo, "ref: refs/heads/feature/edit\n")
	editedFile := filepath.Join(repo, "pkg", "main.go")

	// No workspace_roots or cwd in the payload: branch must resolve from the
	// edited file's own directory.
	runHookWithInput(t, runPostTool, map[string]interface{}{
		"conversation_id": "conv-edit",
		"hook_event_name": "afterFileEdit",
		"file_path":       editedFile,
		"edits": []interface{}{
			map[string]interface{}{"old_string": "old line", "new_string": "new line"},
		},
	})

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "file.modified" {
		t.Fatalf("event.action = %q, want file.modified", action)
	}
	if got := event["branch"]; got != "feature/edit" {
		t.Fatalf("branch = %q, want feature/edit", got)
	}
}

func TestToolFieldsMapsMCPGenAIStandardFields(t *testing.T) {
	tests := []struct {
		name       string
		toolName   string
		toolInput  map[string]interface{}
		wantServer string
		wantTool   string
	}{
		{
			name:     "cursor tool name",
			toolName: "MCP:get_organizations",
			toolInput: map[string]interface{}{
				"jsonrpc_request_id":       "req-1",
				"jsonrpc_protocol_version": "2.0",
				"mcp_protocol_version":     "2025-06-18",
				"mcp_session_id":           "mcp-session",
				"network_transport":        "pipe",
			},
			wantTool: "get_organizations",
		},
		{
			name:       "claude tool name",
			toolName:   "mcp__memory__write",
			toolInput:  map[string]interface{}{},
			wantServer: "memory",
			wantTool:   "write",
		},
		{
			name:       "payload server tool wins",
			toolName:   "MCP:derived_tool",
			toolInput:  map[string]interface{}{"server_name": "linear", "tool_name": "issue-get"},
			wantServer: "linear",
			wantTool:   "issue-get",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := toolFieldsWithResponse(tt.toolName, tt.toolInput, map[string]interface{}{"ok": true})
			mcp := fields["mcp"].(map[string]interface{})
			if mcp["server"] != tt.wantServer || mcp["tool"] != tt.wantTool {
				t.Fatalf("mcp = %#v, want server=%q tool=%q", mcp, tt.wantServer, tt.wantTool)
			}
			method := mcp["method"].(map[string]interface{})
			if method["name"] != "tools/call" {
				t.Fatalf("mcp.method = %#v, want tools/call", method)
			}
			genAI := fields["gen_ai"].(map[string]interface{})
			if op := genAI["operation"].(map[string]interface{})["name"]; op != "execute_tool" {
				t.Fatalf("gen_ai.operation = %q, want execute_tool", op)
			}
			tool := genAI["tool"].(map[string]interface{})
			if tool["name"] != tt.wantTool {
				t.Fatalf("gen_ai.tool = %#v, want name %q", tool, tt.wantTool)
			}
			call := tool["call"].(map[string]interface{})
			if len(tt.toolInput) > 0 && call["arguments"] == nil {
				t.Fatalf("gen_ai.tool.call = %#v, want arguments", call)
			}
			if call["result"] == nil {
				t.Fatalf("gen_ai.tool.call = %#v, want result", call)
			}
		})
	}
}

func TestToolFieldsMapsMCPTransportContext(t *testing.T) {
	fields := toolFields("MCP:read_resource", map[string]interface{}{
		"resource_uri":             "file:///tmp/report.md",
		"jsonrpc_request_id":       "request-7",
		"jsonrpc":                  "2.0",
		"network_protocol_name":    "HTTP",
		"network_protocol_version": "2",
		"network_transport":        "TCP",
		"server_address":           "example.com",
		"server_port":              "443",
		"status_code":              "OK",
	})
	if got := fields["jsonrpc"].(map[string]interface{})["request"].(map[string]interface{})["id"]; got != "request-7" {
		t.Fatalf("jsonrpc.request.id = %q, want request-7", got)
	}
	if got := fields["network"].(map[string]interface{})["transport"]; got != "tcp" {
		t.Fatalf("network.transport = %q, want tcp", got)
	}
	if got := fields["server"].(map[string]interface{})["port"]; got != 443 {
		t.Fatalf("server.port = %#v, want 443", got)
	}
	if got := fields["rpc"].(map[string]interface{})["response"].(map[string]interface{})["status_code"]; got != "OK" {
		t.Fatalf("rpc.response.status_code = %q, want OK", got)
	}
	mcp := fields["mcp"].(map[string]interface{})
	if got := mcp["resource"].(map[string]interface{})["uri"]; got != "file:///tmp/report.md" {
		t.Fatalf("mcp.resource.uri = %q, want resource URI", got)
	}
}

func TestToolFieldsDoesNotTreatMCPArgumentIDAsJSONRPCRequestID(t *testing.T) {
	fields := toolFields("MCP:get_issue", map[string]interface{}{
		"id":   "issue-123",
		"name": "Release blocker",
	})
	jsonrpc, ok := fields["jsonrpc"].(map[string]interface{})
	if !ok {
		return
	}
	if request, ok := jsonrpc["request"]; ok {
		t.Fatalf("jsonrpc.request = %#v, want no request from generic tool argument id", request)
	}
}

func TestToolFieldsIgnoresFalseMCPErrorField(t *testing.T) {
	fields := toolFieldsWithResponse("MCP:get_organizations", map[string]interface{}{}, map[string]interface{}{
		"error": false,
		"ok":    true,
	})
	if _, ok := fields["error"]; ok {
		t.Fatalf("fields should not include error for false MCP error field: %#v", fields["error"])
	}
}

func TestToolFieldsMapsTruthyMCPErrorField(t *testing.T) {
	fields := toolFieldsWithResponse("MCP:get_organizations", map[string]interface{}{}, map[string]interface{}{
		"error": true,
	})
	errorFields, ok := fields["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("fields error missing or wrong type: %#v", fields["error"])
	}
	if got := errorFields["type"]; got != "tool_error" {
		t.Fatalf("error.type = %q, want tool_error", got)
	}
}

func TestToolFieldsDoesNotTreatGenericNameAsMCP(t *testing.T) {
	fields := toolFields("Write", map[string]interface{}{"name": "README.md"})
	if _, ok := fields["mcp"]; ok {
		t.Fatalf("non-MCP tool should not include mcp fields: %#v", fields)
	}
	if _, ok := fields["gen_ai"]; ok {
		t.Fatalf("non-MCP tool should not include gen_ai fields: %#v", fields)
	}
}

func TestToolFieldsDoesNotTreatGenericFlatTransportKeysAsMCP(t *testing.T) {
	fields := toolFieldsWithResponse("HTTPClient", map[string]interface{}{
		"method":           "GET",
		"protocol_version": "HTTP/2",
		"uri":              "https://example.com/report",
	}, map[string]interface{}{
		"status_code": "200",
	})
	if _, ok := fields["mcp"]; ok {
		t.Fatalf("non-MCP tool should not include mcp fields: %#v", fields)
	}
	if _, ok := fields["gen_ai"]; ok {
		t.Fatalf("non-MCP tool should not include gen_ai fields: %#v", fields)
	}
}

func TestToolFieldsDoesNotTreatGenericServerToolPairAsMCP(t *testing.T) {
	oldPlatform := platformFlag
	defer func() {
		platformFlag = oldPlatform
	}()
	platformFlag = "cursor"
	fields := toolFields("HTTPClient", map[string]interface{}{
		"server_name": "api",
		"tool_name":   "request",
	})
	if _, ok := fields["mcp"]; ok {
		t.Fatalf("non-MCP tool should not include mcp fields: %#v", fields)
	}
	if _, ok := fields["gen_ai"]; ok {
		t.Fatalf("non-MCP tool should not include gen_ai fields: %#v", fields)
	}
}

func TestToolFieldsMapsGenericFlatTransportKeysForKnownMCP(t *testing.T) {
	fields := toolFields("MCP:read_resource", map[string]interface{}{
		"method":           "resources/read",
		"protocol_version": "2025-06-18",
		"uri":              "file:///tmp/report.md",
	})
	mcp := fields["mcp"].(map[string]interface{})
	if got := mcp["method"].(map[string]interface{})["name"]; got != "resources/read" {
		t.Fatalf("mcp.method.name = %q, want resources/read", got)
	}
	if got := mcp["protocol"].(map[string]interface{})["version"]; got != "2025-06-18" {
		t.Fatalf("mcp.protocol.version = %q, want 2025-06-18", got)
	}
	if got := mcp["resource"].(map[string]interface{})["uri"]; got != "file:///tmp/report.md" {
		t.Fatalf("mcp.resource.uri = %q, want file URI", got)
	}
}

func TestMaybeEmitInventoryHeartbeatInvokesBeaconCLI(t *testing.T) {
	oldPlatform := platformFlag
	oldRunner := runInventoryHeartbeatCommand
	defer func() {
		platformFlag = oldPlatform
		runInventoryHeartbeatCommand = oldRunner
	}()
	platformFlag = "cursor"
	t.Setenv("BEACON_ENDPOINT_CLI", "/usr/local/bin/beacon")
	t.Setenv("BEACON_ENDPOINT_CONFIG", "/tmp/beacon/config.json")
	t.Setenv("BEACON_ENDPOINT_LOG", "/tmp/beacon/runtime.jsonl")

	var gotName string
	var gotArgs []string
	runInventoryHeartbeatCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = append([]string(nil), args...)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestInventoryHeartbeatCommandHelperProcess")
		cmd.Env = append(os.Environ(), "GO_WANT_INVENTORY_HELPER_PROCESS=1")
		return cmd
	}

	logger := logging.NewLoggerForPlatform("test", "cursor")
	maybeEmitInventoryHeartbeat(logger, map[string]interface{}{"cwd": "/repo"})

	if gotName != "/usr/local/bin/beacon" {
		t.Fatalf("command name = %q, want beacon path", gotName)
	}
	wantArgs := []string{
		"endpoint", "inventory", "heartbeat",
		"--trigger", "hook",
		"--trigger-harness", "cursor",
		"--config", "/tmp/beacon/config.json",
		"--log-path", "/tmp/beacon/runtime.jsonl",
		"--working-dir", "/repo",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestMaybeEmitInventoryHeartbeatInvokesBeaconCLIForAnyEndpointPlatform(t *testing.T) {
	oldPlatform := platformFlag
	oldRunner := runInventoryHeartbeatCommand
	defer func() {
		platformFlag = oldPlatform
		runInventoryHeartbeatCommand = oldRunner
	}()
	platformFlag = "gemini"
	t.Setenv("BEACON_ENDPOINT_CLI", "/usr/local/bin/beacon")
	t.Setenv("BEACON_ENDPOINT_LOG", "/tmp/beacon/runtime.jsonl")
	var gotArgs []string
	called := false
	runInventoryHeartbeatCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		called = true
		gotArgs = append([]string(nil), args...)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestInventoryHeartbeatCommandHelperProcess")
		cmd.Env = append(os.Environ(), "GO_WANT_INVENTORY_HELPER_PROCESS=1")
		return cmd
	}

	logger := logging.NewLoggerForPlatform("test", "gemini")
	maybeEmitInventoryHeartbeat(logger, map[string]interface{}{"cwd": "/repo"})

	if !called {
		t.Fatal("inventory heartbeat command should run for endpoint hook platforms")
	}
	wantArgs := []string{
		"endpoint", "inventory", "heartbeat",
		"--trigger", "hook",
		"--trigger-harness", "gemini",
		"--log-path", "/tmp/beacon/runtime.jsonl",
		"--working-dir", "/repo",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestMaybeEmitInventoryHeartbeatSkipsWithoutEndpointCLI(t *testing.T) {
	oldPlatform := platformFlag
	oldRunner := runInventoryHeartbeatCommand
	defer func() {
		platformFlag = oldPlatform
		runInventoryHeartbeatCommand = oldRunner
	}()
	platformFlag = "cursor"
	t.Setenv("BEACON_ENDPOINT_CLI", "")
	called := false
	runInventoryHeartbeatCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		called = true
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestInventoryHeartbeatCommandHelperProcess")
		cmd.Env = append(os.Environ(), "GO_WANT_INVENTORY_HELPER_PROCESS=1")
		return cmd
	}

	logger := logging.NewLoggerForPlatform("test", "cursor")
	maybeEmitInventoryHeartbeat(logger, map[string]interface{}{"cwd": "/repo"})

	if called {
		t.Fatal("inventory heartbeat command should not run without BEACON_ENDPOINT_CLI")
	}
}

func TestInventoryHeartbeatCommandHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_INVENTORY_HELPER_PROCESS") != "1" {
		return
	}
	os.Exit(0)
}
