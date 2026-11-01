package beaconjsonexporter

import (
	"encoding/json"
	"fmt"
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
	if len(data) > w.maxEventBytes {
		event.Raw = nil
		event.Message = asymptoteobserve.TruncateString(event.Message, 1024)
		event.Truncated = true
		data, err = json.Marshal(event)
		if err != nil {
			return err
		}
	}
	if len(data) > w.maxEventBytes {
		// Retained content is the next thing to go after raw, mirroring the hook
		// writer's compactEndpointContent. Before this path existed the only step
		// past dropping raw was the hard error below, which was survivable while
		// no OTLP event carried a turn's text and is not now that they do.
		compactRetainedContent(&event)
		data, err = json.Marshal(event)
		if err != nil {
			return err
		}
	}
	if len(data) > w.maxEventBytes {
		return fmt.Errorf("event exceeds maximum size after truncation: %d bytes", len(data))
	}
	// Stamped last, over the bytes that are actually about to be written, so a
	// size fallback above cannot leave the ID describing an event that no longer
	// exists. Marshalling twice is the cost of an identity derived from the
	// event rather than asserted about it. An event already at the size limit
	// keeps its bytes and goes without one.
	if event.Event.ID == "" {
		event.Event.ID = asymptoteobserve.EventIDForLine(data)
		stamped, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if len(stamped) <= w.maxEventBytes {
			data = stamped
		}
	}
	_, dirMode, _ := asymptoteobserve.RuntimeLogModes(w.path)
	if err := os.MkdirAll(filepath.Dir(w.path), dirMode); err != nil {
		return err
	}
	return appendJSONL(w.path, append(data, '\n'), w.rotateBytes, w.rotateArchives)
}

// compactRetainedContent drops an event's retained text and records that it did,
// so a reader can still tell what the event was about and that its content was
// held back rather than never captured. The hash and byte count in the content
// marker describe the original text and stay put.
func compactRetainedContent(event *beaconEvent) {
	if event.File != nil {
		event.File.Diff = ""
	}
	if event.Command != nil {
		event.Command.Output = ""
	}
	if event.Prompt != nil {
		event.Prompt.Text = ""
	}
	if event.GenAI != nil {
		event.GenAI.Input = nil
		event.GenAI.Output = nil
		if event.GenAI.Tool != nil && event.GenAI.Tool.Call != nil {
			event.GenAI.Tool.Call.Arguments = nil
			event.GenAI.Tool.Call.Result = nil
		}
		// An event whose only gen_ai content was the turn text drops the object
		// rather than writing "gen_ai":{}.
		if beaconevent.IsZeroJSON(event.GenAI) {
			event.GenAI = nil
		}
	}
	if event.Content != nil {
		event.Content.Included = false
		event.Content.Truncated = true
	}
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
	if event.MCP != nil {
		event.MCP = asymptoteobserve.SanitizeMCP(event.MCP, w.privacyOptions())
	}
	if event.GenAI != nil {
		event.GenAI = asymptoteobserve.SanitizeGenAI(event.GenAI, w.privacyOptions())
	}
	if event.Raw != nil {
		event.Raw = w.sanitizeMap(event.Raw)
	}
	if data, err := json.Marshal(event); err == nil && len(data) > w.maxEventBytes {
		event.Truncated = true
	}
	return event
}

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
