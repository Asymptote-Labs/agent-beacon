package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/fxsession"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const (
	fxTestGeneration = "1f4b2c8e6d3a5a7c9b613f0d5e8c2a14"
	fxTestSession    = "1770000000000-1770000000000000000-a1b2c3d4e5f60718"
)

// writeFxSession lays out one fx session under home, as fx leaves it once a turn is committed.
func writeFxSession(t *testing.T, home, id string) {
	t.Helper()
	dir := filepath.Join(home, ".fx", "sessions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(
		`{"schema_version":1,"log_generation":%q,"seq":1,"event_id":"a1","timestamp_ms":1770000000000,"kind":"session_started",`+
			`"payload":{"id":%q,"created_at_ms":1770000000000,"origin_workspace_root":"/repo","workspace_root":"/repo",`+
			`"conversation_language":"en","preferences":{"provider":"gateway","model":"m","effort":"medium","fast_mode":false},"usage":null}}`,
		fxTestGeneration, id) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(
		`{"schema_version":3,"storage_format":"event_log_v1","id":%q,"authority_id":"ab","log_generation":%q,`+
			`"created_at_ms":1770000000000,"updated_at_ms":1770000000000,"origin_workspace_root":"/repo","workspace_root":"/repo",`+
			`"conversation_language":"en","history_len":0,"total_input_tokens":0,"total_output_tokens":0,`+
			`"last_event_seq":1,"event_log_bytes":%d,"event_log_stat_fingerprint":"cd",`+
			`"generation_base_seq":0,"generation_base_bytes":0,"checkpoint_seq":null,"checkpoint_sha256":null,`+
			`"preferences":{"provider":"gateway","model":"m","effort":"medium","fast_mode":false}}`,
		id, fxTestGeneration, len(line))
	if err := os.WriteFile(filepath.Join(dir, "session.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeFxCursor(t *testing.T, home string, cursor fxsession.Cursor, id string) {
	t.Helper()
	state := &fxsession.State{Version: fxsession.StateVersion, Sessions: map[string]*fxsession.Cursor{id: &cursor}}
	if err := state.Save(filepath.Join(home, ".beacon", "endpoint", "state", "fx.json")); err != nil {
		t.Fatal(err)
	}
}

func fxHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

// The harness name discovery reports has to be the one the collector writes, or `endpoint discover`
// and the runtime log describe the same runtime under two names.
func TestDiscoverFxUsesTheCanonicalHarnessName(t *testing.T) {
	if FxName != fxsession.Harness {
		t.Fatalf("discovery reports %q while the collector writes %q", FxName, fxsession.Harness)
	}
	if got := asymptoteobserve.NormalizeHarnessName(FxName); got != FxName {
		t.Fatalf("NormalizeHarnessName(%q) = %q", FxName, got)
	}
}

// A machine that has never run fx must report that plainly rather than as a broken install.
func TestDiscoverFxOnAMachineWithoutFx(t *testing.T) {
	fxHome(t)

	h := DiscoverFx()
	if h.TelemetryStatus != TelemetryMissing {
		t.Errorf("status = %q, want %q", h.TelemetryStatus, TelemetryMissing)
	}
	if !strings.Contains(h.Message, "no sessions") {
		t.Errorf("message = %q", h.Message)
	}
	if h.Capability != "session_log" {
		t.Errorf("capability = %q, want session_log", h.Capability)
	}
}

// fx has been used and nothing has collected from it. This is the case where a "telemetry enabled"
// answer would be a lie: the sessions are there, the events are not.
func TestDiscoverFxReportsUncollectedSessionsAsDisabled(t *testing.T) {
	home := fxHome(t)
	writeFxSession(t, home, fxTestSession)

	h := DiscoverFx()
	if h.TelemetryStatus != TelemetryDisabled {
		t.Fatalf("status = %q, want %q -- sessions exist but nothing has read them", h.TelemetryStatus, TelemetryDisabled)
	}
	if !strings.Contains(h.Message, "beacon endpoint fx sync") {
		t.Errorf("message = %q, want it to name the command that fixes this", h.Message)
	}
	if !h.Detected {
		t.Error("fx was not detected despite its profile directory being present")
	}
}

func TestDiscoverFxReportsCollectedSessionsAsEnabled(t *testing.T) {
	home := fxHome(t)
	writeFxSession(t, home, fxTestSession)
	writeFxCursor(t, home, fxsession.Cursor{Generation: fxTestGeneration, LastSeq: 1, Started: true}, fxTestSession)

	h := DiscoverFx()
	if h.TelemetryStatus != TelemetryEnabled {
		t.Fatalf("status = %q, want %q", h.TelemetryStatus, TelemetryEnabled)
	}
	if !strings.Contains(h.Message, "all 1 session") {
		t.Errorf("message = %q", h.Message)
	}
}

// A cursor from an earlier sweep that has fallen behind is not the same as no collection at all,
// and the difference decides whether someone goes looking for a broken setup or just runs a sweep.
func TestDiscoverFxDistinguishesStaleCollectionFromNone(t *testing.T) {
	home := fxHome(t)
	writeFxSession(t, home, fxTestSession)
	other := "1770000000001-1770000000000000001-b1b2c3d4e5f60718"
	writeFxSession(t, home, other)
	writeFxCursor(t, home, fxsession.Cursor{Generation: fxTestGeneration, LastSeq: 1, Started: true}, fxTestSession)

	h := DiscoverFx()
	if h.TelemetryStatus != TelemetryEnabled {
		t.Fatalf("status = %q, want %q", h.TelemetryStatus, TelemetryEnabled)
	}
	if !strings.Contains(h.Message, "1 of 2") {
		t.Errorf("message = %q, want it to say how far behind collection is", h.Message)
	}
}

// A cursor pointing at a generation the session no longer has means fx compacted the log after the
// last sweep. That session is not collected, and saying it is would hide a real gap.
func TestDiscoverFxDoesNotCountACursorFromAnotherGeneration(t *testing.T) {
	home := fxHome(t)
	writeFxSession(t, home, fxTestSession)
	writeFxCursor(t, home, fxsession.Cursor{Generation: "ffffffffffffffffffffffffffffffff", LastSeq: 99, Started: true}, fxTestSession)

	h := DiscoverFx()
	if h.TelemetryStatus != TelemetryDisabled {
		t.Fatalf("status = %q, want %q -- the cursor describes a log generation that is gone", h.TelemetryStatus, TelemetryDisabled)
	}
}

// Discovery is a read. It must not create fx's directory, Beacon's state directory, or anything
// else in the home of someone who does not run fx.
func TestDiscoverFxWritesNothing(t *testing.T) {
	home := fxHome(t)

	DiscoverFx()

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

// fx is part of the discovery sweep, not a separate command someone has to know to run.
func TestDiscoverAllIncludesFx(t *testing.T) {
	fxHome(t)
	for _, h := range DiscoverAll() {
		if h.Name == FxName {
			return
		}
	}
	t.Fatalf("DiscoverAll does not include %q", FxName)
}
