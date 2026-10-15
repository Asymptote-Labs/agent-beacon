package harness

import (
	"os"
	"testing"
)

// TestMain clears CLAUDE_CONFIG_DIR, which a developer's shell may set. Claude Code's settings
// path follows it, and the tests here assume ~/.claude unless they set it themselves.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
	os.Exit(m.Run())
}
