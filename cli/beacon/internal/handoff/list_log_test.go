package handoff

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestListWithLogFiltersAndStorePrecedence(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	logPath := filepath.Join(dir, "runtime.jsonl")
	rows := []struct{ h, id, cwd, ts string }{
		{"claude_code", "shared", project, "2026-10-01T14:00:00Z"},
		{"claude_code", "child", project, "2026-10-01T15:00:00Z"},
		{"gemini_cli", "shared", project, "2026-10-01T13:00:00Z"},
		{"gemini_cli", "other", filepath.Join(dir, "other"), "2026-10-01T16:00:00Z"},
		{"kiro", "nested", filepath.Join(project, "sub"), "2026-10-01T12:00:00Z"},
	}
	file, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		event := map[string]any{"timestamp": row.ts, "harness": map[string]string{"name": row.h}, "session": map[string]string{"id": row.id, "working_directory": row.cwd}, "event": map[string]string{"action": "prompt.submitted"}, "prompt": map[string]string{"text": "log title"}}
		if err := json.NewEncoder(file).Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	sources := []Source{listedSource{harness: HarnessClaude, sessions: []Session{
		{Harness: HarnessClaude, ID: "shared", Title: "store title", Directory: project, UpdatedAt: time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)},
		{Harness: HarnessClaude, ID: "child", Directory: project, Subagent: true, UpdatedAt: time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC)},
	}}}
	for _, tc := range []struct {
		name   string
		filter Filter
		want   []string
	}{
		{"directory", Filter{Directory: project}, []string{"gemini_cli/shared", "kiro/nested", "claude_code/shared"}},
		{"limit after merge", Filter{Directory: project, Limit: 1}, []string{"gemini_cli/shared"}},
		{"harness", Filter{Harness: HarnessClaude}, []string{"claude_code/shared"}},
		{"include subagents", Filter{Harness: HarnessClaude, IncludeSubagents: true}, []string{"claude_code/child", "claude_code/shared"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions, err := ListWithLog(sources, logPath, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, s := range sessions {
				got = append(got, s.Harness+"/"+s.ID)
				if s.Harness == HarnessClaude && s.ID == "shared" && s.Title != "store title" {
					t.Fatalf("store lost precedence: %+v", s)
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestListWithLogKeepsStoreErrorsAndPartialResults(t *testing.T) {
	sentinel := errors.New("unreadable store")
	source := listedSource{harness: HarnessClaude, sessions: []Session{{Harness: HarnessClaude, ID: "partial"}}, err: sentinel}
	sessions, err := ListWithLog([]Source{source}, filepath.Join(t.TempDir(), "absent"), Filter{})
	if !errors.Is(err, sentinel) || len(sessions) != 1 || sessions[0].ID != "partial" {
		t.Fatalf("sessions=%+v err=%v", sessions, err)
	}
}

func TestListWithLogKeepsStoreSessionsWhenLogCannotBeRead(t *testing.T) {
	source := listedSource{harness: HarnessClaude, sessions: []Session{{Harness: HarnessClaude, ID: "stored"}}}
	sessions, err := ListWithLog([]Source{source}, t.TempDir(), Filter{})
	if err == nil || len(sessions) != 1 || sessions[0].ID != "stored" {
		t.Fatalf("sessions=%+v err=%v", sessions, err)
	}
}
