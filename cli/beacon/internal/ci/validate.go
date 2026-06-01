package ci

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

type ValidationOptions struct {
	LogPath        string
	MinEvents      int
	RequireHarness string
	Since          time.Time
}

type ValidationStage struct {
	Name     string `json:"name"`
	Target   string `json:"target,omitempty"`
	Status   string `json:"status"`
	Severity string `json:"severity"`
	Message  string `json:"message,omitempty"`
	Evidence string `json:"evidence,omitempty"`
}

type ValidationResult struct {
	Status      string            `json:"status"`
	LogPath     string            `json:"log_path"`
	EventCount  int               `json:"event_count"`
	Stages      []ValidationStage `json:"stages"`
	GeneratedAt string            `json:"generated_at"`
}

func Validate(opts ValidationOptions) ValidationResult {
	if opts.MinEvents <= 0 {
		opts.MinEvents = DefaultValidationMin
	}
	result := ValidationResult{
		LogPath:     opts.LogPath,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}
	info, err := os.Stat(opts.LogPath)
	if err != nil {
		result.Stages = append(result.Stages, ValidationStage{Name: "runtime_log_exists", Target: opts.LogPath, Status: "fail", Severity: "high", Message: err.Error(), Evidence: "stat_failed"})
		result.Status = aggregateValidationStatus(result.Stages)
		return result
	}
	if info.IsDir() {
		result.Stages = append(result.Stages, ValidationStage{Name: "runtime_log_exists", Target: opts.LogPath, Status: "fail", Severity: "high", Message: "runtime log path is a directory", Evidence: "path_is_directory"})
		result.Status = aggregateValidationStatus(result.Stages)
		return result
	}
	result.Stages = append(result.Stages, ValidationStage{Name: "runtime_log_exists", Target: opts.LogPath, Status: "ok", Severity: "info", Evidence: "file_exists"})

	events, malformed := readStructuredEvents(opts.LogPath, opts.Since)
	if len(malformed) > 0 {
		result.Stages = append(result.Stages, ValidationStage{Name: "runtime_log_parseable", Target: opts.LogPath, Status: "fail", Severity: "high", Message: malformed[0], Evidence: fmt.Sprintf("malformed_lines=%d", len(malformed))})
		result.Status = aggregateValidationStatus(result.Stages)
		return result
	}
	result.Stages = append(result.Stages, ValidationStage{Name: "runtime_log_parseable", Target: opts.LogPath, Status: "ok", Severity: "info", Evidence: "jsonl_parse_succeeded"})

	filtered := filterHarness(events, opts.RequireHarness)
	result.EventCount = len(filtered)
	if result.EventCount < opts.MinEvents {
		target := opts.LogPath
		if opts.RequireHarness != "" {
			target = opts.RequireHarness
		}
		result.Stages = append(result.Stages, ValidationStage{Name: "event_count", Target: target, Status: "fail", Severity: "high", Message: fmt.Sprintf("observed %d events, want at least %d", result.EventCount, opts.MinEvents), Evidence: fmt.Sprintf("events=%d", result.EventCount)})
		result.Status = aggregateValidationStatus(result.Stages)
		return result
	}
	result.Stages = append(result.Stages, ValidationStage{Name: "event_count", Target: opts.LogPath, Status: "ok", Severity: "info", Message: fmt.Sprintf("observed %d events", result.EventCount), Evidence: fmt.Sprintf("events=%d", result.EventCount)})
	if opts.RequireHarness != "" {
		result.Stages = append(result.Stages, ValidationStage{Name: "harness_events", Target: opts.RequireHarness, Status: "ok", Severity: "info", Message: "required harness events observed"})
	}
	result.Status = aggregateValidationStatus(result.Stages)
	return result
}

func readStructuredEvents(path string, since time.Time) ([]schema.Event, []string) {
	file, err := os.Open(path)
	if err != nil {
		return nil, []string{err.Error()}
	}
	defer file.Close()
	var events []schema.Event
	var malformed []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0
	filtering := !since.IsZero()
	for scanner.Scan() {
		lineNo++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event schema.Event
		if err := json.Unmarshal(line, &event); err != nil {
			if filtering {
				continue
			}
			malformed = append(malformed, fmt.Sprintf("line %d: %v", lineNo, err))
			continue
		}
		if filtering {
			ts, err := time.Parse(time.RFC3339, event.Timestamp)
			if err != nil || ts.Before(since) {
				continue
			}
		}
		if err := event.Validate(); err != nil {
			malformed = append(malformed, fmt.Sprintf("line %d: %v", lineNo, err))
			continue
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		malformed = append(malformed, err.Error())
	}
	return events, malformed
}

func filterHarness(events []schema.Event, harness string) []schema.Event {
	harness = strings.TrimSpace(harness)
	if harness == "" {
		return events
	}
	var out []schema.Event
	for _, event := range events {
		if harnessMatches(event.Harness.Name, harness) {
			out = append(out, event)
		}
	}
	return out
}

func harnessMatches(got, want string) bool {
	switch want {
	case "claude":
		return got == "claude" || got == "claude_code"
	case "claude_code":
		return got == "claude" || got == "claude_code"
	default:
		return got == want
	}
}

func aggregateValidationStatus(stages []ValidationStage) string {
	status := "ok"
	for _, stage := range stages {
		switch stage.Status {
		case "fail":
			return "fail"
		case "warn":
			status = "warn"
		}
	}
	return status
}
