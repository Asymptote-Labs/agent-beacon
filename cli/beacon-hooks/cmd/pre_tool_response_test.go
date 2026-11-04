package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

// The security property behind preToolResponse: an observing pre-tool hook never answers "allow"
// on a runtime that reads it as a pre-approval, because that would silently disarm the user's own
// confirmation prompts for every tool call on a runtime where Beacon was installed to watch.
//
// Cursor and goose are the two exceptions, each with a documented reason in preToolResponse and
// gooseBlockingEventResponse: neither has a non-opinion answer that lets a call proceed, and
// neither treats a hook's allow as overriding the operator's approval. Adding a runtime to that
// list is a security decision, which is why this test names the list rather than deriving it.
func TestPreToolResponseAllowsOnlyWhereAllowIsNotAPreApproval(t *testing.T) {
	restore := platformFlag
	t.Cleanup(func() { platformFlag = restore })

	allowed := map[string]bool{"cursor": true, goosePlatform: true}
	platforms := []string{
		"cursor", goosePlatform,
		"antigravity", "grok",
		"claude", "qwen", "devin", "devin-cli", "hermes", "vscode", "muse",
		openHandsPlatform, kiroPlatform, dshPlatform, kimiPlatform,
		// Reach pre-tool only through a hand-written config: no installer writes the hook.
		"codex", "copilot", "factory", "cline", "gemini",
		"", "some-future-runtime",
	}
	for _, platform := range platforms {
		t.Run(platform, func(t *testing.T) {
			platformFlag = platform
			response := preToolResponse()
			if allowed[platform] {
				if !responseCarriesAllow(response) {
					t.Fatalf("preToolResponse() = %#v; this runtime needs its allow to let the call proceed", response)
				}
				return
			}
			if responseCarriesAllow(response) {
				t.Fatalf("preToolResponse() = %#v, an observing hook must not approve on %q", response, platform)
			}
		})
	}
}

// Antigravity reads `{"decision":"allow"}` as "automatically allows the tool execution" and an
// object without a decision as a deny, so the only answer that leaves a call to its own permission
// settings is no output at all -- including when Beacon could not read the payload.
func TestAntigravityPreToolWritesNothing(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "antigravity"
	t.Setenv("BEACON_ENDPOINT_LOG", filepath.Join(t.TempDir(), "runtime.jsonl"))

	raw := runHookWithRawInput(t, runPreTool, mustJSON(t, map[string]interface{}{
		"conversationId": "ag-session",
		"workspacePaths": []interface{}{"/repo"},
		"toolCall": map[string]interface{}{
			"name": "run_command",
			"args": map[string]interface{}{"CommandLine": "rm -rf build", "Cwd": "/repo"},
		},
	}))
	if len(raw) != 0 {
		t.Fatalf("antigravity pre-tool wrote %q, want no output", raw)
	}

	raw = runHookWithRawInput(t, runPreTool, []byte(`{"toolCall": `))
	if len(raw) != 0 {
		t.Fatalf("antigravity pre-tool wrote %q on malformed input, want no output", raw)
	}
}

// The runtimes that reach the default answer -- Grok Build, and the ones whose pre-tool hook only a
// hand-written config installs -- get an object with no decision in it. They share one branch, so
// one runtime of each kind stands for the rest; preToolResponse's own test names every runtime.
func TestObservingPreToolDoesNotApproveOnDefaultRuntimes(t *testing.T) {
	for _, platform := range []string{"grok", "codex"} {
		t.Run(platform, func(t *testing.T) {
			setupHookConfigDirs(t)
			platformFlag = platform
			t.Setenv("BEACON_ENDPOINT_LOG", filepath.Join(t.TempDir(), "runtime.jsonl"))

			out := runHookWithInput(t, runPreTool, map[string]interface{}{
				"session_id": platform + "-session",
				"tool_name":  "Bash",
				"tool_input": map[string]interface{}{"command": "rm -rf build"},
			})
			if out == nil || len(out) != 0 {
				t.Fatalf("pre-tool response = %#v, want an empty object carrying no decision", out)
			}

			out = runHookWithInput(t, runPreTool, map[string]interface{}{})
			if out == nil || len(out) != 0 {
				t.Fatalf("pre-tool response on an empty payload = %#v, want an empty object", out)
			}
		})
	}
}

// The policy seam keeps each runtime's native deny shape: taking allow out of the observing answer
// must not take the deny out of the enforcing one.
func TestPreToolPolicyDenyAntigravityAndGrok(t *testing.T) {
	for _, platform := range []string{"antigravity", "grok"} {
		t.Run(platform, func(t *testing.T) {
			logPath := setupPolicyTest(t, platform, denyResponse)
			out := runHookWithInput(t, runPreTool, map[string]interface{}{
				"session_id": "s-" + platform,
				"tool_name":  "run_command",
				"tool_input": map[string]interface{}{"command": "claude --dangerously-skip-permissions -p go"},
			})
			if out["decision"] != "deny" {
				t.Fatalf("%s deny response = %#v, want decision=deny", platform, out)
			}
			assertDenialEvent(t, logPath)
		})
	}
}

// A provider that allows is not grounds for Beacon to approve on the runtime's terms: the seam only
// ever adds a deny, and an allowed call gets the observing answer like any other.
func TestPreToolPolicyAllowIsNotTranslatedIntoAnApproval(t *testing.T) {
	for _, platform := range []string{"antigravity", "grok"} {
		t.Run(platform, func(t *testing.T) {
			setupPolicyTest(t, platform, `{"decision":"allow"}`)
			raw := runHookWithRawInput(t, runPreTool, mustJSON(t, map[string]interface{}{
				"session_id": "s-" + platform,
				"tool_name":  "run_command",
				"tool_input": map[string]interface{}{"command": "ls"},
			}))
			if bytes.Contains(raw, []byte("allow")) {
				t.Fatalf("%s pre-tool wrote %q after a provider allow, want no approval", platform, raw)
			}
		})
	}
}

func responseCarriesAllow(response map[string]interface{}) bool {
	for _, value := range response {
		if s, ok := value.(string); ok && (s == "allow" || s == "approve") {
			return true
		}
	}
	return false
}

func mustJSON(t *testing.T, value interface{}) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

// runHookWithRawInput feeds stdin bytes as given and returns stdout as written, so a test can tell
// no output from an empty object and can send a payload that is not JSON. The payloads here are
// small, so writing before the hook runs fits in any pipe buffer.
func runHookWithRawInput(t testing.TB, run func(cmd *cobra.Command, args []string), stdin []byte) []byte {
	t.Helper()
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	origStdin, origStdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = stdinR, stdoutW
	defer func() {
		os.Stdin, os.Stdout = origStdin, origStdout
		_ = stdinR.Close()
		_ = stdoutR.Close()
	}()

	if _, err := stdinW.Write(stdin); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if err := stdinW.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}
	done := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(stdoutR)
		done <- data
	}()

	run(nil, nil)

	if err := stdoutW.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	return <-done
}
