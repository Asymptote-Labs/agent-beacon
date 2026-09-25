package openclawsession

import "testing"

// Each record maps to one event today; the rule has to hold once a record maps to more (a prompt
// and its session.handoff link).
func TestPartialLastOrderNeverSkipsARecordsRemainingEvents(t *testing.T) {
	mapped := []MappedEvent{{SourceOrder: 1}, {SourceOrder: 2}, {SourceOrder: 2}, {SourceOrder: 3}, {SourceOrder: 3}}
	for _, tc := range []struct {
		failed, start, want int
	}{
		{failed: 0, start: 0, want: 0}, // nothing written
		{failed: 1, start: 0, want: 1}, // record 1 done, record 2 retried
		{failed: 2, start: 0, want: 1}, // half of record 2 written: retry record 2
		{failed: 3, start: 0, want: 2},
		{failed: 4, start: 0, want: 2}, // half of record 3 written: retry record 3
		{failed: 2, start: 5, want: 5}, // never moves the cursor backwards
	} {
		if got := partialLastOrder(tc.start, mapped, tc.failed); got != tc.want {
			t.Fatalf("failure at %d from %d: LastOrder = %d, want %d", tc.failed, tc.start, got, tc.want)
		}
	}
}
