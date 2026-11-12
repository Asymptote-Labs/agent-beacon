package beaconjsonexporter

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/collector-builder/exporter/beaconjsonexporter/internal/beaconevent"
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

func (w jsonlWriter) sanitize(event beaconEvent) beaconEvent {
	event.Message = w.cleanString(event.Message, asymptoteobserve.DefaultStringLimit)
	if event.Tool != nil {
		event.Tool.Command = w.cleanString(event.Tool.Command, asymptoteobserve.DefaultStringLimit)
		event.Tool.Path = asymptoteobserve.TruncateString(event.Tool.Path, asymptoteobserve.DefaultRawStringLimit)
	}
	if event.Command != nil {
		event.Command.Command = w.cleanString(event.Command.Command, asymptoteobserve.DefaultStringLimit)
		event.Command.Output = w.cleanString(event.Command.Output, asymptoteobserve.DefaultStringLimit)
	}
	// Mirrors SanitizeEvent: retained bulk content gets the prompt-text limit and
	// secret redaction, not the shorter raw-attribute limit.
	if event.File != nil {
		event.File.Diff = w.cleanString(event.File.Diff, asymptoteobserve.DefaultStringLimit)
	}
	if event.Approval != nil {
		event.Approval.Reason = w.cleanString(event.Approval.Reason, asymptoteobserve.DefaultStringLimit)
	}
	if event.Policy != nil {
		event.Policy.Reason = w.cleanString(event.Policy.Reason, asymptoteobserve.DefaultStringLimit)
	}
	if event.Prompt != nil {
		event.Prompt.Text = w.cleanString(event.Prompt.Text, asymptoteobserve.DefaultStringLimit)
	}
	// Mirrors SanitizeEvent: the run block and the top-level model, repository and branch come from
	// OTLP resource attributes and the CI environment, where a pull request names its own branch.
	event.Run = asymptoteobserve.SanitizeRun(event.Run, w.privacyOptions())
	event.Model = w.cleanString(event.Model, asymptoteobserve.DefaultRawStringLimit)
	event.Repository = w.cleanString(event.Repository, asymptoteobserve.DefaultRawStringLimit)
	event.Branch = w.cleanString(event.Branch, asymptoteobserve.DefaultRawStringLimit)
	if event.MCP != nil {
		event.MCP = asymptoteobserve.SanitizeMCP(event.MCP, w.privacyOptions())
	}
	browserResponse := event.GenAI != nil && event.GenAI.Output != nil && browserChatHarnesses[event.Harness.Name]
	if event.GenAI != nil {
		output := event.GenAI.Output
		event.GenAI = asymptoteobserve.SanitizeGenAI(event.GenAI, w.privacyOptions())
		if browserResponse {
			opts := w.privacyOptions()
			opts.StringLimit = browserResponseStringLimit
			event.GenAI.Output = asymptoteobserve.SanitizeGenAI(&beaconevent.GenAIInfo{Output: output}, opts).Output
		}
	}
	if event.Raw != nil {
		event.Raw = w.sanitizeMap(event.Raw)
		// The answer is in gen_ai.output.messages already, so the raw copy would only add its first
		// 2 KB to the line again. SanitizeMap returned new maps, so this leaves the caller's alone.
		if attrs, ok := event.Raw["attributes"].(map[string]interface{}); ok && browserResponse {
			delete(attrs, "gen_ai.output.messages")
		}
	}
	if data, err := json.Marshal(event); err == nil && len(data) > w.maxEventBytes {
		event.Truncated = true
	}
	return event
}

// A claude.ai or chatgpt.com answer is what a browser chat event is about, and most run past the
// 2 KB raw-attribute limit the rest of gen_ai keeps, so the log keeps up to 16 KB of it. A coding
// agent's reply rarely needs more, and its OTLP gen_ai fields can carry whole message histories.
const browserResponseStringLimit = 16 * 1024

var browserChatHarnesses = map[string]bool{"claude_web": true, "chatgpt_web": true}

func (w jsonlWriter) sanitizeMap(input map[string]interface{}) map[string]interface{} {
	return asymptoteobserve.SanitizeMap(input, w.privacyOptions())
}

func (w jsonlWriter) privacyOptions() asymptoteobserve.PrivacyOptions {
	return asymptoteobserve.PrivacyOptions{
		RedactSecrets: w.redactSecrets,
		StringLimit:   asymptoteobserve.DefaultRawStringLimit,
	}
}

func (w jsonlWriter) sanitizeSlice(input []interface{}) []interface{} {
	return asymptoteobserve.SanitizeSlice(input, w.privacyOptions())
}

func (w jsonlWriter) cleanString(value string, limit int) string {
	return asymptoteobserve.CleanString(value, limit, w.redactSecrets)
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
