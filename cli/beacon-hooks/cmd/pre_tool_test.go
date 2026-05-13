package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	hookconfig "github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/state"
)

func TestRunPreToolAllowsAndEmitsTelemetry(t *testing.T) {
	tests := []struct {
		name      string
		sessionID string
	}{
		{
			name: "missing session allows",
		},
		{
			name:      "session allows",
			sessionID: "conv-observe",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupHookConfigDirs(t)
			platformFlag = "cursor"

			input := map[string]interface{}{}
			if tt.sessionID != "" {
				input["conversation_id"] = tt.sessionID
			}
			out := runHookWithInput(t, runPreTool, input)

			if out["permission"] != "allow" {
				t.Fatalf("permission = %v, want allow; output=%#v", out["permission"], out)
			}
		})
	}
}

func TestRunPromptSubmitUsesCursorResponse(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"

	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{"conversation_id": "conv-submit"})
	if out["continue"] != true {
		t.Fatalf("cursor prompt response = %#v, want continue=true", out)
	}
}

func TestRunSessionStartStoresModel(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"

	out := runHookWithInput(t, runSessionStart, map[string]interface{}{
		"conversation_id": "conv-model",
		"model":           "gpt-5.5",
	})
	if len(out) != 0 {
		t.Fatalf("session-start response = %#v, want empty response", out)
	}
	if got := state.NewSessionState("conv-model", "cursor").GetModel(); got != "gpt-5.5" {
		t.Fatalf("stored model = %q, want gpt-5.5", got)
	}
}

func TestRunSessionEndRemovesSessionLog(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logFile := hookconfig.GetSessionLogFile("cursor", "conv-end")
	if err := os.MkdirAll(filepath.Dir(logFile), 0755); err != nil {
		t.Fatalf("mkdir session log dir: %v", err)
	}
	if err := os.WriteFile(logFile, []byte("session log"), 0644); err != nil {
		t.Fatalf("write session log: %v", err)
	}

	out := runHookWithInput(t, runSessionEnd, map[string]interface{}{"conversation_id": "conv-end"})
	if len(out) != 0 {
		t.Fatalf("session-end response = %#v, want empty response", out)
	}
	if _, err := os.Stat(logFile); !os.IsNotExist(err) {
		t.Fatalf("session log still exists or unexpected error: %v", err)
	}
}

func setupHookConfigDirs(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	origBeaconDir := hookconfig.BeaconDir
	origClaudeDir := hookconfig.ClaudeDir
	origCopilotDir := hookconfig.CopilotDir
	origCursorDir := hookconfig.CursorDir
	origFactoryDir := hookconfig.FactoryDir
	origPlatform := platformFlag
	hookconfig.BeaconDir = tmp
	hookconfig.ClaudeDir = filepath.Join(tmp, "claude")
	hookconfig.CopilotDir = filepath.Join(tmp, "copilot")
	hookconfig.CursorDir = filepath.Join(tmp, "cursor")
	hookconfig.FactoryDir = filepath.Join(tmp, "factory")
	t.Cleanup(func() {
		hookconfig.BeaconDir = origBeaconDir
		hookconfig.ClaudeDir = origClaudeDir
		hookconfig.CopilotDir = origCopilotDir
		hookconfig.CursorDir = origCursorDir
		hookconfig.FactoryDir = origFactoryDir
		platformFlag = origPlatform
	})
}

func runHookWithInput(t *testing.T, run func(cmd *cobra.Command, args []string), input map[string]interface{}) map[string]interface{} {
	t.Helper()
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	origStdin := os.Stdin
	origStdout := os.Stdout
	os.Stdin = stdinR
	os.Stdout = stdoutW
	defer func() {
		os.Stdin = origStdin
		os.Stdout = origStdout
		_ = stdinR.Close()
		_ = stdoutR.Close()
	}()

	if err := json.NewEncoder(stdinW).Encode(input); err != nil {
		t.Fatalf("encode input: %v", err)
	}
	if err := stdinW.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}

	run(nil, nil)

	if err := stdoutW.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	os.Stdin = origStdin
	os.Stdout = origStdout
	var out map[string]interface{}
	if err := json.NewDecoder(stdoutR).Decode(&out); err != nil {
		t.Fatalf("decode hook output: %v", err)
	}
	return out
}
