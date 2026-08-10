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
	// A sibling whose name merely starts with the same characters is not underneath it.
	//
	// The sibling is formed from the *outermost* guarded root, not the log directory. On POSIX the
	// log directory sits outside the base directory (/var/log/beacon-agent versus /etc or
	// /Library), so a sibling of either is outside both; on Windows the log directory is nested
	// inside the base directory, so "<logdir>-other" is still under the base and legitimately
	// matches. Testing the log directory's sibling therefore asserted a POSIX layout rather than
	// the prefix-boundary property it was written for.
	sibling := endpointconfig.SystemBaseDir() + "-other"
	if underSystemLocation(filepath.Join(sibling, "runtime.jsonl")) {
		t.Errorf("%q must not be treated as being under %q", sibling, endpointconfig.SystemBaseDir())
	}
}

// Detection matches the binary stem, not the platform-specific filename.
//
// A settings file is a portable artifact: it gets synced between machines, committed to dotfiles,
// and written by older builds. Matching on GetBinaryName meant a Windows Beacon recognized only
// "beacon-hooks.exe" and was blind to a command naming "beacon-hooks" -- so it would neither repair
// nor remove a hook it had every reason to own, and `hooks status` would report not-installed for a
// hook sitting in the file.
func TestHookDetectionRecognizesBothBinarySpellings(t *testing.T) {
	for _, command := range []string{
		`BEACON_ENDPOINT_MODE=1 '/home/u/.beacon/endpoint/hooks/beacon-hooks' --platform cursor`,
		`BEACON_ENDPOINT_MODE=1 'C:\Users\u\.beacon\endpoint\hooks\beacon-hooks.exe' --platform cursor`,
	} {
		if !isEndpointHookCommand(command, "cursor") {
			t.Errorf("command not recognized as a Beacon hook: %s", command)
		}
	}

	// Still not a licence to claim anything: an unrelated command must not be adopted.
	for _, command := range []string{
		`echo keep`,
		`/usr/local/bin/some-other-tool --platform cursor`,
	} {
		if isEndpointHookCommand(command, "cursor") {
			t.Errorf("unrelated command was claimed as a Beacon hook: %s", command)
		}
	}
}
