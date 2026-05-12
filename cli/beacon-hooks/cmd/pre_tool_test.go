package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	hookconfig "github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/state"
)

func TestRunPreToolSecureByDesignGateBranches(t *testing.T) {
	tests := []struct {
		name       string
		enableSBD  bool
		sessionID  string
		inputGen   string
		storedGen  string
		policies   string
		injected   bool
		wantPerm   string
		wantInject bool
	}{
		{
			name:      "disabled allows",
			sessionID: "conv-disabled",
			policies:  "policy\n",
			wantPerm:  "allow",
		},
		{
			name:      "missing session allows",
			enableSBD: true,
			wantPerm:  "allow",
		},
		{
			name:      "no policies allows",
			enableSBD: true,
			sessionID: "conv-no-policies",
			wantPerm:  "allow",
		},
		{
			name:      "generation mismatch allows",
			enableSBD: true,
			sessionID: "conv-stale",
			inputGen:  "gen-2",
			storedGen: "gen-1",
			policies:  "policy\n",
			wantPerm:  "allow",
		},
		{
			name:      "already injected allows",
			enableSBD: true,
			sessionID: "conv-injected",
			inputGen:  "gen-1",
			storedGen: "gen-1",
			policies:  "policy\n",
			injected:  true,
			wantPerm:  "allow",
		},
		{
			name:       "first write denies and marks injected",
			enableSBD:  true,
			sessionID:  "conv-deny",
			inputGen:   "gen-1",
			storedGen:  "gen-1",
			policies:   "policy\n",
			wantPerm:   "deny",
			wantInject: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupHookConfigDirs(t)
			platformFlag = "cursor"
			if tt.enableSBD {
				writePlatformConfig(t, "cursor", `{"secure_by_design":true}`)
			}
			if tt.sessionID != "" && tt.policies != "" {
				st := state.NewSessionState(tt.sessionID, "cursor")
				st.SetSbdPolicies(tt.policies, tt.storedGen)
				if tt.injected {
					st.MarkSbdInjected()
				}
			}

			input := map[string]interface{}{}
			if tt.sessionID != "" {
				input["conversation_id"] = tt.sessionID
			}
			if tt.inputGen != "" {
				input["generation_id"] = tt.inputGen
			}
			out := runHookWithInput(t, runPreTool, input)

			if out["permission"] != tt.wantPerm {
				t.Fatalf("permission = %v, want %s; output=%#v", out["permission"], tt.wantPerm, out)
			}
			if tt.wantPerm == "deny" {
				message, _ := out["agent_message"].(string)
				if !strings.Contains(message, tt.policies) || !strings.Contains(message, "Retry your file write") {
					t.Fatalf("deny message missing policy context: %#v", out)
				}
			}
			if tt.sessionID != "" {
				_, _, injected := state.NewSessionState(tt.sessionID, "cursor").GetSbdState()
				if injected != (tt.injected || tt.wantInject) {
					t.Fatalf("injected = %v, want %v", injected, tt.injected || tt.wantInject)
				}
			}
		})
	}
}

func TestRunPromptSubmitClearsSbdPoliciesAndUsesCursorResponse(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	st := state.NewSessionState("conv-clear", "cursor")
	st.SetSbdPolicies("policy", "gen-1")

	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{"conversation_id": "conv-clear"})
	if out["continue"] != true {
		t.Fatalf("cursor prompt response = %#v, want continue=true", out)
	}
	policies, generationID, injected := st.GetSbdState()
	if policies != "" || generationID != "" || injected {
		t.Fatalf("SbD state was not cleared: policies=%q generation=%q injected=%v", policies, generationID, injected)
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

func writePlatformConfig(t *testing.T, platform, body string) {
	t.Helper()
	path := filepath.Join(hookconfig.GetStateDir(platform), "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir platform config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write platform config: %v", err)
	}
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
