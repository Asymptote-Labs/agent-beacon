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
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// HookCoverage finds, per session id, the earliest hook event the retained runtime log holds for
// any of harnesses (canonical names, compared after NormalizeHarnessName), and the content hash of
// every prompt those hooks recorded. A session collector whose runtime also has live hooks takes it
// as its sessionsync.LiveCoverage, so a poll sweep skips what the hooks already recorded and keeps
// only what the hook payload cannot carry.
//
// Every retained archive is read because coverage can sit several rotations back on a busy
// machine; a missing file is simply not evidence. The scan decodes only the fields it needs, so a
// runtime.jsonl of any size costs a line at a time, and only lines containing marker are
// unmarshalled.
func HookCoverage(logPath, marker string, harnesses ...string) sessionsync.LiveCoverage {
	var coverage sessionsync.LiveCoverage
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
				Event     struct {
					Action string `json:"action"`
				} `json:"event"`
				Harness struct {
					Name             string `json:"name"`
					CollectionMethod string `json:"collection_method"`
				} `json:"harness"`
				Session struct {
					ID string `json:"id"`
				} `json:"session"`
				Content struct {
					Hash string `json:"hash"`
				} `json:"content"`
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
			if coverage.From == nil {
				coverage.From = map[string]time.Time{}
			}
			if earliest, ok := coverage.From[sessionID]; !ok || ts.Before(earliest) {
				coverage.From[sessionID] = ts
			}
			if event.Event.Action == "prompt.submitted" && event.Content.Hash != "" {
				if coverage.Prompts == nil {
					coverage.Prompts = map[string]map[string]bool{}
				}
				if coverage.Prompts[sessionID] == nil {
					coverage.Prompts[sessionID] = map[string]bool{}
				}
				coverage.Prompts[sessionID][event.Content.Hash] = true
			}
		}
		_ = f.Close()
	}
	return coverage
}
