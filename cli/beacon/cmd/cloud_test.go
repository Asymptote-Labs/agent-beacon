package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	"github.com/spf13/cobra"
)

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
		`"beforeSubmitPrompt"`,
		`prompt-submit`,
		`"beforeReadFile"`,
		`"afterFileEdit"`,
		`"subagentStart"`,
		`"subagentStop"`,
		`"preCompact"`,
		`"afterAgentThought"`,
		`agent-thought`,
		`"afterAgentResponse"`,
		`agent-response`,
		`"stop"`,
		`BEACON_ORIGIN=cloud`,
		`BEACON_RUN_PROVIDER=cursor_cloud`,
		`'/tmp/beacon/bin/beacon-hooks' --platform cursor`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered hooks missing %q:\n%s", want, got)
		}
	}
	for _, unsupported := range []string{`"preToolUse"`, `"sessionStart"`, `"sessionEnd"`} {
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
		`"beforeSubmitPrompt"`,
		`"beforeReadFile"`,
		`"postToolUse"`,
		`"subagentStart"`,
		`"preCompact"`,
		`"afterAgentThought"`,
		`"afterAgentResponse"`,
		`"stop"`,
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

func TestRenderCursorCloudSetupWritesExcludedHooks(t *testing.T) {
	got, err := renderCursorCloudSetup("v1.3.60", false)
	if err != nil {
		t.Fatalf("render cursor cloud setup: %v", err)
	}
	for _, want := range []string{
		`BEACON_VERSION="v1.3.60"`,
		`tar -xzf "/tmp/beacon/${ARCHIVE}" -C /tmp/beacon/bin beacon-hooks`,
		`cat > "$REPO_ROOT/.cursor/hooks.json" <<'BEACON_HOOKS'`,
		`"afterAgentThought"`,
		`BEACON_ORIGIN=cloud BEACON_RUN_PROVIDER=cursor_cloud`,
		`grep -qxF '.cursor/hooks.json' "$EXCLUDE" 2>/dev/null || echo '.cursor/hooks.json' >> "$EXCLUDE"`,
		// A repository's own committed hooks are never overwritten.
		`git -C "$REPO_ROOT" ls-files --error-unmatch .cursor/hooks.json`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered setup missing %q:\n%s", want, got)
		}
	}
	// The heredoc's terminator must stand on its own line, or the hooks file swallows the script.
	if !strings.Contains(got, "}\nBEACON_HOOKS\n") {
		t.Fatalf("hooks heredoc is not terminated on its own line:\n%s", got)
	}

	safe, err := renderCursorCloudSetup("v1.3.60", true)
	if err != nil {
		t.Fatalf("render safe cursor cloud setup: %v", err)
	}
	if strings.Contains(safe, `"beforeShellExecution"`) {
		t.Fatalf("--safe-hooks setup should leave out the shell hooks:\n%s", safe)
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

func TestS3SetupCommandsUseLeastPrivilegePolicy(t *testing.T) {
	commands, err := s3SetupCommands("bucket", "us-west-2", "agent-traces/customer=my-team", "beacon-uploader")
	if err != nil {
		t.Fatalf("s3SetupCommands returned error: %v", err)
	}
	joined := make([]string, 0, len(commands))
	for _, command := range commands {
		joined = append(joined, shellCommand(command...))
	}
	got := strings.Join(joined, "\n")
	for _, want := range []string{
		"aws s3api head-bucket --bucket bucket",
		"aws s3api create-bucket --bucket bucket --region us-west-2 --create-bucket-configuration LocationConstraint=us-west-2",
		"aws s3api put-public-access-block --bucket bucket --region us-west-2",
		"aws iam create-user --user-name beacon-uploader",
		"aws iam put-user-policy --user-name beacon-uploader --policy-name BeaconCloudTraceUpload",
		"aws iam create-access-key --user-name beacon-uploader --output json",
		`arn:aws:s3:::bucket/agent-traces/customer=my-team/*`,
		`"s3:PutObject"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("setup commands missing %q:\n%s", want, got)
		}
	}
}

func TestS3CreateBucketCommandOmitsLocationConstraintForUSEast1(t *testing.T) {
	got := shellCommand(s3CreateBucketCommand("bucket", "us-east-1")...)
	if strings.Contains(got, "LocationConstraint") {
		t.Fatalf("us-east-1 create-bucket should omit LocationConstraint: %s", got)
	}
}

func TestS3UploadPolicyScopesToPrefix(t *testing.T) {
	policyJSON, err := s3UploadPolicy("bucket", "/prefix/")
	if err != nil {
		t.Fatalf("s3UploadPolicy returned error: %v", err)
	}
	var policy struct {
		Statement []struct {
			Action   []string `json:"Action"`
			Resource string   `json:"Resource"`
		} `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(policyJSON), &policy); err != nil {
		t.Fatalf("policy is not JSON: %v", err)
	}
	if len(policy.Statement) != 1 {
		t.Fatalf("Statement length = %d, want 1", len(policy.Statement))
	}
	if got := strings.Join(policy.Statement[0].Action, ","); got != "s3:PutObject" {
		t.Fatalf("Action = %q, want s3:PutObject", got)
	}
	if policy.Statement[0].Resource != "arn:aws:s3:::bucket/prefix/*" {
		t.Fatalf("Resource = %q", policy.Statement[0].Resource)
	}
}

// The shuttle uploads only from /tmp/beacon, so a hook log elsewhere would never be shipped.
func TestCloudPrintHooksRejectsLogPathOutsideSandbox(t *testing.T) {
	prev := cloudOpts
	t.Cleanup(func() { cloudOpts = prev })
	for _, cmd := range []*cobra.Command{cloudClaudeWebPrintHooksCmd, cloudCursorPrintHooksCmd} {
		cloudOpts.binaryPath = "/tmp/beacon/bin/beacon-hooks"
		for _, logPath := range []string{"/workspace/.beacon/runtime.jsonl", "/tmp/beacon/../etc/runtime.jsonl", "/tmp/beacon"} {
			cloudOpts.logPath = logPath
			if err := cmd.RunE(cmd, nil); err == nil || !strings.Contains(err.Error(), "/tmp/beacon") {
				t.Fatalf("%s --log-path %s: err = %v, want a rejection naming /tmp/beacon", cmd.Parent().Name(), logPath, err)
			}
		}
	}
}
