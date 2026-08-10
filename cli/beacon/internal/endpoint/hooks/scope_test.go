package hooks

import (
	"path/filepath"
	"runtime"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

// A hook writing to a machine-wide log is feeding a system-mode endpoint and must read the system
// config, whatever scope the caller asked for. The previous test matched the POSIX prefixes
// "/var/log/" and "/Library/", which named two platforms' locations and no Windows one -- so a
// Windows system install would have routed its hooks to the *user* config and pointed them at a
// log the collector never reads. That fails silently, which is the worst way for it to fail.
func TestSystemLogRoutesHooksToTheSystemConfig(t *testing.T) {
	system := endpointconfig.ConfigPath(false)

	for _, logPath := range []string{
		endpointconfig.SystemLogPath(),
		filepath.Join(endpointconfig.SystemLogDir(), "inventory_state.jsonl"),
		filepath.Join(endpointconfig.SystemBaseDir(), "logs", "runtime.jsonl"),
	} {
		if got := endpointConfigPathForHook(logPath, true); got != system {
			t.Errorf("a hook logging to %q asked for user mode and got %q, want the system config %q",
				logPath, got, system)
		}
	}
}

// A log in the user's own tree must keep the caller's scope, or a user-mode install would write
// its hook configuration into a system path it may not even be able to open.
func TestUserLogKeepsTheRequestedScope(t *testing.T) {
	userLog := filepath.Join(t.TempDir(), ".beacon", "endpoint", "logs", "runtime.jsonl")

	if got, want := endpointConfigPathForHook(userLog, true), endpointconfig.ConfigPath(true); got != want {
		t.Errorf("user-mode log routed to %q, want %q", got, want)
	}
	if got, want := endpointConfigPathForHook(userLog, false), endpointconfig.ConfigPath(false); got != want {
		t.Errorf("system-mode caller with a user log routed to %q, want %q", got, want)
	}
}

// Windows paths compare case-insensitively and may arrive with either separator. A false negative
// is the dangerous direction, so both spellings must still be recognized.
func TestSystemLocationMatchIsPlatformAppropriate(t *testing.T) {
	dir := endpointconfig.SystemLogDir()
	if !underSystemLocation(filepath.Join(dir, "runtime.jsonl")) {
		t.Fatalf("the system log dir %q must be recognized", dir)
	}
	if runtime.GOOS == "windows" {
		mixed := filepath.Join(dir, "RUNTIME.JSONL")
		if !underSystemLocation(mixed) {
			t.Errorf("windows path comparison must be case-insensitive, %q was not matched", mixed)
		}
	}
	// A sibling directory whose name merely starts with the same characters is not underneath it.
	if underSystemLocation(dir + "-other/runtime.jsonl") {
		t.Errorf("%q-other must not be treated as being under %q", dir, dir)
	}
}
