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

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/learning"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// A trace longer than 2,000 events must reach memory with its real final event.
func TestMemoryReadsTheEndOfLongTraces(t *testing.T) {
	const count = 2001
	const finalEventID = "evaluation-session-event-2000"
	for _, route := range []string{"evaluate --trace", "evaluate list", "candidates create"} {
		t.Run(route, func(t *testing.T) {
			logPath, project := writeEvaluationTraceFixture(t, count)
			resetMemoryOpts(t)
			memoryOpts.logPath, memoryOpts.projectPath = logPath, project
			if route != "evaluate list" {
				memoryOpts.traceID = "session:cursor:evaluation-session"
			}
			if route == "candidates create" {
				memoryOpts.kind = asymptoteobserve.LearningMemoryKindDebuggingPattern
				memoryOpts.title = "Long trace lesson"
				memoryOpts.bodyFile = "-"
				memoryOpts.jsonOutput = true
				var out bytes.Buffer
				cmd := &cobra.Command{}
				cmd.SetOut(&out)
				cmd.SetIn(strings.NewReader("Lesson body.\n"))
				if err := runMemoryCandidatesCreate(cmd, nil); err != nil {
					t.Fatal(err)
				}
				var candidate asymptoteobserve.LearningCandidateV1
				if err := json.Unmarshal(out.Bytes(), &candidate); err != nil {
					t.Fatal(err)
				}
				ids := candidate.Evidence[0].EventIDs
				if len(ids) != count || ids[len(ids)-1] != finalEventID {
					t.Fatalf("evidence has %d events ending %q, want %d ending %q", len(ids), ids[len(ids)-1], count, finalEventID)
				}
				return
			}
			inputs, err := selectedEvaluationInputs()
			if err != nil {
				t.Fatal(err)
			}
			if len(inputs) != 1 {
				t.Fatalf("selected %d traces, want one", len(inputs))
			}
			projection := learning.BuildProjection(inputs[0].Trace)
			last := projection.Events[len(projection.Events)-1]
			if last.Number != count || last.Content != "Final outcome: all tests passed." {
				t.Fatalf("last projected event = %+v, want event %d with the final answer", last, count)
			}
		})
	}
}

func writeEvaluationTraceFixture(t *testing.T, count int) (string, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	var lines bytes.Buffer
	for i := range count {
		event := schema.Event{
			Timestamp: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second).Format(time.RFC3339),
			Vendor:    schema.Vendor, Product: schema.Product, SchemaVersion: schema.SchemaVersion,
			Event:      schema.EventInfo{ID: fmt.Sprintf("evaluation-session-event-%d", i), Kind: "agent_runtime", Action: "prompt.submitted", Category: "prompt"},
			Severity:   schema.SeverityInfo,
			Harness:    schema.HarnessInfo{Name: "cursor"},
			Session:    &schema.SessionInfo{ID: "evaluation-session", WorkingDirectory: dir},
			Repository: dir,
			Prompt:     &schema.PromptInfo{Text: "Evaluation target task"},
			Message:    "Prompt submitted",
		}
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
