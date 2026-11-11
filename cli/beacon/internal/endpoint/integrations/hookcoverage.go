package integrations

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// HookCoverage finds, per session id, the earliest hook event the retained runtime log holds for
// any of harnesses (canonical names, compared after NormalizeHarnessName). A session collector
// whose runtime also has live hooks takes it as its sessionsync.LiveCoverage, so a poll sweep skips
// what the hooks already recorded and keeps only what the hook payload cannot carry.
//
// Every retained archive is read because coverage can sit several rotations back on a busy
// machine; a missing file is simply not evidence. The scan decodes only the three fields it
// needs, so a runtime.jsonl of any size costs a line at a time, and only lines containing marker
// are unmarshalled.
func HookCoverage(logPath, marker string, harnesses ...string) map[string]time.Time {
	coverage := map[string]time.Time{}
	for _, path := range writer.RetainedLogPaths(logPath) {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			line := scanner.Bytes()
			if !bytes.Contains(line, []byte(marker)) {
				continue
			}
			var event struct {
				Timestamp string `json:"timestamp"`
				Harness   struct {
					Name             string `json:"name"`
					CollectionMethod string `json:"collection_method"`
				} `json:"harness"`
				Session struct {
					ID string `json:"id"`
				} `json:"session"`
			}
			if json.Unmarshal(line, &event) != nil {
				continue
			}
			name := asymptoteobserve.NormalizeHarnessName(event.Harness.Name)
			if !slices.Contains(harnesses, name) || event.Harness.CollectionMethod != asymptoteobserve.CollectionMethodHook {
				continue
			}
			sessionID := strings.TrimSpace(event.Session.ID)
			if sessionID == "" {
				continue
			}
			ts, err := asymptoteobserve.ParseTimestamp(event.Timestamp)
			if err != nil {
				continue
			}
			if earliest, ok := coverage[sessionID]; !ok || ts.Before(earliest) {
				coverage[sessionID] = ts
			}
		}
		_ = f.Close()
	}
	if len(coverage) == 0 {
		return nil
	}
	return coverage
}
