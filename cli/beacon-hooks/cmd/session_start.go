package cmd

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/cloudshuttle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/state"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

var sessionStartCmd = &cobra.Command{
	Use:   "session-start",
	Short: "Initialize session and cleanup stale data",
	Long:  `SessionStart hook - triggered when a new coding session begins.`,
	Run:   runSessionStart,
}

func init() {
	rootCmd.AddCommand(sessionStartCmd)
}

func runSessionStart(cmd *cobra.Command, args []string) {
	platformLogger := logging.NewLoggerForPlatform("session-start", platformFlag)

	input, err := readStdinJSON()
	if err != nil {
		outputJSON(emptyResponse)
		return
	}

	sessionID := resolveSessionID(input, platformFlag)
	if err := cloudshuttle.ResetFromEnv(); err != nil {
		platformLogger.Warn("Failed to reset cloud telemetry log", "error", err.Error())
	}
	if sessionID == "" {
		if isDevinLikePlatform(platformFlag) {
			logger := logging.NewLoggerForPlatform("session-start", platformFlag)
			logger.Info("Session initialized", "platform", platformFlag)
			emitHookEvent(logger, "session.started", "session", "info", "Agent session started", input, sessionFields("", input))
		}
		outputJSON(emptyResponse)
		return
	}

	// Switch to per-session logger now that we have a session ID
	logger := logging.NewSessionLogger("session-start", platformFlag, sessionID)

	if config.RotateLogIfNeededForPlatform(platformFlag) {
		platformLogger.Info("Rotated log file (exceeded size limit)")
	}

	cleaned := state.CleanupStaleForPlatform(platformFlag)
	if cleaned > 0 {
		logger.Info(fmt.Sprintf("Cleaned up %d stale sessions", cleaned))
	}

	// Recording the session lets a later sweep remove its log if session-end never runs
	if err := state.NewSessionState(sessionID, platformFlag).Register(); err != nil {
		logger.Warn("Failed to record session state", "error", err.Error())
	}

	logger.Info("Session initialized", "session_id", sessionID, "platform", platformFlag)
	emitHookEvent(logger, "session.started", "session", "info", "Agent session started", input, sessionFields(sessionID, input))
	if err := exportClaudeSessionEnv(sessionID); err != nil {
		logger.Warn("Failed to export the session id to CLAUDE_ENV_FILE", "error", err.Error())
	}
	outputJSON(emptyResponse)
}

// claudeEnvSessionID is the shape of session id written to CLAUDE_ENV_FILE. The file is run as shell
// code before every Bash command, so anything outside this set is refused rather than quoted.
var claudeEnvSessionID = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,199}$`) })

// exportClaudeSessionEnv appends BEACON_SESSION_ID and BEACON_HARNESS to the file Claude Code names
// in CLAUDE_ENV_FILE, so the shell commands an agent runs later in the session -- `beacon share
// --current` among them -- know which session they belong to.
//
// Claude Code documents CLAUDE_ENV_FILE for SessionStart hooks only: the hook appends `export`
// lines and Claude Code runs the file before each Bash command. Appending keeps what other hooks
// wrote, and a later SessionStart (a /clear starts a new session) appends a newer value that
// overrides the earlier one. Other runtimes and a missing variable leave everything untouched.
func exportClaudeSessionEnv(sessionID string) error {
	if asymptoteobserve.NormalizeHarnessName(platformFlag) != "claude_code" {
		return nil
	}
	path := strings.TrimSpace(os.Getenv("CLAUDE_ENV_FILE"))
	if path == "" || !claudeEnvSessionID().MatchString(sessionID) {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := fmt.Fprintf(f, "export BEACON_SESSION_ID=%s\nexport BEACON_HARNESS=claude_code\n", sessionID)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}
