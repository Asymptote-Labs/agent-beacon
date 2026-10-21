package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/learning"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestMemoryEvaluationSelectionKeepsCompleteTrace(t *testing.T) {
	for _, count := range []int{0, 1, 2000, 2001} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("events_%d_explicit_%t", count, explicit), func(t *testing.T) {
				logPath, project := writeEvaluationTraceFixture(t, count)
				resetMemoryOpts(t)
				memoryOpts.logPath = logPath
				memoryOpts.projectPath = project
				if explicit {
					memoryOpts.traceID = "session:cursor:evaluation-session"
				}
				inputs, err := selectedEvaluationInputs()
				if count == 0 {
					if explicit {
						if err == nil || !strings.Contains(err.Error(), "trace not found") {
							t.Fatalf("missing trace error = %v", err)
						}
					} else if err != nil || len(inputs) != 0 {
						t.Fatalf("empty selection = %v, error = %v", inputs, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(inputs) != 1 {
					t.Fatalf("selected %d traces, want one", len(inputs))
				}
				show := inputs[0].Trace
				if len(show.Events) != count || show.Range.TotalEvents != count || show.Range.ReturnedEvents != count {
					t.Errorf("selected %d of %d events (returned %d), want the complete %d", len(show.Events), show.Range.TotalEvents, show.Range.ReturnedEvents, count)
				}
				projection := learning.BuildProjection(show)
				last := projection.Events[len(projection.Events)-1]
				if last.Number != count || last.Content != "Final outcome: all tests passed." {
					t.Errorf("last projected event = %+v, want event %d with the final answer", last, count)
				}
				wantProjected := count
				if count > 80 {
					wantProjected = 81
					if projection.Events[0].Number != 1 || projection.Events[39].Number != 40 || projection.Events[41].Number != count-39 {
						t.Errorf("projection did not retain the first and last 40 events: first=%d head-end=%d tail-start=%d", projection.Events[0].Number, projection.Events[39].Number, projection.Events[41].Number)
					}
					omitted := projection.Events[40]
					wantOmitted := fmt.Sprintf("%d events omitted from the middle of this trace", count-80)
					if omitted.Type != "omitted" || omitted.Summary != wantOmitted {
						t.Errorf("omission marker = %+v, want %q", omitted, wantOmitted)
					}
				}
				if len(projection.Events) != wantProjected {
					t.Errorf("projected %d events, want %d", len(projection.Events), wantProjected)
				}
			})
		}
	}
}

func TestMemoryEvaluationSelectionPreservesFiltersAndProject(t *testing.T) {
	logPath, _ := writeEvaluationTraceFixture(t, 2001)
	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, extra := range []struct {
		id, harness, timestamp, prompt string
	}{
		{"different-query", "cursor", "2026-10-01T00:00:00Z", "Unrelated task"},
		{"outside-time", "cursor", "2026-09-01T00:00:00Z", "Evaluation target task"},
		{"other-harness", "claude_code", "2026-10-01T00:00:00Z", "Evaluation target task"},
	} {
		event := evaluationTraceEvent(extra.harness, extra.id, extra.timestamp, 0)
		event.Prompt.Text = extra.prompt
		if err := json.NewEncoder(file).Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	resetMemoryOpts(t)
	memoryOpts.logPath = logPath
	memoryOpts.query = "Evaluation target"
	memoryOpts.harness = "cursor"
	memoryOpts.since = "2026-10-01T00:00:00Z"
	memoryOpts.until = "2026-10-02T00:00:00Z"
	memoryOpts.projectPath = t.TempDir()
	inputs, err := selectedEvaluationInputs()
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 || inputs[0].Trace.Trace.ID != "session:cursor:evaluation-session" {
		t.Fatalf("selected traces = %+v, want only the matching session", inputs)
	}
	project, err := learning.ResolveProject(memoryOpts.projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if inputs[0].Project.ID != project.ID || len(inputs[0].Trace.Events) != 2001 {
		t.Fatalf("selected project = %+v, events = %d; want pinned project %+v and complete trace", inputs[0].Project, len(inputs[0].Trace.Events), project)
	}
}

func writeEvaluationTraceFixture(t *testing.T, count int) (string, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	var lines bytes.Buffer
	for i := 0; i < count; i++ {
		at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		event := evaluationTraceEvent("cursor", "evaluation-session", at, i)
		event.Session.WorkingDirectory, event.Repository = dir, dir
		if i == count-1 {
			event.Event.Action, event.Event.Category = "agent.message", "agent"
			event.Prompt = nil
			event.GenAI = &schema.GenAIInfo{Output: &schema.GenAIOutputInfo{Messages: asymptoteobserve.TextOutputMessages("Final outcome: all tests passed.")}}
			event.Message = "Agent response"
		}
		if err := json.NewEncoder(&lines).Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(logPath, lines.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return logPath, dir
}

func evaluationTraceEvent(harness, sessionID, timestamp string, number int) schema.Event {
	return schema.Event{
		Timestamp: timestamp, Vendor: schema.Vendor, Product: schema.Product, SchemaVersion: schema.SchemaVersion,
		Event:    schema.EventInfo{ID: fmt.Sprintf("%s-event-%d", sessionID, number), Kind: "agent_runtime", Action: "prompt.submitted", Category: "prompt"},
		Severity: schema.SeverityInfo,
		Harness:  schema.HarnessInfo{Name: harness},
		Session:  &schema.SessionInfo{ID: sessionID},
		Prompt:   &schema.PromptInfo{Text: "Evaluation target task"},
		Message:  "Prompt submitted",
	}
}
