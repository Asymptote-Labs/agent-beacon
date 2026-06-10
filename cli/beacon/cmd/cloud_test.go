package cmd

import (
	"strings"
	"testing"

	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
)

func TestCloudCommandsRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"cloud", "claude-web", "print-hooks"},
		{"cloud", "claude-web", "print-setup"},
		{"cloud", "cursor", "print-hooks"},
		{"cloud", "cursor", "install-hooks"},
		{"cloud", "cursor", "print-setup"},
		{"cloud", "gcs", "setup"},
	} {
		cmd, _, err := rootCmd.Find(path)
		if err != nil {
			t.Fatalf("Find %v returned error: %v", path, err)
		}
		if cmd == nil || cmd.Use != path[len(path)-1] {
			t.Fatalf("cloud command %v not registered: %#v", path, cmd)
		}
	}
}

func TestRenderCursorCloudHooks(t *testing.T) {
	got, err := endpointhooks.RenderCursorCloudHooks(endpointhooks.CursorCloudOptions{
		BinaryPath: "/tmp/beacon/bin/beacon-hooks",
		LogPath:    "/tmp/beacon/runtime.jsonl",
	})
	if err != nil {
		t.Fatalf("render cursor cloud hooks: %v", err)
	}
	for _, want := range []string{
		`"version": 1`,
		`"preToolUse"`,
		`"postToolUse"`,
		`"postToolUseFailure"`,
		`"beforeShellExecution"`,
		`"afterShellExecution"`,
		`"beforeReadFile"`,
		`"afterFileEdit"`,
		`"subagentStart"`,
		`"subagentStop"`,
		`"preCompact"`,
		`"matcher": "Write|Edit|MultiEdit|Delete|Grep|Glob|MCP|Task"`,
		`BEACON_ORIGIN=cloud`,
		`BEACON_RUN_PROVIDER=cursor_cloud`,
		`'/tmp/beacon/bin/beacon-hooks' --platform cursor`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered hooks missing %q:\n%s", want, got)
		}
	}
	for _, unsupported := range []string{`"sessionStart"`, `"sessionEnd"`, `"beforeSubmitPrompt"`, `"stop"`} {
		if strings.Contains(got, unsupported) {
			t.Fatalf("rendered cursor cloud hooks should not contain %q:\n%s", unsupported, got)
		}
	}
}

func TestRenderCursorCloudSafeHooks(t *testing.T) {
	got, err := endpointhooks.RenderCursorCloudHooks(endpointhooks.CursorCloudOptions{
		BinaryPath: "/tmp/beacon/bin/beacon-hooks",
		LogPath:    "/tmp/beacon/runtime.jsonl",
		SafeHooks:  true,
	})
	if err != nil {
		t.Fatalf("render cursor cloud hooks: %v", err)
	}
	for _, want := range []string{
		`"beforeReadFile"`,
		`"preToolUse"`,
		`"postToolUse"`,
		`"subagentStart"`,
		`"preCompact"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered safe hooks missing %q:\n%s", want, got)
		}
	}
	for _, skipped := range []string{`"beforeShellExecution"`, `"afterShellExecution"`, `"afterFileEdit"`} {
		if strings.Contains(got, skipped) {
			t.Fatalf("rendered safe hooks should not contain %q:\n%s", skipped, got)
		}
	}
}

func TestRenderCursorCloudSetupInstallsHooks(t *testing.T) {
	got := renderCursorCloudSetup("v0.0.50")
	for _, want := range []string{
		`BEACON_VERSION="v0.0.50"`,
		`REPO_ROOT="${BEACON_CLOUD_REPO_DIR:-${CURSOR_PROJECT_DIR:-}}"`,
		`.cursor/hooks.json`,
		`beacon cloud cursor install-hooks`,
		`--hooks-json "$REPO_ROOT/.cursor/hooks.json"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered setup missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `> "$REPO_ROOT/.cursor/hooks.json"`) {
		t.Fatalf("cursor setup should merge hooks instead of overwriting hooks.json:\n%s", got)
	}
}

func TestRenderClaudeWebHooks(t *testing.T) {
	got := renderClaudeWebHooks("/tmp/beacon/bin/beacon-hooks", "/tmp/beacon/runtime.jsonl")
	for _, want := range []string{
		`"SessionStart"`,
		`"UserPromptSubmit"`,
		`"PreToolUse"`,
		`"PostToolUse"`,
		`"Stop"`,
		`"SessionEnd"`,
		`BEACON_ENDPOINT_MODE=1`,
		`BEACON_ENDPOINT_LOG=/tmp/beacon/runtime.jsonl`,
		`/tmp/beacon/bin/beacon-hooks --platform claude`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered hooks missing %q:\n%s", want, got)
		}
	}
}

func TestRenderClaudeWebSetupUsesLocalSettings(t *testing.T) {
	got := renderClaudeWebSetup("v0.0.50")
	for _, want := range []string{
		`BEACON_VERSION="v0.0.50"`,
		`REPO_ROOT="${BEACON_CLOUD_REPO_DIR:-}"`,
		`find /home/user`,
		`.claude/settings.local.json`,
		`.git/info/exclude`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered setup missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `> .claude/settings.json`) {
		t.Fatalf("rendered setup should not write settings.json:\n%s", got)
	}
}

func TestGCSSetupCommands(t *testing.T) {
	commands := gcsSetupCommands("asymptote-code", "bucket", "us-central1", "uploader@asymptote-code.iam.gserviceaccount.com", "uploader")
	got := shellCommand(commands[len(commands)-1]...)
	for _, want := range []string{
		"gcloud storage buckets add-iam-policy-binding gs://bucket",
		"serviceAccount:uploader@asymptote-code.iam.gserviceaccount.com",
		"roles/storage.objectUser",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("setup command missing %q: %s", want, got)
		}
	}
}

func TestGCSSetupClassifiesNotFoundOutput(t *testing.T) {
	for _, text := range []string{
		"ERROR: bucket not found",
		"HTTPError 400: Service account example does not exist.",
		"One or more URLs matched no objects.",
	} {
		if !isNotFoundOutput(text) {
			t.Fatalf("isNotFoundOutput(%q) = false, want true", text)
		}
	}
	if isNotFoundOutput("ERROR: permission denied") {
		t.Fatal("permission denied should not be treated as not found")
	}
}
