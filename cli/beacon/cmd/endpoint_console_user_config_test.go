package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
)

func stubConsoleUser(t *testing.T, info consoleUserInfo, ok bool, err error) {
	t.Helper()
	restore := activeConsoleUser
	t.Cleanup(func() { activeConsoleUser = restore })
	activeConsoleUser = func() (consoleUserInfo, bool, error) { return info, ok, err }
}

func userHomeWith(t *testing.T, rel, contents string) consoleUserInfo {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return consoleUserInfo{Username: "alice", HomeDir: home}
}

// The state this check exists to catch, and the one that shipped: a system install that resolved
// nobody, so the collector runs perfectly and captures nothing. Every other check is green in this
// state because they all read root's configuration, which the install did write.
func TestConsoleUserConfigCheckFailsWhenTheUserIsNotConfigured(t *testing.T) {
	stubConsoleUser(t, consoleUserInfo{Username: "alice", HomeDir: t.TempDir()}, true, nil)

	got := consoleUserConfigCheck()
	if got.Status != diagnostics.StatusFail {
		t.Errorf("status = %v, want fail: an endpoint capturing nobody must not read as healthy", got.Status)
	}
	if got.Action == "" {
		t.Error("a failure the operator can fix must say how")
	}
}

func TestConsoleUserConfigCheckPassesWhenPointedAtTheCollector(t *testing.T) {
	info := userHomeWith(t, ".claude/settings.json",
		`{"env":{"OTEL_EXPORTER_OTLP_ENDPOINT":"http://127.0.0.1:4317"}}`)
	stubConsoleUser(t, info, true, nil)

	if got := consoleUserConfigCheck(); got.Status != diagnostics.StatusOK {
		t.Errorf("status = %v (%s), want ok", got.Status, got.Message)
	}
}

// A settings file that exists but points somewhere else is not configured for this endpoint. The
// distinction matters because a developer who has used Claude Code before installing Beacon
// already has the file.
func TestConsoleUserConfigCheckRejectsAnUnrelatedSettingsFile(t *testing.T) {
	info := userHomeWith(t, ".claude/settings.json", `{"theme":"dark"}`)
	stubConsoleUser(t, info, true, nil)

	if got := consoleUserConfigCheck(); got.Status != diagnostics.StatusFail {
		t.Errorf("status = %v, want fail for a settings file not pointed at this collector", got.Status)
	}
}

// An unattended host legitimately has nobody to configure. That is a warning, not a failure --
// failing it would make every CI and container install red for a condition nobody can act on.
func TestConsoleUserConfigCheckWarnsWhenNobodyIsLoggedIn(t *testing.T) {
	stubConsoleUser(t, consoleUserInfo{}, false, nil)

	got := consoleUserConfigCheck()
	if got.Status != diagnostics.StatusWarn {
		t.Errorf("status = %v, want warn when no console user exists", got.Status)
	}
}

// Hook-only runtimes count as configured: the installed hook command names the hook binary, which
// is what points that runtime at the endpoint.
func TestConsoleUserConfigCheckAcceptsAHookOnlyRuntime(t *testing.T) {
	info := userHomeWith(t, ".cursor/hooks.json",
		`{"hooks":{"beforeShellExecution":[{"command":"/etc/beacon/endpoint/hooks/beacon-hooks pre-tool"}]}}`)
	stubConsoleUser(t, info, true, nil)

	if got := consoleUserConfigCheck(); got.Status != diagnostics.StatusOK {
		t.Errorf("status = %v (%s), want ok for a hook-configured runtime", got.Status, got.Message)
	}
}
