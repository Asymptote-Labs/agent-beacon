package cmd

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	"github.com/spf13/cobra"
)

var cloudOpts struct {
	binaryPath     string
	logPath        string
	version        string
	project        string
	bucket         string
	location       string
	prefix         string
	serviceAccount string
	printOnly      bool
	apply          bool
	printEnv       bool
	safeHooks      bool
}

var cloudCmd = &cobra.Command{
	Use:   "cloud",
	Short: "Configure Beacon telemetry for provider-managed cloud agents",
}

var cloudClaudeWebCmd = &cobra.Command{
	Use:   "claude-web",
	Short: "Generate Claude Code on the web telemetry setup",
}

var cloudCursorCmd = &cobra.Command{
	Use:   "cursor",
	Short: "Generate Cursor cloud agent telemetry setup",
}

var cloudCodexCmd = &cobra.Command{
	Use:   "codex",
	Short: "Generate Codex cloud agent telemetry setup",
}

var cloudClaudeWebPrintHooksCmd = &cobra.Command{
	Use:          "print-hooks",
	Short:        "Print project-level Claude hook settings for a cloud sandbox",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(cloudOpts.binaryPath) == "" {
			return fmt.Errorf("--binary-path is required")
		}
		if strings.TrimSpace(cloudOpts.logPath) == "" {
			cloudOpts.logPath = "/tmp/beacon/runtime.jsonl"
		}
		fmt.Print(renderClaudeWebHooks(cloudOpts.binaryPath, cloudOpts.logPath))
		return nil
	},
}

var cloudClaudeWebPrintSetupCmd = &cobra.Command{
	Use:          "print-setup",
	Short:        "Print a Claude Code web environment setup script",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(cloudOpts.version) == "" {
			return fmt.Errorf("--version is required")
		}
		fmt.Print(renderClaudeWebSetup(cloudOpts.version))
		return nil
	},
}

var cloudCursorPrintHooksCmd = &cobra.Command{
	Use:          "print-hooks",
	Short:        "Print project-level Cursor hook settings for a cloud sandbox",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(cloudOpts.binaryPath) == "" {
			return fmt.Errorf("--binary-path is required")
		}
		rendered, err := endpointhooks.RenderCursorCloudHooks(endpointhooks.CursorCloudOptions{
			BinaryPath: cloudOpts.binaryPath,
			LogPath:    defaultCloudLogPath(cloudOpts.logPath),
			SafeHooks:  cloudOpts.safeHooks,
		})
		if err != nil {
			return err
		}
		fmt.Print(rendered)
		return nil
	},
}

var cloudCursorPrintSetupCmd = &cobra.Command{
	Use:          "print-setup",
	Short:        "Print a Cursor cloud agent binary setup script",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(cloudOpts.version) == "" {
			return fmt.Errorf("--version is required")
		}
		fmt.Print(renderCursorCloudSetup(cloudOpts.version))
		return nil
	},
}

var cloudCodexPrintHooksCmd = &cobra.Command{
	Use:          "print-hooks",
	Short:        "Print user-level Codex hook settings for a cloud sandbox",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(cloudOpts.binaryPath) == "" {
			return fmt.Errorf("--binary-path is required")
		}
		fmt.Print(renderCodexCloudHooks(cloudOpts.binaryPath, defaultCloudLogPath(cloudOpts.logPath)))
		return nil
	},
}

var cloudCodexPrintSetupCmd = &cobra.Command{
	Use:          "print-setup",
	Short:        "Print a Codex cloud agent telemetry setup script",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(cloudOpts.version) == "" {
			return fmt.Errorf("--version is required")
		}
		fmt.Print(renderCodexCloudSetup(cloudOpts.version))
		return nil
	},
}

var cloudGCSCmd = &cobra.Command{
	Use:   "gcs",
	Short: "Configure GCS forwarding for cloud agent telemetry",
}

var cloudGCSSetupCmd = &cobra.Command{
	Use:          "setup",
	Short:        "Create or print self-serve GCS setup for cloud agent telemetry",
	SilenceUsage: true,
	RunE:         runCloudGCSSetup,
}

func init() {
	rootCmd.AddCommand(cloudCmd)
	cloudCmd.AddCommand(cloudClaudeWebCmd)
	cloudCmd.AddCommand(cloudCursorCmd)
	cloudCmd.AddCommand(cloudCodexCmd)
	cloudCmd.AddCommand(cloudGCSCmd)
	cloudClaudeWebCmd.AddCommand(cloudClaudeWebPrintHooksCmd)
	cloudClaudeWebCmd.AddCommand(cloudClaudeWebPrintSetupCmd)
	cloudCursorCmd.AddCommand(cloudCursorPrintHooksCmd)
	cloudCursorCmd.AddCommand(cloudCursorPrintSetupCmd)
	cloudCodexCmd.AddCommand(cloudCodexPrintHooksCmd)
	cloudCodexCmd.AddCommand(cloudCodexPrintSetupCmd)
	cloudGCSCmd.AddCommand(cloudGCSSetupCmd)

	cloudClaudeWebPrintHooksCmd.Flags().StringVar(&cloudOpts.binaryPath, "binary-path", "", "Path to beacon-hooks inside the cloud sandbox")
	cloudClaudeWebPrintHooksCmd.Flags().StringVar(&cloudOpts.logPath, "log-path", "/tmp/beacon/runtime.jsonl", "Cloud sandbox runtime JSONL path")
	cloudClaudeWebPrintSetupCmd.Flags().StringVar(&cloudOpts.version, "version", "", "Beacon release tag to download, such as v0.0.50")
	cloudCursorPrintHooksCmd.Flags().StringVar(&cloudOpts.binaryPath, "binary-path", "", "Path to beacon-hooks inside the cloud sandbox")
	cloudCursorPrintHooksCmd.Flags().StringVar(&cloudOpts.logPath, "log-path", "/tmp/beacon/runtime.jsonl", "Cloud sandbox runtime JSONL path")
	cloudCursorPrintHooksCmd.Flags().BoolVar(&cloudOpts.safeHooks, "safe-hooks", false, "Skip Cursor cloud shell/edit-specific hooks while testing telemetry forwarding")
	cloudCursorPrintSetupCmd.Flags().StringVar(&cloudOpts.version, "version", "", "Beacon release tag to download, such as v0.0.50")
	cloudCodexPrintHooksCmd.Flags().StringVar(&cloudOpts.binaryPath, "binary-path", "", "Path to beacon-hooks inside the cloud sandbox")
	cloudCodexPrintHooksCmd.Flags().StringVar(&cloudOpts.logPath, "log-path", "/tmp/beacon/runtime.jsonl", "Cloud sandbox runtime JSONL path")
	cloudCodexPrintSetupCmd.Flags().StringVar(&cloudOpts.version, "version", "", "Beacon release tag to download, such as v0.0.50")

	cloudGCSSetupCmd.Flags().StringVar(&cloudOpts.project, "project", "", "Google Cloud project ID")
	cloudGCSSetupCmd.Flags().StringVar(&cloudOpts.bucket, "bucket", "", "GCS bucket for cloud agent telemetry")
	cloudGCSSetupCmd.Flags().StringVar(&cloudOpts.location, "location", "us-central1", "GCS bucket location when creating a bucket")
	cloudGCSSetupCmd.Flags().StringVar(&cloudOpts.prefix, "prefix", "agent-traces", "GCS object prefix for cloud telemetry")
	cloudGCSSetupCmd.Flags().StringVar(&cloudOpts.serviceAccount, "service-account", "beacon-cloud-trace-uploader", "Uploader service account ID or email")
	cloudGCSSetupCmd.Flags().BoolVar(&cloudOpts.printOnly, "print", false, "Print the gcloud commands without running them")
	cloudGCSSetupCmd.Flags().BoolVar(&cloudOpts.apply, "apply", false, "Run the gcloud setup commands")
	cloudGCSSetupCmd.Flags().BoolVar(&cloudOpts.printEnv, "print-env", false, "Print Claude web environment variables after setup")
}

func defaultCloudLogPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return "/tmp/beacon/runtime.jsonl"
	}
	return path
}

func renderClaudeWebHooks(binaryPath, logPath string) string {
	prefix := fmt.Sprintf("BEACON_ENDPOINT_MODE=1 BEACON_ENDPOINT_LOG=%s %s --platform claude", shellQuote(logPath), shellQuote(binaryPath))
	hooks := map[string][]map[string]interface{}{
		"SessionStart": {
			{"hooks": []map[string]interface{}{{"type": "command", "command": prefix + " session-start"}}},
		},
		"UserPromptSubmit": {
			{"hooks": []map[string]interface{}{{"type": "command", "command": prefix + " prompt-submit", "timeout": 30}}},
		},
		"PreToolUse": {
			{"matcher": "Bash|Edit|Write|MultiEdit|Read|Glob|Grep|WebFetch|WebSearch|Agent|mcp__.*", "hooks": []map[string]interface{}{{"type": "command", "command": prefix + " pre-tool"}}},
		},
		"PostToolUse": {
			{"matcher": "*", "hooks": []map[string]interface{}{{"type": "command", "command": prefix + " post-tool"}}},
		},
		"PostToolUseFailure": {
			{"matcher": "*", "hooks": []map[string]interface{}{{"type": "command", "command": prefix + " post-tool"}}},
		},
		"Stop": {
			{"hooks": []map[string]interface{}{{"type": "command", "command": prefix + " stop", "timeout": 45}}},
		},
		"SubagentStart": {
			{"hooks": []map[string]interface{}{{"type": "command", "command": prefix + " subagent-start"}}},
		},
		"SubagentStop": {
			{"hooks": []map[string]interface{}{{"type": "command", "command": prefix + " subagent-stop"}}},
		},
		"PermissionRequest": {
			{"matcher": "*", "hooks": []map[string]interface{}{{"type": "command", "command": prefix + " permission-request"}}},
		},
		"SessionEnd": {
			{"hooks": []map[string]interface{}{{"type": "command", "command": prefix + " session-end"}}},
		},
	}
	out := map[string]interface{}{"hooks": hooks}
	data, _ := json.MarshalIndent(out, "", "  ")
	return string(data) + "\n"
}

func renderCodexCloudHooks(binaryPath, logPath string) string {
	prefix := fmt.Sprintf(
		"BEACON_ENDPOINT_MODE=1 BEACON_ORIGIN=cloud BEACON_RUN_PROVIDER=codex_cloud BEACON_RUN_EPHEMERAL=true BEACON_ENDPOINT_LOG=%s %s --platform codex",
		shellQuote(logPath),
		shellQuote(binaryPath),
	)
	hooks := map[string][]map[string]interface{}{
		"SessionStart": {
			{"matcher": "startup|resume|clear|compact", "hooks": []map[string]interface{}{{"type": "command", "command": prefix + " cloud-reset", "timeout": 30}}},
		},
		"PreToolUse": {
			{"matcher": "*", "hooks": []map[string]interface{}{{"type": "command", "command": prefix + " pre-tool", "timeout": 30}}},
		},
		"PermissionRequest": {
			{"matcher": "*", "hooks": []map[string]interface{}{{"type": "command", "command": prefix + " permission-request", "timeout": 30}}},
		},
		"PostToolUse": {
			{"matcher": "*", "hooks": []map[string]interface{}{{"type": "command", "command": prefix + " post-tool", "timeout": 30}}},
		},
		"UserPromptSubmit": {
			{"hooks": []map[string]interface{}{{"type": "command", "command": prefix + " codex-prompt-submit", "timeout": 30}}},
		},
		"Stop": {
			{"hooks": []map[string]interface{}{{"type": "command", "command": prefix + " cloud-upload", "timeout": 45}}},
		},
	}
	out := map[string]interface{}{"hooks": hooks}
	data, _ := json.MarshalIndent(out, "", "  ")
	return string(data) + "\n"
}

func renderClaudeWebSetup(version string) string {
	return fmt.Sprintf(`set -euo pipefail
mkdir -p /tmp/beacon/bin /tmp/beacon/logs

BEACON_VERSION=%q
OS="linux"
case "$(uname -m)" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "unsupported arch $(uname -m)" >&2; exit 1 ;;
esac

ARCHIVE="beacon_${BEACON_VERSION#v}_${OS}_${ARCH}.tar.gz"
BASE="https://github.com/asymptote-labs/agent-beacon/releases/download/${BEACON_VERSION}"
curl -fsSL "${BASE}/${ARCHIVE}" -o "/tmp/beacon/${ARCHIVE}"
tar -xzf "/tmp/beacon/${ARCHIVE}" -C /tmp/beacon/bin
chmod +x /tmp/beacon/bin/beacon /tmp/beacon/bin/beacon-hooks 2>/dev/null || true

REPO_ROOT="${BEACON_CLOUD_REPO_DIR:-}"
if [ -z "$REPO_ROOT" ]; then
  REPO_GIT_DIR="$(find /home/user -mindepth 2 -maxdepth 3 -type d -name .git -print -quit 2>/dev/null || true)"
  if [ -n "$REPO_GIT_DIR" ]; then
    REPO_ROOT="$(dirname "$REPO_GIT_DIR")"
  fi
fi
if [ -z "$REPO_ROOT" ] || [ ! -d "$REPO_ROOT" ]; then
  echo "Could not find Claude web repo root under /home/user" >&2
  exit 1
fi

mkdir -p "$REPO_ROOT/.claude"
cat >> "$REPO_ROOT/.git/info/exclude" <<'EOF'
.claude/settings.local.json
.claude/settings.json
EOF
/tmp/beacon/bin/beacon cloud claude-web print-hooks \
  --binary-path /tmp/beacon/bin/beacon-hooks \
  --log-path /tmp/beacon/runtime.jsonl > "$REPO_ROOT/.claude/settings.local.json"

echo "Beacon hooks installed at $REPO_ROOT/.claude/settings.local.json"
`, version)
}

func renderCursorCloudSetup(version string) string {
	return fmt.Sprintf(`set -euo pipefail
mkdir -p /tmp/beacon/bin /tmp/beacon/logs

BEACON_VERSION=%q
OS="linux"
case "$(uname -m)" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "unsupported arch $(uname -m)" >&2; exit 1 ;;
esac

ARCHIVE="beacon_${BEACON_VERSION#v}_${OS}_${ARCH}.tar.gz"
BASE="https://github.com/asymptote-labs/agent-beacon/releases/download/${BEACON_VERSION}"
curl -fsSL "${BASE}/${ARCHIVE}" -o "/tmp/beacon/${ARCHIVE}"
tar -xzf "/tmp/beacon/${ARCHIVE}" -C /tmp/beacon/bin
chmod +x /tmp/beacon/bin/beacon /tmp/beacon/bin/beacon-hooks 2>/dev/null || true

echo "Beacon binaries installed in /tmp/beacon/bin"
echo "Commit a project-level .cursor/hooks.json before starting Cursor Cloud Agents:"
echo "  beacon cloud cursor print-hooks --binary-path /tmp/beacon/bin/beacon-hooks --log-path /tmp/beacon/runtime.jsonl > .cursor/hooks.json"
`, version)
}

func renderCodexCloudSetup(version string) string {
	return fmt.Sprintf(`set -euo pipefail
CODEX_CONFIG_DIR="${CODEX_HOME:-$HOME/.codex}"
mkdir -p /tmp/beacon/bin /tmp/beacon/logs "$CODEX_CONFIG_DIR"

BEACON_VERSION=%q
OS="linux"
case "$(uname -m)" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "unsupported arch $(uname -m)" >&2; exit 1 ;;
esac

ARCHIVE="beacon_${BEACON_VERSION#v}_${OS}_${ARCH}.tar.gz"
BASE="https://github.com/asymptote-labs/agent-beacon/releases/download/${BEACON_VERSION}"
curl -fsSL "${BASE}/${ARCHIVE}" -o "/tmp/beacon/${ARCHIVE}"
tar -xzf "/tmp/beacon/${ARCHIVE}" -C /tmp/beacon/bin
chmod +x /tmp/beacon/bin/beacon /tmp/beacon/bin/beacon-hooks /tmp/beacon/bin/beacon-otelcol 2>/dev/null || true

cat > /tmp/beacon/otelcol.yaml <<'EOF'
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 127.0.0.1:4317
      http:
        endpoint: 127.0.0.1:4318

processors:
  memory_limiter:
    check_interval: 1s
    limit_mib: 128
  batch:
    timeout: 5s
    send_batch_size: 128

exporters:
  beaconjson:
    path: "/tmp/beacon/runtime.jsonl"
    max_event_bytes: 65536
    rotate_bytes: 10485760
    rotate_archives: 5
    redact_secrets: true

extensions:
  health_check:
    endpoint: 127.0.0.1:13133

service:
  telemetry:
    metrics:
      level: none
  extensions: [health_check]
  pipelines:
    logs:
      receivers: [otlp]
      processors: [memory_limiter, batch]
      exporters: [beaconjson]
    traces:
      receivers: [otlp]
      processors: [memory_limiter, batch]
      exporters: [beaconjson]
    metrics:
      receivers: [otlp]
      processors: [memory_limiter, batch]
      exporters: [beaconjson]
EOF

if [ -f /tmp/beacon/otelcol.pid ] && kill -0 "$(cat /tmp/beacon/otelcol.pid)" 2>/dev/null; then
  echo "Beacon collector already running with pid $(cat /tmp/beacon/otelcol.pid)"
else
  nohup /tmp/beacon/bin/beacon-otelcol --config /tmp/beacon/otelcol.yaml > /tmp/beacon/logs/otelcol.log 2>&1 &
  echo "$!" > /tmp/beacon/otelcol.pid
fi

echo "Codex hooks must be committed before starting Codex Cloud Agents:"
echo "  beacon cloud codex print-hooks --binary-path /tmp/beacon/bin/beacon-hooks --log-path /tmp/beacon/runtime.jsonl > .codex/hooks.json"

python3 - <<'PY'
import os
from pathlib import Path

path = Path(os.environ.get("CODEX_CONFIG_DIR", str(Path.home() / ".codex"))) / "config.toml"
existing = path.read_text() if path.exists() else ""
lines = existing.splitlines()
out = []
in_otel = False
wrote = False
block = [
    "[otel]",
    'environment = "cloud"',
    'exporter = "otlp-grpc"',
    'log_user_prompt = true',
    "",
    '[otel.exporter."otlp-grpc"]',
    'endpoint = "http://127.0.0.1:4317"',
    "",
]
for line in lines:
    stripped = line.strip()
    if stripped.startswith("[") and stripped.endswith("]"):
        is_otel = stripped == "[otel]" or stripped.startswith("[otel.")
        if in_otel and not is_otel and not wrote:
            out.extend(block)
            wrote = True
        in_otel = is_otel
        if not in_otel:
            out.append(line)
        continue
    if in_otel:
        continue
    out.append(line)
if in_otel and not wrote:
    out.extend(block)
    wrote = True
if not wrote:
    if existing.strip():
        out.append("")
    out.extend(block)
path.parent.mkdir(parents=True, exist_ok=True)
path.write_text("\n".join(out).rstrip() + "\n")
PY

cat > /tmp/beacon/codex-env.sh <<'EOF'
beacon_cloud_attrs() {
  attrs="beacon.origin=${BEACON_ORIGIN:-cloud},beacon.run.provider=${BEACON_RUN_PROVIDER:-codex_cloud},beacon.run.ephemeral=${BEACON_RUN_EPHEMERAL:-true}"
  if [ -n "${BEACON_RUN_ID:-}" ]; then attrs="${attrs},beacon.run.run_id=${BEACON_RUN_ID}"; fi
  if [ -n "${BEACON_RUN_REPOSITORY:-}" ]; then attrs="${attrs},beacon.run.repository=${BEACON_RUN_REPOSITORY}"; fi
  if [ -n "${BEACON_RUN_BRANCH:-}" ]; then attrs="${attrs},beacon.run.branch=${BEACON_RUN_BRANCH}"; fi
  if [ -n "${OTEL_RESOURCE_ATTRIBUTES:-}" ]; then
    export OTEL_RESOURCE_ATTRIBUTES="${OTEL_RESOURCE_ATTRIBUTES},${attrs}"
  else
    export OTEL_RESOURCE_ATTRIBUTES="${attrs}"
  fi
  export BEACON_ORIGIN="${BEACON_ORIGIN:-cloud}"
  export BEACON_RUN_PROVIDER="${BEACON_RUN_PROVIDER:-codex_cloud}"
  export BEACON_RUN_EPHEMERAL="${BEACON_RUN_EPHEMERAL:-true}"
  export BEACON_ENDPOINT_LOG="${BEACON_ENDPOINT_LOG:-/tmp/beacon/runtime.jsonl}"
}
beacon_cloud_attrs
EOF

if ! grep -q "/tmp/beacon/codex-env.sh" "$HOME/.bashrc" 2>/dev/null; then
  printf '\n# Beacon Codex cloud telemetry\n[ -f /tmp/beacon/codex-env.sh ] && . /tmp/beacon/codex-env.sh\n' >> "$HOME/.bashrc"
fi

. /tmp/beacon/codex-env.sh
nohup /tmp/beacon/bin/beacon-hooks --platform codex cloud-watch > /tmp/beacon/logs/cloud-watch.log 2>&1 &
echo "$!" > /tmp/beacon/cloud-watch.pid

echo "Beacon Codex cloud telemetry configured:"
echo "  collector: /tmp/beacon/otelcol.yaml"
echo "  runtime log: /tmp/beacon/runtime.jsonl"
echo "  Codex config: $CODEX_CONFIG_DIR/config.toml"
echo "  Commit .codex/hooks.json before starting cloud tasks"
`, version)
}

func runCloudGCSSetup(cmd *cobra.Command, args []string) error {
	if cloudOpts.project == "" {
		return fmt.Errorf("--project is required")
	}
	if cloudOpts.bucket == "" {
		return fmt.Errorf("--bucket is required")
	}
	if !cloudOpts.apply && !cloudOpts.printOnly {
		return fmt.Errorf("choose --print to review commands or --apply to run them")
	}
	email := serviceAccountEmail(cloudOpts.serviceAccount, cloudOpts.project)
	commands := gcsSetupCommands(cloudOpts.project, cloudOpts.bucket, cloudOpts.location, email, serviceAccountID(cloudOpts.serviceAccount))
	if cloudOpts.printOnly {
		for _, command := range commands {
			fmt.Println(shellCommand(command...))
		}
	}
	if !cloudOpts.apply {
		return nil
	}
	if err := runGCloud("gcloud", "services", "enable", "storage.googleapis.com", "iam.googleapis.com", "--project", cloudOpts.project); err != nil {
		return err
	}
	if err := ensureGCSBucket(cloudOpts.project, cloudOpts.bucket, cloudOpts.location); err != nil {
		return err
	}
	if err := ensureServiceAccount(cloudOpts.project, email, serviceAccountID(cloudOpts.serviceAccount)); err != nil {
		return err
	}
	if err := runGCloud("gcloud", "storage", "buckets", "add-iam-policy-binding", "gs://"+cloudOpts.bucket, "--member", "serviceAccount:"+email, "--role", "roles/storage.objectUser"); err != nil {
		return err
	}
	if cloudOpts.printEnv {
		keyPath, cleanup, err := createServiceAccountKey(cloudOpts.project, email)
		if err != nil {
			return err
		}
		defer cleanup()
		data, err := os.ReadFile(keyPath)
		if err != nil {
			return err
		}
		fmt.Printf("BEACON_CLOUD_GCS_BUCKET=%s\n", cloudOpts.bucket)
		fmt.Printf("BEACON_CLOUD_GCS_PREFIX=%s\n", strings.Trim(cloudOpts.prefix, "/"))
		fmt.Printf("BEACON_CLOUD_GCS_CREDENTIALS_B64=%s\n", base64.StdEncoding.EncodeToString(data))
	}
	return nil
}

func gcsSetupCommands(project, bucket, location, email, accountID string) [][]string {
	return [][]string{
		{"gcloud", "services", "enable", "storage.googleapis.com", "iam.googleapis.com", "--project", project},
		{"gcloud", "storage", "buckets", "describe", "gs://" + bucket, "--project", project},
		{"gcloud", "storage", "buckets", "create", "gs://" + bucket, "--project", project, "--location", location, "--uniform-bucket-level-access"},
		{"gcloud", "iam", "service-accounts", "describe", email, "--project", project},
		{"gcloud", "iam", "service-accounts", "create", accountID, "--project", project, "--display-name", "Beacon cloud trace uploader"},
		{"gcloud", "storage", "buckets", "add-iam-policy-binding", "gs://" + bucket, "--member", "serviceAccount:" + email, "--role", "roles/storage.objectUser"},
	}
}

func runGCloud(args ...string) error {
	output, err := runGCloudCommand(args...)
	if err != nil {
		text := strings.TrimSpace(string(output))
		if isAlreadyExistsOutput(text) {
			return nil
		}
		if isGCSBucketIAMBinding(args) && serviceAccountNotPropagated(text) {
			var lastOutput []byte
			var lastErr error
			for i := 0; i < 6; i++ {
				time.Sleep(time.Duration(i+1) * 5 * time.Second)
				lastOutput, lastErr = runGCloudCommand(args...)
				if lastErr == nil {
					return nil
				}
				if !serviceAccountNotPropagated(strings.TrimSpace(string(lastOutput))) {
					break
				}
			}
			if lastErr != nil {
				return fmt.Errorf("%s failed after waiting for service account propagation: %w\n%s", shellCommand(args...), lastErr, strings.TrimSpace(string(lastOutput)))
			}
		}
		return fmt.Errorf("%s failed: %w\n%s", shellCommand(args...), err, text)
	}
	return nil
}

func runGCloudCommand(args ...string) ([]byte, error) {
	return exec.Command(args[0], args[1:]...).CombinedOutput()
}

func ensureGCSBucket(project, bucket, location string) error {
	describe := []string{"gcloud", "storage", "buckets", "describe", "gs://" + bucket, "--project", project}
	output, err := runGCloudCommand(describe...)
	if err == nil {
		return nil
	}
	if !isNotFoundOutput(string(output)) {
		return fmt.Errorf("%s failed: %w\n%s", shellCommand(describe...), err, strings.TrimSpace(string(output)))
	}
	return runGCloud("gcloud", "storage", "buckets", "create", "gs://"+bucket, "--project", project, "--location", location, "--uniform-bucket-level-access")
}

func ensureServiceAccount(project, email, accountID string) error {
	describe := []string{"gcloud", "iam", "service-accounts", "describe", email, "--project", project}
	output, err := runGCloudCommand(describe...)
	if err == nil {
		return nil
	}
	if !isNotFoundOutput(string(output)) {
		return fmt.Errorf("%s failed: %w\n%s", shellCommand(describe...), err, strings.TrimSpace(string(output)))
	}
	return runGCloud("gcloud", "iam", "service-accounts", "create", accountID, "--project", project, "--display-name", "Beacon cloud trace uploader")
}

func isAlreadyExistsOutput(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "already exists") ||
		strings.Contains(lower, "already own it") ||
		strings.Contains(lower, "alreadyexists")
}

func isNotFoundOutput(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "not found") ||
		strings.Contains(lower, "does not exist") ||
		strings.Contains(lower, "matched no") ||
		strings.Contains(lower, "no urls matched")
}

func isGCSBucketIAMBinding(args []string) bool {
	return len(args) >= 5 &&
		args[0] == "gcloud" &&
		args[1] == "storage" &&
		args[2] == "buckets" &&
		args[3] == "add-iam-policy-binding"
}

func serviceAccountNotPropagated(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "service account") && strings.Contains(lower, "does not exist")
}

func createServiceAccountKey(project, email string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "beacon-cloud-gcs-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	keyPath := filepath.Join(dir, "uploader.json")
	args := []string{"gcloud", "iam", "service-accounts", "keys", "create", keyPath, "--iam-account", email, "--project", project}
	output, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("%s failed: %w\n%s", shellCommand(args...), err, strings.TrimSpace(string(output)))
	}
	return keyPath, cleanup, nil
}

func serviceAccountEmail(value, project string) string {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "@") {
		return value
	}
	return value + "@" + project + ".iam.gserviceaccount.com"
}

func serviceAccountID(value string) string {
	value = strings.TrimSpace(value)
	if before, _, ok := strings.Cut(value, "@"); ok {
		return before
	}
	return value
}

func shellCommand(args ...string) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if strings.IndexFunc(value, func(r rune) bool {
		return !(r == '-' || r == '_' || r == '.' || r == '/' || r == ':' || r == '@' || r == '=' || r == ',' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
	}) == -1 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
