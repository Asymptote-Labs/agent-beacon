package cmd

import (
	"strings"
	"testing"
)

func TestCloudCommandsRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"cloud", "claude-web", "print-hooks"},
		{"cloud", "claude-web", "print-setup"},
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

func TestRenderClaudeWebHooks(t *testing.T) {
	got := renderClaudeWebHooks("/tmp/beacon/bin/beacon-hooks", "/tmp/beacon/runtime.jsonl")
	for _, want := range []string{
		`"SessionStart"`,
		`"UserPromptSubmit"`,
		`"PreToolUse"`,
		`"PostToolUse"`,
		`"PostToolUseFailure"`,
		`"Stop"`,
		`"SessionEnd"`,
		`"SubagentStart"`,
		`"SubagentStop"`,
		`"PermissionRequest"`,
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
	if strings.Contains(got, `/tmp/beacon/logs`) {
		t.Fatalf("rendered setup should not reference unused logs directory:\n%s", got)
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
