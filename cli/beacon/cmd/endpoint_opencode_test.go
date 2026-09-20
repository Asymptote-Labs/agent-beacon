package cmd

import (
	"path/filepath"
	"strings"
	"testing"
)

// The cursor is what keeps a scheduled sweep from re-appending every OpenCode session's whole
// history, so it has to land where the next sweep on this platform will look for it. A hardcoded
// /var/lib path is only the Linux spelling: on macOS and Windows it names a directory nothing
// reads, and progress is silently lost on every run.
func TestOpenCodeStatePathFollowsThePlatformEndpointRoots(t *testing.T) {
	if got := resolveOpenCodeStatePath("/explicit/path.json", true); got != "/explicit/path.json" {
		t.Errorf("an explicit --state was overridden: %q", got)
	}
	if got := resolveOpenCodeStatePath("/explicit/path.json", false); got != "/explicit/path.json" {
		t.Errorf("an explicit --state was overridden in system mode: %q", got)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	user := resolveOpenCodeStatePath("", true)
	if want := filepath.Join(home, ".beacon", "endpoint", "state", "opencode-sessions.json"); user != want {
		t.Errorf("user cursor = %q, want %q", user, want)
	}

	system := resolveOpenCodeStatePath("", false)
	if !filepath.IsAbs(system) {
		t.Errorf("system cursor = %q, which is not somewhere a sweep can reliably write", system)
	}
	if filepath.Base(system) != "opencode-sessions.json" {
		t.Errorf("system cursor = %q, want the opencode cursor file", system)
	}
	if strings.HasPrefix(system, home) {
		t.Errorf("a system sweep put its cursor in the operator's home: %q", system)
	}
	if system == user {
		t.Errorf("system and user sweeps share one cursor at %q", system)
	}
	// The pre-fix path. It is the Linux endpoint root and nothing else, so on macOS and Windows it
	// pointed at a directory no later sweep reads.
	if system == filepath.Join("/var", "lib", "beacon", "endpoint", "state", "opencode-sessions.json") {
		t.Errorf("system cursor is still the hardcoded Linux path: %q", system)
	}
}
