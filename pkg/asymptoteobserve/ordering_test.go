package asymptoteobserve

import (
	"sync"
	"testing"
	"time"
)

// Fixed width is what lets a SIEM query, a `sort` over a JSONL tail, or the dashboard summary
// compare two timestamps as strings; time.RFC3339Nano would trim trailing zeros instead. The
// finding that motivated sub-second precision: 0 of 5,595 events in a real runtime log carried a
// sub-second timestamp, so 97% of them shared a timestamp with another event in the same session.
func TestFormatTimestamp(t *testing.T) {
	for name, tc := range map[string]struct {
		at   time.Time
		want string
	}{
		"sub-second":   {time.Date(2026, 8, 21, 9, 14, 3, 123456789, time.UTC), "2026-08-21T09:14:03.123456789Z"},
		"whole second": {time.Date(2026, 8, 21, 9, 14, 3, 0, time.UTC), "2026-08-21T09:14:03.000000000Z"},
		"half second":  {time.Date(2026, 8, 21, 9, 14, 3, 500000000, time.UTC), "2026-08-21T09:14:03.500000000Z"},
		"to UTC":       {time.Date(2026, 8, 21, 2, 14, 3, 0, time.FixedZone("UTC-7", -7*60*60)), "2026-08-21T09:14:03.000000000Z"},
	} {
		if got := FormatTimestamp(tc.at); got != tc.want {
			t.Errorf("%s: FormatTimestamp = %q, want %q", name, got, tc.want)
		}
	}
}

func TestParseTimestamp(t *testing.T) {
	at := time.Date(2026, 8, 21, 9, 14, 3, 123456789, time.UTC)
	if got, err := ParseTimestamp(FormatTimestamp(at)); err != nil || !got.Equal(at) {
		t.Fatalf("round trip = %s, %v; want %s", got, err, at)
	}
	// A log written before nanosecond stamping has to keep reading back: an installed agent
	// upgrades mid-log, and the active runtime.jsonl spans both.
	if got, err := ParseTimestamp("2026-08-21T09:14:03Z"); err != nil || !got.Equal(at.Truncate(time.Second)) {
		t.Fatalf("legacy = %s, %v", got, err)
	}
	for _, value := range []string{"", "not-a-time", "1755767643"} {
		if _, err := ParseTimestamp(value); err == nil {
			t.Fatalf("ParseTimestamp(%q) succeeded, want error", value)
		}
	}
}

func TestNewEventStampsSubSecondPrecision(t *testing.T) {
	event := NewEvent(NewEventOptions{Action: "session.started", Harness: HarnessInfo{Name: "claude_code"}})
	if len(event.Timestamp) != len(FormatTimestamp(time.Now())) {
		t.Fatalf("event timestamp = %q, want canonical fixed-width form", event.Timestamp)
	}
	if _, err := ParseTimestamp(event.Timestamp); err != nil {
		t.Fatalf("event timestamp %q does not parse: %v", event.Timestamp, err)
	}
}

func TestSequencerStartsAtOneAndAdvances(t *testing.T) {
	var s Sequencer
	// Zero is the schema's "unsequenced", so it must never be handed out.
	for want := uint64(1); want <= 3; want++ {
		if got := s.Next(); got != want {
			t.Fatalf("Next() = %d, want %d", got, want)
		}
	}
}

func TestSequencerIsConcurrencySafe(t *testing.T) {
	// The exporter's log, trace and metric consumers can run at the same time, and one
	// hook invocation emits several events; a duplicated number would silently
	// mis-order them.
	var (
		s    Sequencer
		wg   sync.WaitGroup
		mu   sync.Mutex
		seen = map[uint64]bool{}
	)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 32; j++ {
				n := s.Next()
				mu.Lock()
				if seen[n] {
					mu.Unlock()
					t.Errorf("sequence %d handed out twice", n)
					return
				}
				seen[n] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != 32*32 {
		t.Fatalf("handed out %d distinct sequences, want %d", len(seen), 32*32)
	}
}
