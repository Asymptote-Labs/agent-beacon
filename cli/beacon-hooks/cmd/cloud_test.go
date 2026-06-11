package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCloudCommandsRegistered(t *testing.T) {
	for _, name := range []string{"cloud-reset", "cloud-upload", "cloud-watch", "codex-prompt-submit"} {
		cmd, _, err := rootCmd.Find([]string{name})
		if err != nil {
			t.Fatalf("Find %s returned error: %v", name, err)
		}
		if cmd == nil || cmd.Use != name {
			t.Fatalf("command %s not registered: %#v", name, cmd)
		}
	}
}

func TestRunCloudResetRemovesRuntimeFiles(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "codex"
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	statePath := filepath.Join(dir, "state.json")
	for _, path := range []string{logPath, logPath + ".lock", statePath} {
		if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	t.Setenv("BEACON_ORIGIN", "cloud")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CLOUD_SHUTTLE_STATE", statePath)

	out := runHookWithInput(t, runCloudReset, map[string]interface{}{"session_id": "codex-session"})
	if len(out) != 0 {
		t.Fatalf("cloud-reset response = %#v, want empty", out)
	}
	for _, path := range []string{logPath, logPath + ".lock", statePath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists, stat err=%v", path, err)
		}
	}
}

func TestRunCodexPromptSubmitEmitsCloudPromptEvent(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "codex"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_ORIGIN", "cloud")
	t.Setenv("BEACON_RUN_PROVIDER", "codex_cloud")
	t.Setenv("BEACON_RUN_EPHEMERAL", "true")

	out := runHookWithInput(t, runCodexPromptSubmit, map[string]interface{}{
		"session_id": "codex-session",
		"cwd":        "/repo",
		"prompt":     "summarize token=codex-secret",
		"model":      "gpt-5.1-codex",
	})
	if len(out) != 0 {
		t.Fatalf("codex-prompt-submit response = %#v, want empty", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "prompt.submitted" {
		t.Fatalf("event.action = %q, want prompt.submitted", action)
	}
	if provider := event["run"].(map[string]interface{})["provider"]; provider != "codex_cloud" {
		t.Fatalf("run.provider = %q, want codex_cloud", provider)
	}
	if runID := event["run"].(map[string]interface{})["run_id"]; runID != "codex-session" {
		t.Fatalf("run.run_id = %q, want codex-session", runID)
	}
	if prompt := event["prompt"].(map[string]interface{})["text"]; prompt != "summarize token=[REDACTED]" {
		t.Fatalf("prompt.text = %q, want redacted prompt", prompt)
	}
}

func TestRunCodexPreToolObservesWithoutDecisionResponse(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "codex"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_ORIGIN", "cloud")
	t.Setenv("BEACON_RUN_PROVIDER", "codex_cloud")
	t.Setenv("BEACON_RUN_EPHEMERAL", "true")

	out := runHookWithInput(t, runPreTool, map[string]interface{}{
		"session_id": "codex-session",
		"tool_name":  "Bash",
		"tool_input": map[string]interface{}{
			"command": "pwd && ls",
		},
	})
	if len(out) != 0 {
		t.Fatalf("codex pre-tool response = %#v, want empty", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "approval.allowed" {
		t.Fatalf("event.action = %q, want approval.allowed", action)
	}
	if command := event["command"].(map[string]interface{})["command"]; command != "pwd && ls" {
		t.Fatalf("command = %q, want pwd && ls", command)
	}
}
