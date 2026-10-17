package logging

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/activesession"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/shareauto"
)

// A share request that cannot be written takes its dedupe mark back, so the next hook of the
// session asks again and exactly one request is written once the log takes it.
func TestShareRequestMarkIsRolledBackWhenTheWriteFails(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	recordPath := filepath.Join(dir, activesession.FileName)
	settingsPath := filepath.Join(dir, "share.json")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv(activesession.PathEnv, recordPath)
	t.Setenv(shareauto.PathEnv, settingsPath)
	var settings shareauto.Settings
	settings.SetGlobal(shareauto.GlobalOn)
	if err := shareauto.Save(settingsPath, settings); err != nil {
		t.Fatal(err)
	}
	failing := true
	prev := writeEndpointJSON
	writeEndpointJSON = func(path string, event map[string]interface{}) error {
		if info, _ := event["event"].(map[string]interface{}); failing && info["action"] == asymptoteobserve.ActionShareRequested {
			return errors.New("read-only file system")
		}
		return prev(path, event)
	}
	t.Cleanup(func() { writeEndpointJSON = prev })

	logger := &Logger{hookName: "test", platform: "claude"}
	hook := func() {
		t.Helper()
		fields := map[string]interface{}{"session": map[string]interface{}{"id": "sess-1", "working_directory": dir}}
		_ = captureStderr(t, func() {
			if err := logger.EndpointEvent("prompt.submitted", "prompt", "info", "prompt", fields); err != nil {
				t.Fatalf("hook event: %v", err)
			}
		})
	}
	hook()
	var record struct {
		Sessions       []json.RawMessage `json:"sessions"`
		ShareRequested []json.RawMessage `json:"share_requested"`
	}
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if len(record.Sessions) == 0 || len(record.ShareRequested) != 0 {
		t.Fatalf("record = %s, want the session noted and no share-request mark", data)
	}
	if got := countActions(t, logPath, asymptoteobserve.ActionShareRequested); got != 0 {
		t.Fatalf("share requests while failing = %d", got)
	}
	failing = false
	hook()
	hook()
	if got := countActions(t, logPath, asymptoteobserve.ActionShareRequested); got != 1 {
		t.Fatalf("share requests = %d, want exactly one", got)
	}
}

func countActions(t *testing.T, path, action string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var event map[string]interface{}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if info, _ := event["event"].(map[string]interface{}); info["action"] == action {
			n++
		}
	}
	return n
}
