package dashboard

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func TestShowTraceAroundOriginalNumberWithTypeFilter(t *testing.T) {
	var lines [][]byte
	for n := 1; n <= 7; n++ {
		event := testSchemaEvent(fmt.Sprintf("2026-06-11T10:00:0%dZ", n), "cursor", "prompt.submitted", "prompt", "repo-a")
		event.Event.ID = fmt.Sprintf("e%d", n)
		event.Session = &schema.SessionInfo{ID: "around"}
		if n%2 == 0 {
			event.Event.Action = "command.executed"
			event.Event.Category = "command"
			event.Command = &schema.CommandInfo{Command: fmt.Sprintf("command %d", n)}
		} else {
			event.Prompt = &schema.PromptInfo{Text: fmt.Sprintf("prompt %d", n)}
		}
		lines = append(lines, marshalEvents(t, event)...)
	}
	path := newTestLog(t, lines)
	cases := []struct {
		name                 string
		query                TraceQuery
		numbers              []int
		total, offset, limit int
	}{
		{"defaults", TraceQuery{AroundEvent: 4, EventTypes: []string{"command"}}, []int{2, 4, 6}, 3, 1, 3},
		{"nonpositive context", TraceQuery{AroundEvent: 4, Before: -1, After: -2, EventTypes: []string{"command"}}, []int{2, 4, 6}, 3, 1, 3},
		{"anchor", TraceQuery{AroundEvent: 4, Before: 1, After: 1, EventTypes: []string{"command"}}, []int{2, 4, 6}, 3, 1, 3},
		{"filtered anchor", TraceQuery{AroundEvent: 3, Before: 1, After: 1, EventTypes: []string{"command"}}, []int{2, 4}, 3, 1, 2},
		{"first", TraceQuery{AroundEvent: 2, Before: 1, After: 1, EventTypes: []string{"command"}}, []int{2, 4}, 3, 1, 2},
		{"last", TraceQuery{AroundEvent: 6, Before: 1, After: 1, EventTypes: []string{"command"}}, []int{4, 6}, 3, 2, 2},
		{"before first", TraceQuery{AroundEvent: 1, Before: 1, After: 1, EventTypes: []string{"command"}}, []int{2}, 3, 1, 1},
		{"after last", TraceQuery{AroundEvent: 99, Before: 1, After: 1, EventTypes: []string{"command"}}, []int{6}, 3, 3, 1},
		{"empty filter", TraceQuery{AroundEvent: 4, Before: 1, After: 1, EventTypes: []string{"approval"}}, nil, 0, 1, 0},
		{"filtered pagination", TraceQuery{Offset: 2, Limit: 1, EventTypes: []string{"command"}}, []int{4}, 3, 2, 1},
		{"filtered past end", TraceQuery{Offset: 4, Limit: 1, EventTypes: []string{"command"}}, nil, 3, 4, 0},
		{"unfiltered", TraceQuery{AroundEvent: 4, Before: 1, After: 1}, []int{3, 4, 5}, 7, 3, 3},
		{"unfiltered boundary", TraceQuery{AroundEvent: 1, Before: 1, After: 1}, []int{1, 2, 3}, 7, 1, 3},
	}
	answers := map[string]string{}
	for _, mode := range []string{"jsonl", "history"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "history" {
				withHistory(t)
				optIn(t, path)
			} else {
				withoutHistory(t)
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					result, ok, err := ShowTrace(path, "session:cursor:around", tc.query)
					if err != nil || !ok {
						t.Fatalf("ShowTrace ok=%v err=%v", ok, err)
					}
					var numbers []int
					for _, event := range result.Events {
						numbers = append(numbers, event.Number)
					}
					if !reflect.DeepEqual(numbers, tc.numbers) {
						t.Errorf("numbers=%v want %v", numbers, tc.numbers)
					}
					if result.Range.TotalEvents != tc.total || result.Range.Offset != tc.offset || result.Range.Limit != tc.limit || result.Range.ReturnedEvents != len(tc.numbers) || result.Range.AroundEvent != tc.query.AroundEvent {
						t.Errorf("range=%+v want total=%d offset=%d limit=%d", result.Range, tc.total, tc.offset, tc.limit)
					}
					encoded := mustJSON(t, result)
					if mode == "jsonl" {
						answers[tc.name] = encoded
					} else if encoded != answers[tc.name] {
						t.Errorf("history/fallback JSON mismatch\n%s\n%s", answers[tc.name], encoded)
					}
				})
			}
		})
	}
}
