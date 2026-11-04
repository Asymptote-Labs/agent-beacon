package cmd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
)

func TestTracePlainMetadataRowsPreserveRawContent(t *testing.T) {
	for _, text := range []string{"valuable first line\r\nsecond\tcolumn"} {
		t.Run(text, func(t *testing.T) {
			logPath, _, _ := traceHistoryFixture(t, false, "")
			data, err := json.Marshal(map[string]interface{}{
				"timestamp": "2026-10-12T10:00:00Z",
				"event":     map[string]interface{}{"id": "e1", "action": "prompt.submitted", "category": "prompt"},
				"harness":   map[string]interface{}{"name": "cursor"},
				"session":   map[string]interface{}{"id": "s1"},
				"prompt":    map[string]interface{}{"text": text},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(logPath, append(data, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			previous := endpointTraceOpts
			t.Cleanup(func() { endpointTraceOpts = previous })
			normalized := strings.Join(strings.Fields(text), " ")
			var traceID string
			for _, level := range []string{"trace", "event"} {
				t.Run(level, func(t *testing.T) {
					endpointTraceOpts.resultLevel = level
					endpointOpts.jsonOutput = true
					output, err := captureStdout(t, func() error { return endpointTracesSearchCmd.RunE(endpointTracesSearchCmd, []string{"valuable"}) })
					if err != nil {
						t.Fatal(err)
					}
					var result dashboard.TraceSearchResultV1
					if err := json.Unmarshal([]byte(output), &result); err != nil {
						t.Fatal(err)
					}
					if level == "trace" {
						if len(result.Traces) != 1 || result.Traces[0].Title != text {
							t.Fatalf("raw trace title changed: %+v", result)
						}
						traceID = result.Traces[0].ID
					} else {
						if len(result.Events) != 1 || result.Events[0].Snippet != text || result.Events[0].Event.Content.Text != text {
							t.Fatalf("raw event content changed: %+v", result)
						}
					}
					endpointOpts.jsonOutput = false
					output, err = captureStdout(t, func() error { return endpointTracesSearchCmd.RunE(endpointTracesSearchCmd, []string{"valuable"}) })
					if err != nil {
						t.Fatal(err)
					}
					columns := 3
					if level == "event" {
						columns = 4
					}
					assertTraceMetadataRow(t, strings.TrimSuffix(output, "\n"), columns, normalized)
				})
			}
			t.Run("list parity", func(t *testing.T) {
				endpointOpts.jsonOutput = false
				output, err := captureStdout(t, func() error { return endpointTracesListCmd.RunE(endpointTracesListCmd, nil) })
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(output, normalized) || strings.Contains(output, text+"\n") && text != normalized {
					t.Fatalf("list normalization: %q", output)
				}
			})
			t.Run("show", func(t *testing.T) {
				endpointOpts.jsonOutput = true
				output, err := captureStdout(t, func() error { return endpointTracesShowCmd.RunE(endpointTracesShowCmd, []string{traceID}) })
				if err != nil {
					t.Fatal(err)
				}
				var result dashboard.TraceShowResultV1
				if err := json.Unmarshal([]byte(output), &result); err != nil {
					t.Fatal(err)
				}
				if result.Trace.Title != text || len(result.Events) != 1 || result.Events[0].Content.Text != text {
					t.Fatalf("raw show content changed: %+v", result)
				}
				endpointOpts.jsonOutput = false
				output, err = captureStdout(t, func() error { return endpointTracesShowCmd.RunE(endpointTracesShowCmd, []string{traceID}) })
				if err != nil {
					t.Fatal(err)
				}
				rows := strings.Split(output, "\n")
				assertTraceMetadataRow(t, rows[0], 2, normalized)
				assertTraceMetadataRow(t, rows[1], 3, strings.Join(strings.Fields(traceFirstNonEmpty(result.Events[0].Title, result.Events[0].Summary)), " "))
				if !strings.HasSuffix(output, indentTraceText(text)+"\n") {
					t.Fatalf("detail body changed: %q", output)
				}
			})
		})
	}
}

func assertTraceMetadataRow(t *testing.T, row string, columns int, text string) {
	t.Helper()
	fields := strings.Split(row, "\t")
	if len(fields) != columns || fields[len(fields)-1] != text || strings.ContainsAny(row, "\r\n") {
		t.Errorf("ambiguous metadata row: %q; want %d columns ending %q", row, columns, text)
	}
}

func TestTraceShowCommandMetadataKeepsIndentedCommand(t *testing.T) {
	logPath, _, _ := traceHistoryFixture(t, false, "")
	command := "printf 'valuable first line'\n\tprintf 'second line'"
	data, err := json.Marshal(map[string]interface{}{
		"timestamp": "2026-10-12T10:00:00Z",
		"event":     map[string]interface{}{"id": "e1", "action": "command.executed", "category": "tool"},
		"harness":   map[string]interface{}{"name": "cursor"},
		"session":   map[string]interface{}{"id": "s1"},
		"command":   map[string]interface{}{"command": command},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	previous := endpointTraceOpts
	t.Cleanup(func() { endpointTraceOpts = previous })
	endpointOpts.jsonOutput = true
	output, err := captureStdout(t, func() error { return endpointTracesListCmd.RunE(endpointTracesListCmd, nil) })
	if err != nil {
		t.Fatal(err)
	}
	var list dashboard.TraceListResultV1
	if err := json.Unmarshal([]byte(output), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Traces) != 1 {
		t.Fatalf("traces: %+v", list)
	}
	output, err = captureStdout(t, func() error { return endpointTracesShowCmd.RunE(endpointTracesShowCmd, []string{list.Traces[0].ID}) })
	if err != nil {
		t.Fatal(err)
	}
	var result dashboard.TraceShowResultV1
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || result.Events[0].Title != command || result.Events[0].Command == nil || result.Events[0].Command.Command != command {
		t.Fatalf("raw command changed: %+v", result)
	}
	endpointOpts.jsonOutput = false
	output, err = captureStdout(t, func() error { return endpointTracesShowCmd.RunE(endpointTracesShowCmd, []string{list.Traces[0].ID}) })
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(output, "\n")
	assertTraceMetadataRow(t, rows[1], 3, strings.Join(strings.Fields(command), " "))
	if !strings.HasSuffix(output, indentTraceText(command)+"\n") {
		t.Fatalf("raw command detail changed: %q", output)
	}
}
