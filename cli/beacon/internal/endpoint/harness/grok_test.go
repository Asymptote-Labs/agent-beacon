package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/groksession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func grokHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("PATH", t.TempDir())
	return home
}

// writeGrokHooks lays down a Beacon-managed Grok hook file, as `endpoint hooks install --harness
// grok` leaves it: the managed marker plus at least one command naming the hook binary.
func writeGrokHooks(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".grok", "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := `{"beacon":"beacon-managed-grok-hooks:v1","hooks":{"SessionStart":[{"hooks":[{"type":"command",` +
		`"command":"'/opt/beacon/bin/beacon-hooks' --platform grok --log '/tmp/runtime.jsonl' session-start"}]}]}}`
	if err := os.WriteFile(filepath.Join(dir, "beacon-endpoint.json"), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeGrokSession lays out one committed session, as Grok leaves it under ~/.grok/sessions.
func writeGrokSession(t *testing.T, home, id string) {
	t.Helper()
	dir := filepath.Join(home, ".grok", "sessions", "%2Frepo", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.json"),
		[]byte(`{"created_at":"2026-05-21T16:49:22Z","updated_at":"2026-05-21T16:50:25Z","generated_title":"t","current_model_id":"grok-build"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chat_history.jsonl"),
		[]byte(`{"type":"user","content":"hello"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeGrokCursor(t *testing.T, id string, cursor groksession.Cursor) {
	t.Helper()
	state := &groksession.State{Version: groksession.StateVersion, Sessions: map[string]*groksession.Cursor{id: &cursor}}
	if err := state.Save(groksession.DefaultStatePath()); err != nil {
		t.Fatal(err)
	}
}

// The harness name discovery reports has to be the one the collector writes, or `endpoint discover`
// and the runtime log describe the same runtime under two names.
func TestDiscoverGrokUsesTheCanonicalHarnessName(t *testing.T) {
	if GrokName != groksession.Harness {
		t.Fatalf("discovery reports %q while the collector writes %q", GrokName, groksession.Harness)
	}
	if got := asymptoteobserve.NormalizeHarnessName(GrokName); got != GrokName {
		t.Fatalf("NormalizeHarnessName(%q) = %q", GrokName, got)
	}
}

// A runtime missing from DiscoverAll is invisible to `beacon endpoint discover`. It fails silently:
// the command succeeds and simply never mentions Grok Build.
func TestDiscoverAllIncludesGrok(t *testing.T) {
	grokHome(t)
	for _, h := range DiscoverAll() {
		if h.Name == GrokName {
			if h.Capability != "hooks+session_log" {
				t.Errorf("Capability = %q, want hooks+session_log", h.Capability)
			}
			return
		}
	}
	t.Fatalf("DiscoverAll does not include %q", GrokName)
}

// Installed hooks are telemetry. Reporting a machine as disabled until someone runs a backfill
// sweep it does not need would send them to fix something that is already working.
func TestDiscoverGrokReportsInstalledHooksAsEnabledWithoutASyncCursor(t *testing.T) {
	home := grokHome(t)
	writeGrokHooks(t, home)
	writeGrokSession(t, home, "session-1")

	h := DiscoverGrok()
	if h.TelemetryStatus != TelemetryEnabled {
		t.Fatalf("status = %q, want %q -- the hooks are installed", h.TelemetryStatus, TelemetryEnabled)
	}
	if !strings.Contains(h.Message, "hooks are configured") {
		t.Errorf("message = %q, want it to name the hooks", h.Message)
	}
	if want := filepath.Join(home, ".grok", "hooks", "beacon-endpoint.json"); h.ConfigPath != want {
		t.Errorf("ConfigPath = %q, want the managed hook file %q", h.ConfigPath, want)
	}
	if !h.Detected {
		t.Error("Grok was not detected despite its profile directory being present")
	}
}

// Without hooks the sync cursor is the only evidence telemetry is flowing, and sessions nobody has
// read are the case where "enabled" would be a lie.
func TestDiscoverGrokReportsNoHooksAndNoCollectionAsDisabled(t *testing.T) {
	home := grokHome(t)
	writeGrokSession(t, home, "session-1")

	h := DiscoverGrok()
	if h.TelemetryStatus != TelemetryDisabled {
		t.Fatalf("status = %q, want %q -- nothing is collecting", h.TelemetryStatus, TelemetryDisabled)
	}
	if !strings.Contains(h.Message, "hooks install --harness grok") {
		t.Errorf("message = %q, want it to name the command that fixes this", h.Message)
	}
}

// A backfill-only setup is real telemetry too: no hooks, but every committed session read.
func TestDiscoverGrokReportsCollectedSessionsAsEnabledWithoutHooks(t *testing.T) {
	home := grokHome(t)
	writeGrokSession(t, home, "session-1")

	store := &groksession.Store{Dir: filepath.Join(home, ".grok", "sessions")}
	refs, err := store.List()
	if err != nil || len(refs) != 1 {
		t.Fatalf("List() = %d refs, %v", len(refs), err)
	}
	writeGrokCursor(t, refs[0].ID, groksession.Cursor{UpdatedAtMS: refs[0].ModTimeUnixMS, Events: 3, Started: true})

	h := DiscoverGrok()
	if h.TelemetryStatus != TelemetryEnabled {
		t.Fatalf("status = %q, want %q", h.TelemetryStatus, TelemetryEnabled)
	}
	if !strings.Contains(h.Message, "all 1 session") {
		t.Errorf("message = %q", h.Message)
	}
	// With no hooks installed, the session store is what is carrying telemetry. Naming the hook
	// path here would point at a file that is not on this machine and hide the one that is --
	// GrokHookStatus reports the path it would use whether or not the file exists.
	if want := filepath.Join(home, ".grok", "sessions"); h.ConfigPath != want {
		t.Errorf("ConfigPath = %q, want the session store %q", h.ConfigPath, want)
	}
}

// Grok that has never run here is not a broken install, and with no hooks the remedy is to install
// them rather than to sweep a session store that does not exist.
func TestDiscoverGrokOnAMachineWithNoSessionsAndNoHooks(t *testing.T) {
	home := grokHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".grok"), 0o755); err != nil {
		t.Fatal(err)
	}

	h := DiscoverGrok()
	if h.TelemetryStatus != TelemetryDisabled {
		t.Fatalf("status = %q, want %q", h.TelemetryStatus, TelemetryDisabled)
	}
	if !strings.Contains(h.Message, "hooks install --harness grok") {
		t.Errorf("message = %q", h.Message)
	}
}

// Discovery is a read. It must not create Grok's directories, Beacon's state directory, or anything
// else in the home of someone who does not run Grok.
func TestDiscoverGrokWritesNothing(t *testing.T) {
	home := grokHome(t)

	DiscoverGrok()

	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("discovery created %v in a home that had nothing", names)
	}
}
