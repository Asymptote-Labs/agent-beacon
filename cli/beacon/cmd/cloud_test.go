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
		{"cloud", "cursor", "print-setup"},
		{"cloud", "codex", "print-hooks"},
		{"cloud", "codex", "print-setup"},
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

func TestRenderCodexCloudHooks(t *testing.T) {
	got := renderCodexCloudHooks("/tmp/beacon/bin/beacon-hooks", "/tmp/beacon/runtime.jsonl")
	for _, want := range []string{
		`"SessionStart"`,
		`"UserPromptSubmit"`,
		`"Stop"`,
		`BEACON_ORIGIN=cloud`,
		`BEACON_RUN_PROVIDER=codex_cloud`,
		`BEACON_RUN_EPHEMERAL=true`,
		`BEACON_ENDPOINT_LOG=/tmp/beacon/runtime.jsonl`,
		`/tmp/beacon/bin/beacon-hooks --platform codex cloud-reset`,
		`/tmp/beacon/bin/beacon-hooks --platform codex codex-prompt-submit`,
		`/tmp/beacon/bin/beacon-hooks --platform codex cloud-upload`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered Codex hooks missing %q:\n%s", want, got)
		}
	}
	for _, skipped := range []string{`"PreToolUse"`, `"PostToolUse"`} {
		if strings.Contains(got, skipped) {
			t.Fatalf("rendered Codex hooks should not contain %q:\n%s", skipped, got)
		}
	}
}

func TestRenderCodexCloudSetupConfiguresCollectorAndPromptLogging(t *testing.T) {
	got := renderCodexCloudSetup("v0.0.60")
	for _, want := range []string{
		`BEACON_VERSION="v0.0.60"`,
		`chmod +x /tmp/beacon/bin/beacon /tmp/beacon/bin/beacon-hooks /tmp/beacon/bin/beacon-otelcol`,
		`/tmp/beacon/bin/beacon-otelcol --config /tmp/beacon/otelcol.yaml`,
		`path: "/tmp/beacon/runtime.jsonl"`,
		`beacon cloud codex print-hooks`,
		`exporter = "otlp-grpc"`,
		`log_user_prompt = true`,
		`environment = "cloud"`,
		`[otel.exporter."otlp-grpc"]`,
		`beacon.run.provider=${BEACON_RUN_PROVIDER:-codex_cloud}`,
		`. /tmp/beacon/codex-env.sh`,
		`beacon-hooks --platform codex cloud-watch`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered Codex setup missing %q:\n%s", want, got)
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
		`"postToolUse"`,
		`"postToolUseFailure"`,
		`"beforeShellExecution"`,
		`"afterShellExecution"`,
		`"beforeReadFile"`,
		`"afterFileEdit"`,
		`"subagentStart"`,
		`"subagentStop"`,
		`"preCompact"`,
		`BEACON_ORIGIN=cloud`,
		`BEACON_RUN_PROVIDER=cursor_cloud`,
		`'/tmp/beacon/bin/beacon-hooks' --platform cursor`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered hooks missing %q:\n%s", want, got)
		}
	}
	for _, unsupported := range []string{`"preToolUse"`, `"sessionStart"`, `"sessionEnd"`, `"beforeSubmitPrompt"`, `"stop"`} {
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
	if strings.Contains(got, `"preToolUse"`) {
		t.Fatalf("rendered safe hooks should not contain broad preToolUse:\n%s", got)
	}
}

func TestRenderCursorCloudSetupInstallsBinariesOnly(t *testing.T) {
	got := renderCursorCloudSetup("v0.0.50")
	for _, want := range []string{
		`BEACON_VERSION="v0.0.50"`,
		`tar -xzf "/tmp/beacon/${ARCHIVE}" -C /tmp/beacon/bin`,
		`Beacon binaries installed in /tmp/beacon/bin`,
		`beacon cloud cursor print-hooks --binary-path /tmp/beacon/bin/beacon-hooks --log-path /tmp/beacon/runtime.jsonl > .cursor/hooks.json`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered setup missing %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{`beacon cloud cursor install-hooks`, `--hooks-json`, `.git/info/exclude`} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("cursor setup should not rewrite project hooks with %q:\n%s", forbidden, got)
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
