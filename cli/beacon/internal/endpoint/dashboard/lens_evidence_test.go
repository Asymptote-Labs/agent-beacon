package dashboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func lensEvidenceRules(t *testing.T, correlation bool) string {
	t.Helper()
	match := `match: e.event.action == "command.executed"`
	if correlation {
		match = `correlation:
  scope: session
  window: 120s
  steps:
    - id: read
      match: e.event.action == "file.read"
    - id: command
      match: e.event.action == "command.executed"`
	}
	rule := `id: lens-evidence
version: 1
title: Lens evidence
severity: high
status: stable
posture: detect
` + match + `
emit:
  reason: "Evidence fixture"
tests:
  - name: command
    verdict: match
    events:
      - event: {action: file.read}
        session: {id: legacy}
      - event: {action: command.executed}
        session: {id: legacy}
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "evidence.rule.yaml"), []byte(rule), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func lensEvidenceBuilders() map[string]func(string, LensDataOptions) (LensDataV1, bool, error) {
	return map[string]func(string, LensDataOptions) (LensDataV1, bool, error){
		"trace": func(path string, opts LensDataOptions) (LensDataV1, bool, error) {
			return BuildLensData(path, "session:claude_code:legacy", opts)
		},
		"session": func(path string, opts LensDataOptions) (LensDataV1, bool, error) {
			return BuildSessionLensData(path, "legacy", opts)
		},
	}
}

func TestLensEvidencePreservesRecordIdentity(t *testing.T) {
	for name, build := range lensEvidenceBuilders() {
		for _, history := range []bool{false, true} {
			suffix := "/jsonl"
			if history {
				suffix = "/history"
			}
			t.Run(name+suffix, func(t *testing.T) {
				testenv.SetHome(t, t.TempDir())
				withoutHistory(t)
				event := testSchemaEvent("2026-06-11T10:00:00Z", "claude_code", "command.executed", "command", "repo-a")
				event.Session = &schema.SessionInfo{ID: "legacy"}
				event.Command = &schema.CommandInfo{Command: "fixture"}
				event.GenAI = &schema.GenAIInfo{Usage: &schema.GenAIUsageInfo{InputTokens: int64Ptr(100), OutputTokens: int64Ptr(10)}}
				explicit := event
				explicit.Event.ID = " explicit-id "
				other := event
				other.Session = &schema.SessionInfo{ID: "legacy-other"}
				lines := marshalEvents(t, event, event, explicit, other)
				path := newTestLog(t, append([][]byte{nil, []byte("malformed")}, lines...))
				archive := path + ".3" // A sparse rotation must keep its actual archive number.
				writeTestLog(t, archive, nil, []byte("malformed"), lines[0])
				original := map[string][]byte{}
				for _, file := range []string{path, archive} {
					var err error
					if original[file], err = os.ReadFile(file); err != nil {
						t.Fatal(err)
					}
				}
				if history {
					withHistory(t)
					optIn(t, path)
				}

				data, ok, err := build(path, LensDataOptions{UserMode: true, RulesDir: lensEvidenceRules(t, false)})
				if err != nil || !ok {
					t.Fatalf("build = ok %v, err %v", ok, err)
				}
				want := []string{"archive-3-line-3", "line-3", "line-4", " explicit-id "}
				var projected, evidence []string
				for _, event := range data.Trace.Events {
					projected = append(projected, event.ID)
				}
				if data.Findings == nil {
					t.Fatal("findings were not scanned")
				}
				for _, finding := range data.Findings.Items {
					evidence = append(evidence, finding.EventIDs...)
				}
				if !reflect.DeepEqual(projected, want) {
					t.Fatalf("projected IDs = %q, want %q", projected, want)
				}
				if len(data.Findings.Items) != len(want) || !reflect.DeepEqual(evidence, want) {
					t.Fatalf("%d findings have evidence IDs %q, want %q", len(data.Findings.Items), evidence, want)
				}
				if data.Truncated {
					t.Fatal("small fixture was truncated")
				}
				wantUsage := lensTokenUsage([]schema.Event{event, event, event, explicit})
				if !reflect.DeepEqual(data.TokenUsage, wantUsage) {
					t.Fatalf("token usage = %#v, want unchanged raw-event usage %#v", data.TokenUsage, wantUsage)
				}
				for file, before := range original {
					after, err := os.ReadFile(file)
					if err != nil || !reflect.DeepEqual(after, before) {
						t.Fatalf("source log %s changed: err %v", file, err)
					}
				}
			})
		}
	}
}

func TestLensEvidenceFollowsChronologicalCorrelation(t *testing.T) {
	for name, build := range lensEvidenceBuilders() {
		t.Run(name, func(t *testing.T) {
			testenv.SetHome(t, t.TempDir())
			withoutHistory(t)
			read := testSchemaEvent("2026-06-11T10:00:00Z", "claude_code", "file.read", "file", "repo-a")
			read.Session = &schema.SessionInfo{ID: "legacy"}
			command := testSchemaEvent("2026-06-11T10:00:30Z", "claude_code", "command.executed", "command", "repo-a")
			command.Session = read.Session
			// The later event arrived first; correlation must sort events without losing IDs.
			path := newTestLog(t, marshalEvents(t, command, read))
			data, ok, err := build(path, LensDataOptions{UserMode: true, RulesDir: lensEvidenceRules(t, true)})
			if err != nil || !ok {
				t.Fatalf("build = ok %v, err %v", ok, err)
			}
			if data.Findings == nil || len(data.Findings.Items) != 1 {
				t.Fatalf("findings = %#v, want one ordered correlation", data.Findings)
			}
			if got := data.Findings.Items[0].EventIDs; !reflect.DeepEqual(got, []string{"line-2", "line-1"}) {
				t.Fatalf("correlation evidence = %v, want [line-2 line-1]", got)
			}
		})
	}
}

func TestLensFindingsDoesNotMutateRawInput(t *testing.T) {
	event := testSchemaEvent("2026-06-11T10:00:00Z", "claude_code", "command.executed", "command", "repo-a")
	raw := []EventRecord{{ID: "line-1", Event: event}}
	before, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lensFindings(raw, LensDataOptions{RulesDir: lensEvidenceRules(t, false)}); err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("scan mutated its caller's raw events")
	}
}

func TestLensEvidenceRetainsReferencesWhenBundleIsTruncated(t *testing.T) {
	event := testSchemaEvent("2026-06-11T10:00:00Z", "claude_code", "command.executed", "command", "repo-a")
	show := TraceShowResultV1{
		Events: []TraceEventV1{{ID: "line-1", Summary: strings.Repeat("x", lensDataEventBudget)}},
		Range:  TraceRangeV1{TotalEvents: 1, ReturnedEvents: 1, Offset: 1, Limit: 1},
	}
	data := lensDataFromShow(show, []EventRecord{{ID: "line-1", Event: event}}, LensDataOptions{RulesDir: lensEvidenceRules(t, false)})
	if !data.Truncated || len(data.Trace.Events) != 0 || data.Trace.Range.TotalEvents != 1 || data.Trace.Range.ReturnedEvents != 0 {
		t.Fatalf("truncation = %v, events = %d, range = %#v", data.Truncated, len(data.Trace.Events), data.Trace.Range)
	}
	if data.Findings == nil || len(data.Findings.Items) != 1 || !reflect.DeepEqual(data.Findings.Items[0].EventIDs, []string{"line-1"}) {
		t.Fatalf("findings = %#v, want evidence for the event omitted by truncation", data.Findings)
	}
}
