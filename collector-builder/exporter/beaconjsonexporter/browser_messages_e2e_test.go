package beaconjsonexporter

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	observe "github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/plog"
)

// The extension's message-contract.test.ts compares these envelopes with the
// real parsers replaying the checked-in simple-turn SSE fixtures. This half
// checks that the producer contract survives OTLP, sanitization and JSONL.
func TestRecordedBrowserMessagesReachCanonicalChat(t *testing.T) {
	for _, site := range []struct{ name, prompt, answer string }{
		{"claude", "What is the capital of France?", "Paris."},
		{"chatgpt", "good evening", "Good evening! Hello to you as well. Hope your day is going well."},
	} {
		for _, legacy := range []bool{false, true} {
			name := site.name + "/semconv"
			if legacy {
				name = site.name + "/legacy-browser"
			}
			t.Run(name, func(t *testing.T) {
				data, err := os.ReadFile(filepath.Join("..", "..", "..", "browser-extension", "fixtures", site.name, "simple-turn.otlp.json"))
				if err != nil {
					t.Fatal(err)
				}
				logs, err := (&plog.JSONUnmarshaler{}).UnmarshalLogs(data)
				if err != nil {
					t.Fatal(err)
				}
				if legacy {
					// Recreate the exact role/text spelling emitted by older
					// browser versions, without altering any other attribute.
					records := logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
					for i := 0; i < records.Len(); i++ {
						attrs := records.At(i).Attributes()
						for _, key := range []string{"gen_ai.input.messages", "gen_ai.output.messages"} {
							value, ok := attrs.Get(key)
							if !ok {
								continue
							}
							var messages []struct {
								Role  string `json:"role"`
								Parts []struct {
									Content string `json:"content"`
								} `json:"parts"`
							}
							if err := json.Unmarshal([]byte(value.Str()), &messages); err != nil {
								t.Fatal(err)
							}
							var old []map[string]string
							for _, message := range messages {
								old = append(old, map[string]string{"role": message.Role, "text": message.Parts[0].Content})
							}
							encoded, err := json.Marshal(old)
							if err != nil {
								t.Fatal(err)
							}
							attrs.PutStr(key, string(encoded))
						}
					}
				}
				cfg := createDefaultConfig()
				cfg.Path = filepath.Join(t.TempDir(), "runtime.jsonl")
				exp, err := newExporter(cfg, exporter.Settings{})
				if err != nil {
					t.Fatal(err)
				}
				if err := exp.consumeLogs(context.Background(), logs); err != nil {
					t.Fatal(err)
				}
				file, err := os.Open(cfg.Path)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				var builder *observe.TraceBuilder
				var count int
				scanner := bufio.NewScanner(file)
				for scanner.Scan() {
					count++
					var event observe.Event
					if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
						t.Fatal(err)
					}
					if builder == nil {
						builder = observe.NewTraceBuilder(observe.TraceProjectionID(event), event)
					}
					projection := observe.TraceEventFromEvent(event, "", count)
					builder.Add(event, projection)
					if event.Event.ID == "" || projection.ID != event.Event.ID {
						t.Fatal("event identity missing or changed during projection")
					}
					if event.Event.Action == "agent.response.completed" {
						if got := observe.RetainedOutputText(event.GenAI, observe.GenAIPartTypeText); got != site.answer {
							t.Errorf("retained answer = %q, want %q", got, site.answer)
						}
						if projection.Content == nil || projection.Content.Text != site.answer {
							t.Errorf("projected answer = %+v, want %q", projection.Content, site.answer)
						}
					}
				}
				if err := scanner.Err(); err != nil {
					t.Fatal(err)
				}
				if count != 2 || builder == nil {
					t.Fatalf("wrote %d events, want prompt and answer", count)
				}
				var user, assistant []string
				for _, message := range observe.TraceMessages(builder.Bundle(observe.TraceProvenanceV1{})) {
					switch message.Role {
					case observe.TraceRoleUser:
						user = append(user, message.Text)
					case observe.TraceRoleAssistant:
						assistant = append(assistant, message.Text)
					}
				}
				if !reflect.DeepEqual(user, []string{site.prompt}) || !reflect.DeepEqual(assistant, []string{site.answer}) {
					t.Fatalf("canonical chat user=%q assistant=%q", user, assistant)
				}
			})
		}
	}
}
