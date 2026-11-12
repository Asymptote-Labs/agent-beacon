package beaconjsonexporter

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/collector-builder/exporter/beaconjsonexporter/internal/runtimelog"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

type jsonlWriter struct {
	path           string
	maxEventBytes  int
	rotateBytes    int64
	rotateArchives int
	redactSecrets  bool
}

func (w jsonlWriter) append(event beaconEvent) error {
	event = w.sanitize(event)
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	var stamp func(id string) ([]byte, error)
	if event.Event.ID == "" {
		stamp = func(id string) ([]byte, error) {
			event.Event.ID = id
			return json.Marshal(event)
		}
	}
	data, err = asymptoteobserve.FitEventLine(data, w.maxEventBytes, stamp)
	if err != nil {
		return err
	}
	_, dirMode, _ := asymptoteobserve.RuntimeLogModes(w.path)
	if err := os.MkdirAll(filepath.Dir(w.path), dirMode); err != nil {
		return err
	}
	return appendJSONL(w.path, append(data, '\n'), w.rotateBytes, w.rotateArchives)
}

// sanitize is asymptoteobserve.SanitizeEventWith with the exporter's choices: secret redaction as
// configured (redact_secrets), and a browser chat answer kept to browserResponseStringLimit with its
// duplicate in raw dropped.
func (w jsonlWriter) sanitize(event beaconEvent) beaconEvent {
	opts := asymptoteobserve.EventSanitizeOptions{RedactSecrets: w.redactSecrets}
	if event.GenAI != nil && event.GenAI.Output != nil && browserChatHarnesses[event.Harness.Name] {
		opts.GenAIOutputLimit = browserResponseStringLimit
		event.Raw = withoutRawAnswer(event.Raw)
	}
	event.Base = asymptoteobserve.SanitizeEventWith(event.Base, w.maxEventBytes, opts)
	return event
}

// A claude.ai or chatgpt.com answer is what a browser chat event is about, and most run past the
// 2 KB raw-attribute limit the rest of gen_ai keeps, so the log keeps up to 16 KB of it. A coding
// agent's reply rarely needs more, and its OTLP gen_ai fields can carry whole message histories.
const browserResponseStringLimit = 16 * 1024

var browserChatHarnesses = map[string]bool{"claude_web": true, "chatgpt_web": true}

// withoutRawAnswer is raw without the answer's copy in its attributes: the answer is in
// gen_ai.output.messages already, so the raw copy would only add its first 2 KB to the line again.
// It copies what it changes and leaves the caller's maps alone. It runs before sanitizing, so the
// field_truncated decision sees the line as it will be written.
func withoutRawAnswer(raw map[string]interface{}) map[string]interface{} {
	attrs, ok := raw["attributes"].(map[string]interface{})
	if !ok {
		return raw
	}
	if _, ok := attrs["gen_ai.output.messages"]; !ok {
		return raw
	}
	trimmed := make(map[string]interface{}, len(attrs))
	for key, value := range attrs {
		if key != "gen_ai.output.messages" {
			trimmed[key] = value
		}
	}
	out := make(map[string]interface{}, len(raw))
	for key, value := range raw {
		out[key] = value
	}
	out["attributes"] = trimmed
	return out
}

// In system mode the collector runs as root, while agent hooks append to the
// same runtime log as the console user, so the shared runtime files must stay
// writable by both regardless of which process creates them first. A user-mode
// collector writes a log inside its user's home, which is private (0600 in a
// 0700 directory): asymptoteobserve.RuntimeLogModes decides from the path,
// identically in all three writers.
const runtimeFileMode = asymptoteobserve.SharedRuntimeLogFileMode

// Keep this rotation and file-mode contract mirrored with the endpoint CLI and hook writer. The
// implementation lives in internal/runtimelog so the beacon_ingest extension appends through the
// same lock, rotation and file-mode handling as the exporter.
func appendJSONL(path string, line []byte, rotateBytes int64, rotateArchives int) error {
	return runtimelog.Append(path, line, rotateBytes, rotateArchives)
}
