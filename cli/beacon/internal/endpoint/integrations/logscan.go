package integrations

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

type CodexUsageSignals struct {
	SessionContexts int
	TurnSpans       int
	LegacyMetrics   int
}

// ScanCodexUsageSignals summarizes which Codex attribution source has reached
// the active runtime log. It reads metadata only; prompt and tool content are
// never decoded.
func ScanCodexUsageSignals(logPath string) CodexUsageSignals {
	var signals CodexUsageSignals
	if logPath == "" {
		return signals
	}
	file, err := os.Open(logPath)
	if err != nil {
		return signals
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var event struct {
			Event struct {
				Action string `json:"action"`
			} `json:"event"`
			Harness struct {
				Name string `json:"name"`
			} `json:"harness"`
			Raw map[string]interface{} `json:"raw"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil ||
			asymptoteobserve.NormalizeHarnessName(event.Harness.Name) != "codex_cli" {
			continue
		}
		if event.Event.Action == "session.context" {
			signals.SessionContexts++
		}
		if source, _ := event.Raw["source"].(string); source == "codex_turn_span" {
			signals.TurnSpans++
		}
		if metric, _ := event.Raw["metric_name"].(string); metric == "codex.turn.token_usage" {
			signals.LegacyMetrics++
		}
	}
	return signals
}

func HasRecentHarnessEvent(logPath, harnessName string) bool {
	_, ok := LastHarnessEvent(logPath, harnessName)
	return ok
}

func HasHarnessEventSince(logPath, harnessName string, since time.Time) bool {
	last, ok := LastHarnessEvent(logPath, harnessName)
	if !ok || last.IsZero() {
		return false
	}
	return !last.Before(since)
}

func LastHarnessEvent(logPath, harnessName string) (time.Time, bool) {
	return lastHarnessEvent(logPath, harnessName, "")
}

// LastHarnessEventByCollectionMethod is LastHarnessEvent restricted to events whose
// harness.collection_method is method.
//
// The distinction matters for a runtime with two capture paths. DeepSeek Harness has live hooks
// and an offline poll of its session store, and both write events named deepseek_harness, so a
// log full of backfilled poll events answers "has this harness been observed?" with yes while
// every live hook is failing to write (#605). Asking for hook events alone is what separates the
// two.
func LastHarnessEventByCollectionMethod(logPath, harnessName, method string) (time.Time, bool) {
	if strings.TrimSpace(method) == "" {
		return time.Time{}, false
	}
	return lastHarnessEvent(logPath, harnessName, method)
}

// FirstEventTime returns the timestamp of the first event in logPath that carries a parseable one.
// For a rotated archive that is the start of the history the file holds. Only the head of the file
// is read.
func FirstEventTime(logPath string) (time.Time, bool) {
	if logPath == "" {
		return time.Time{}, false
	}
	file, err := os.Open(logPath)
	if err != nil {
		return time.Time{}, false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for lines := 0; lines < 100 && scanner.Scan(); lines++ {
		var event struct {
			Timestamp string `json:"timestamp"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339Nano, event.Timestamp); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func lastHarnessEvent(logPath, harnessName, method string) (time.Time, bool) {
	if logPath == "" || strings.TrimSpace(harnessName) == "" {
		return time.Time{}, false
	}
	file, err := os.Open(logPath)
	if err != nil {
		return time.Time{}, false
	}
	defer file.Close()

	// Compared canonically on both sides, because the two names come from different places and do
	// not agree on spelling. Discovery names a runtime for the CLI surface -- "vscode" is what
	// `--harness vscode` accepts -- while events carry the canonical telemetry name, which for that
	// runtime is "vscode_copilot". An exact comparison therefore asks whether two unrelated
	// vocabularies happen to coincide, and it silently answers "no" for a runtime that is working
	// perfectly: doctor's harness_observed never clears, and the operator is told to run an agent
	// they have already been running.
	//
	// This was wrong in both directions before it was normalized. Claude Code discovers as
	// "claude_code" while its hooks emitted "claude", so its observed check never cleared either.
	want := asymptoteobserve.NormalizeHarnessName(harnessName)
	var last time.Time
	found := false
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var event map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if harness, ok := event["harness"].(map[string]interface{}); ok {
			if name, _ := harness["name"].(string); asymptoteobserve.NormalizeHarnessName(name) == want {
				if method != "" {
					if got, _ := harness["collection_method"].(string); got != method {
						continue
					}
				}
				found = true
				if ts, ok := event["timestamp"].(string); ok {
					if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil && parsed.After(last) {
						last = parsed
					}
				}
			}
		}
	}
	return last, found
}
