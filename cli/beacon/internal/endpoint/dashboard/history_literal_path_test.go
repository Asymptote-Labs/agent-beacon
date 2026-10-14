package dashboard

import (
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"os"
	"path/filepath"
	"testing"
)

func TestHistoryOpensLiteralURICharacters(t *testing.T) {
	for _, name := range []string{"plain", "project#draft", "project%25name"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name, "history.db")
			t.Setenv(endpointconfig.HistoryStoreEnv, path)
			e := testSchemaEvent("2026-10-13T12:00:00Z", "claude_code", "prompt.submitted", "prompt", "")
			e.Session = &schema.SessionInfo{ID: "literal-path"}
			log := newTestLog(t, marshalEvents(t, e))
			got, err := EnableHistoryStore(log, HistoryOptions{RetentionDays: 36500})
			if err != nil {
				t.Fatal(err)
			}
			if !got.Enabled || got.Path != path || got.Traces != 1 || got.Events != 1 {
				t.Fatalf("history: %+v", got)
			}
			info, err := os.Stat(path)
			if err != nil || info.Size() == 0 {
				t.Fatalf("literal database not populated: stat%v info%v", err, info)
			}
			again, err := TraceStoreStatus(log)
			if err != nil || again.Traces != 1 || again.Events != 1 {
				t.Fatalf("reopen: %+v %v", again, err)
			}
		})
	}
}
