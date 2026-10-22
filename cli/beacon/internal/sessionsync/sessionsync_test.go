package sessionsync

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
)

type testState struct {
	Version int               `json:"version"`
	Files   map[string]*int64 `json:"files"`
}

var testFile = StateFile[testState]{
	Name:  "Test",
	New:   func() *testState { return &testState{Version: 2, Files: map[string]*int64{}} },
	Valid: func(s *testState) bool { return s.Version == 2 && s.Files != nil },
	Stamp: func(s *testState) { s.Version = 2 },
}

func testEvent(message string) schema.Event {
	return schema.NewEvent(schema.NewEventOptions{Action: "agent.detected", Harness: schema.HarnessInfo{Name: "test"}, Message: message})
}

func TestStateFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "test.json")
	state, err := testFile.Load(path)
	if err != nil || state.Version != 2 || len(state.Files) != 0 {
		t.Fatalf("missing file loaded %+v, %v", state, err)
	}
	n := int64(7)
	state.Files["a"] = &n
	state.Version = 0
	if err := testFile.Save(path, state); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"version\": 2,\n  \"files\": {\n    \"a\": 7\n  }\n}"; string(data) != want {
		t.Fatalf("saved %q, want %q", data, want)
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file left behind: %v", err)
	}
	loaded, err := testFile.Load(path)
	if err != nil || loaded.Files["a"] == nil || *loaded.Files["a"] != 7 {
		t.Fatalf("reloaded %+v, %v", loaded, err)
	}

	if err := os.WriteFile(path, []byte(`{"version":1,"files":{"a":7}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if reset, err := testFile.Load(path); err != nil || len(reset.Files) != 0 {
		t.Fatalf("another version should reset, got %+v, %v", reset, err)
	}
	if err := os.WriteFile(path, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := testFile.Load(path); err == nil || !strings.HasPrefix(err.Error(), "read Test collector state "+path+": ") {
		t.Fatalf("corrupt file error = %v", err)
	}
}

func TestStateFileEmptyPath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	blank := testFile
	blank.BlankPath = true
	for _, f := range []StateFile[testState]{testFile, blank} {
		if err := f.Save("", f.New()); err != nil {
			t.Fatal(err)
		}
	}
	if err := blank.Save("  ", blank.New()); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("an empty path wrote %v", entries)
	}
}

func TestOpenSQLiteIsReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	rw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rw.Exec(`CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('x')`); err != nil {
		t.Fatal(err)
	}
	rw.Close()

	db, err := OpenSQLite(path, "busy_timeout(1000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v string
	if err := db.QueryRow(`SELECT v FROM t`).Scan(&v); err != nil || v != "x" {
		t.Fatalf("read %q, %v", v, err)
	}
	if _, err := db.Exec(`INSERT INTO t VALUES ('y')`); err == nil {
		t.Fatal("a read-only open accepted a write")
	}

	missing := filepath.Join(t.TempDir(), "missing.db")
	if db, err := OpenSQLite(missing); err == nil {
		db.Ping()
		db.Close()
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("opening a missing store created it: %v", err)
	}
}

func TestSinkPrintOnly(t *testing.T) {
	event := testEvent("printed")
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	var out bytes.Buffer
	for _, sink := range []Sink{
		{Print: true, Out: &out, Write: true, Log: writer.Options{Path: logPath, UserMode: true}},
		{Print: true, Write: true, Log: writer.Options{Path: logPath, UserMode: true}},
	} {
		if err := sink.Emit(event); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.HasSuffix(out.String(), "\n") || strings.Count(out.String(), "\n") != 1 || !strings.Contains(out.String(), `"message":"printed"`) {
		t.Fatalf("printed %q", out.String())
	}
	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a printing sink wrote the log: %v", err)
	}
}

func TestSinkStopsOnBudgetAndRetention(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	sink := Sink{Write: true, Log: writer.Options{Path: logPath, UserMode: true, Budget: writer.NewBudget(1)}}
	sink.GuardRetention()
	if sink.Log.Guard == nil {
		t.Fatal("a writing sink has no retention guard")
	}
	if err := sink.Emit(testEvent("too big for the budget")); !errors.Is(err, writer.ErrBudgetSpent) {
		t.Fatalf("over budget = %v, want ErrBudgetSpent", err)
	}
	if retained, rotatedOut, err := sink.Retention(); retained != 0 || rotatedOut != 0 || err != nil {
		t.Fatalf("Retention() = %d, %d, %v after nothing was written", retained, rotatedOut, err)
	}

	printing := Sink{Print: true, Write: true}
	printing.GuardRetention()
	if printing.Log.Guard != nil {
		t.Fatal("a printing sink got a retention guard")
	}
	if retained, rotatedOut, err := printing.Retention(); retained != 0 || rotatedOut != 0 || err != nil {
		t.Fatalf("Retention() without a guard = %d, %d, %v", retained, rotatedOut, err)
	}
}
