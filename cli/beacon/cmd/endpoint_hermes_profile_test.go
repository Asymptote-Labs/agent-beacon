package cmd

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/hermessession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/spf13/cobra"
)

func hermesCommandHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("HERMES_HOME", "")
	t.Setenv("HERMES_DATA_DIR_SUFFIX", "")
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "LocalAppData"))
	oldHermes, oldEndpoint := endpointHermesOpts, endpointOpts
	t.Cleanup(func() { endpointHermesOpts, endpointOpts = oldHermes, oldEndpoint })
	endpointHermesOpts.dbPath, endpointHermesOpts.statePath = "", ""
	endpointHermesOpts.logPath = filepath.Join(home, "runtime.jsonl")
	endpointHermesOpts.print, endpointHermesOpts.watch = false, false
	endpointOpts.userMode, endpointOpts.systemMode, endpointOpts.jsonOutput = true, false, true
	return home
}

func writeHermesCommandDB(t *testing.T, path, prompt string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
CREATE TABLE sessions (id TEXT PRIMARY KEY, source TEXT, model TEXT, cwd TEXT, title TEXT,
 parent_session_id TEXT, started_at REAL, ended_at REAL, end_reason TEXT,
 message_count INTEGER, tool_call_count INTEGER, input_tokens INTEGER, output_tokens INTEGER,
 cache_read_tokens INTEGER, cache_write_tokens INTEGER, reasoning_tokens INTEGER,
 estimated_cost_usd REAL, actual_cost_usd REAL, cost_status TEXT, cost_source TEXT, archived INTEGER);
CREATE TABLE messages (id INTEGER PRIMARY KEY, session_id TEXT, role TEXT, content TEXT,
 tool_call_id TEXT, tool_calls TEXT, tool_name TEXT, timestamp REAL, token_count INTEGER,
 finish_reason TEXT, reasoning TEXT, reasoning_content TEXT, reasoning_details TEXT,
 platform_message_id TEXT, observed INTEGER, active INTEGER);
INSERT INTO sessions (id, source, cwd, started_at, input_tokens, output_tokens, archived)
 VALUES ('shared-session', 'cli', '/project', 1780000000, 10, 2, 0);`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO messages (id, session_id, role, content, timestamp, active)
 VALUES (1, 'shared-session', 'user', ?, 1780000001, 1)`, prompt); err != nil {
		t.Fatal(err)
	}
}

func hermesCommandStatus(t *testing.T) hermesStatusReport {
	t.Helper()
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := runEndpointHermesStatus(cmd, nil); err != nil {
		t.Fatal(err)
	}
	var report hermesStatusReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("status = %s: %v", out.Bytes(), err)
	}
	return report
}

func hermesCommandSync(t *testing.T) hermessession.Summary {
	t.Helper()
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := runEndpointHermesSync(cmd, nil); err != nil {
		t.Fatal(err)
	}
	var summary hermessession.Summary
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatalf("sync = %s: %v", out.Bytes(), err)
	}
	return summary
}

func TestHermesProfilesDoNotInheritTheLegacyCursor(t *testing.T) {
	home := hermesCommandHome(t)
	legacy := hermessession.DefaultStatePath()
	oldState := &hermessession.State{Version: hermessession.StateVersion, Sessions: map[string]*hermessession.Cursor{
		"shared-session": {LastMessageID: 100, Started: true, InputTokens: 1000, OutputTokens: 1000},
	}}
	if err := oldState.Save(legacy); err != nil {
		t.Fatal(err)
	}
	legacyBytes, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoints []string
	for _, root := range []string{"one", "two"} {
		profile := filepath.Join(home, root, "profiles", "work")
		dbPath := filepath.Join(profile, "state.db")
		writeHermesCommandDB(t, dbPath, "prompt from "+root)
		t.Setenv("HERMES_HOME", profile)
		before := hermesCommandStatus(t)
		if before.StatePath == legacy || len(before.Sessions) != 1 || before.Sessions[0].Collected {
			t.Fatalf("profile %s inherited legacy progress: %+v", root, before)
		}
		if summary := hermesCommandSync(t); summary.EventsEmitted != 3 {
			t.Fatalf("profile %s sync = %+v, want its start, prompt and usage", root, summary)
		}
		after := hermesCommandStatus(t)
		if after.StatePath != before.StatePath || !after.Sessions[0].Collected || after.Sessions[0].CollectedMessageID != 1 {
			t.Fatalf("profile %s status after sync = %+v", root, after)
		}
		endpointHermesOpts.dbPath = dbPath
		if explicit := hermesCommandStatus(t); explicit.StatePath != after.StatePath {
			t.Fatalf("--db selects a different cursor for the same store: %+v / %+v", explicit, after)
		}
		endpointHermesOpts.dbPath = ""
		if summary := hermesCommandSync(t); summary.EventsEmitted != 0 {
			t.Fatalf("unchanged profile %s emitted again: %+v", root, summary)
		}
		checkpoints = append(checkpoints, after.StatePath)
	}
	if checkpoints[0] == checkpoints[1] {
		t.Fatalf("different roots with the same profile name share %s", checkpoints[0])
	}
	for _, root := range []string{"one", "two"} {
		profile := filepath.Join(home, root, "profiles", "work")
		db, err := sql.Open("sqlite", filepath.Join(profile, "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(`INSERT INTO messages (id, session_id, role, content, timestamp, active)
 VALUES (2, 'shared-session', 'user', ?, 1780000002, 1);
UPDATE sessions SET input_tokens=15, output_tokens=3, ended_at=1780000003 WHERE id='shared-session'`, "later prompt from "+root)
		db.Close()
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("HERMES_HOME", profile)
		if summary := hermesCommandSync(t); summary.EventsEmitted != 3 {
			t.Fatalf("profile %s append = %+v, want prompt, usage delta and end", root, summary)
		}
		if summary := hermesCommandSync(t); summary.EventsEmitted != 0 {
			t.Fatalf("profile %s append replayed: %+v", root, summary)
		}
		status := hermesCommandStatus(t)
		if !status.Sessions[0].Collected || status.Sessions[0].CollectedMessageID != 2 {
			t.Fatalf("profile %s appended status = %+v", root, status)
		}
	}
	if got, err := os.ReadFile(legacy); err != nil || !bytes.Equal(got, legacyBytes) {
		t.Fatalf("profile sync changed the legacy cursor: %q, %v", got, err)
	}
	logBytes, err := os.ReadFile(endpointHermesOpts.logPath)
	if err != nil {
		t.Fatal(err)
	}
	prompts := map[string]bool{}
	starts := map[string]int{}
	decoder := json.NewDecoder(bytes.NewReader(logBytes))
	for decoder.More() {
		var event schema.Event
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Prompt != nil {
			prompts[event.Prompt.Text] = true
		}
		if event.Event.Action == "session.started" {
			starts[event.Harness.ConfigPath]++
		}
	}
	for _, root := range []string{"one", "two"} {
		if !prompts["prompt from "+root] || !prompts["later prompt from "+root] {
			t.Fatalf("profile %s lost a prompt: %v", root, prompts)
		}
		if got := starts[filepath.Join(home, root, "profiles", "work", "state.db")]; got != 1 {
			t.Errorf("profile %s starts = %d, want 1", root, got)
		}
	}
}

func TestHermesNativeDefaultKeepsItsLegacyProgress(t *testing.T) {
	hermesCommandHome(t)
	dbPath := hermessession.DefaultDBPath()
	writeHermesCommandDB(t, dbPath, "already collected")
	legacy := hermessession.DefaultStatePath()
	state := &hermessession.State{Version: hermessession.StateVersion, Sessions: map[string]*hermessession.Cursor{
		"shared-session": {LastMessageID: 1, Started: true, InputTokens: 10, OutputTokens: 2},
	}}
	if err := state.Save(legacy); err != nil {
		t.Fatal(err)
	}
	for _, explicitDB := range []string{"", dbPath} {
		endpointHermesOpts.dbPath = explicitDB
		if status := hermesCommandStatus(t); status.StatePath != legacy || !status.Sessions[0].Collected {
			t.Fatalf("native database %q lost legacy progress: %+v", explicitDB, status)
		}
		if summary := hermesCommandSync(t); summary.EventsEmitted != 0 {
			t.Fatalf("native database %q replayed legacy progress: %+v", explicitDB, summary)
		}
	}
}

func TestHermesExplicitStateAndSystemDefaults(t *testing.T) {
	home := hermesCommandHome(t)
	first := filepath.Join(home, "one", "state.db")
	second := filepath.Join(home, "two", "state.db")
	for _, userMode := range []bool{true, false} {
		explicit := " relative custom state.json "
		if got, err := resolveHermesStatePath(explicit, first, userMode); err != nil || got != explicit {
			t.Fatalf("explicit state changed: %q, %v", got, err)
		}
		a, err := resolveHermesStatePath("", first, userMode)
		if err != nil {
			t.Fatal(err)
		}
		b, err := resolveHermesStatePath("", second, userMode)
		if err != nil {
			t.Fatal(err)
		}
		if a == b {
			t.Fatalf("mode user=%t shares a checkpoint across stores: %q", userMode, a)
		}
		if userMode != (filepath.Dir(a) == filepath.Dir(hermessession.DefaultStatePath())) {
			t.Fatalf("checkpoint is in the wrong collection mode: %q (user=%t)", a, userMode)
		}
	}
}

func TestHermesPollEventsNameTheirOpenedStore(t *testing.T) {
	home := hermesCommandHome(t)
	dbPath := filepath.Join(home, "custom", "state.db")
	writeHermesCommandDB(t, dbPath, "source attribution")
	var out bytes.Buffer
	if summary, err := hermessession.CollectOnce(hermessession.CollectOptions{DBPath: dbPath, Print: true, Out: &out}); err != nil || summary.EventsEmitted == 0 {
		t.Fatalf("collect = %+v, %v", summary, err)
	}
	decoder := json.NewDecoder(&out)
	for decoder.More() {
		var event schema.Event
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Harness.ConfigPath != dbPath {
			t.Errorf("%s has config_path %q, want %q", event.Event.Action, event.Harness.ConfigPath, dbPath)
		}
	}
}

func TestHermesBackfillUsesTheSameProfileCursorAsSync(t *testing.T) {
	home := hermesCommandHome(t)
	profile := filepath.Join(home, "custom", "profiles", "work")
	writeHermesCommandDB(t, filepath.Join(profile, "state.db"), "backfilled prompt")
	t.Setenv("HERMES_HOME", profile)
	before := hermesCommandStatus(t)
	var found bool
	for _, runner := range endpointBackfillRunners() {
		if runner.Name != "Hermes Agent" {
			continue
		}
		found = true
		if events, err := runner.Run(endpointBackfillRun{LogPath: endpointHermesOpts.logPath, UserMode: true}); err != nil || events != 3 {
			t.Fatalf("Hermes backfill = %d, %v", events, err)
		}
	}
	if !found {
		t.Fatal("Hermes backfill runner missing")
	}
	after := hermesCommandStatus(t)
	if after.StatePath != before.StatePath || !after.Sessions[0].Collected {
		t.Fatalf("backfill and status disagree: before=%+v after=%+v", before, after)
	}
	if summary := hermesCommandSync(t); summary.EventsEmitted != 0 {
		t.Fatalf("sync replayed the backfill: %+v", summary)
	}
}
