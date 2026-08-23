package asymptoteobserve

import (
	"sync"
	"testing"
	"time"
)

func TestFormatTimestampKeepsSubSecondPrecision(t *testing.T) {
	// The finding that motivated this: 0 of 5,595 events in a real runtime log carried a
	// sub-second timestamp, so 97% of them shared a timestamp with another event in the
	// same session and nothing could order them.
	at := time.Date(2026, 8, 21, 9, 14, 3, 123456789, time.UTC)
	if got, want := FormatTimestamp(at), "2026-08-21T09:14:03.123456789Z"; got != want {
		t.Fatalf("FormatTimestamp = %q, want %q", got, want)
	}
}

func TestFormatTimestampIsFixedWidth(t *testing.T) {
	// time.RFC3339Nano would render these at three different widths, because it trims
	// trailing zeros from the fractional second.
	cases := []time.Time{
		time.Date(2026, 8, 21, 9, 14, 3, 0, time.UTC),
		time.Date(2026, 8, 21, 9, 14, 3, 500000000, time.UTC),
		time.Date(2026, 8, 21, 9, 14, 3, 1, time.UTC),
	}
	want := len(FormatTimestamp(cases[0]))
	for _, at := range cases {
		if got := FormatTimestamp(at); len(got) != want {
			t.Fatalf("FormatTimestamp(%s) = %q (%d chars), want %d chars", at, got, len(got), want)
		}
	}
	if got, want := FormatTimestamp(cases[0]), "2026-08-21T09:14:03.000000000Z"; got != want {
		t.Fatalf("whole second = %q, want %q (zeros retained)", got, want)
	}
}

func TestFormatTimestampSortsByteWiseInChronologicalOrder(t *testing.T) {
	// Fixed width is what lets a SIEM query, a `sort` over a JSONL tail, or the
	// dashboard summary compare two timestamps as strings and get the right answer.
	base := time.Date(2026, 8, 21, 9, 14, 3, 0, time.UTC)
	prev := ""
	for _, offset := range []time.Duration{
		0, time.Nanosecond, time.Microsecond, 500 * time.Millisecond,
		time.Second, 61 * time.Second,
	} {
		got := FormatTimestamp(base.Add(offset))
		if prev != "" && !(prev < got) {
			t.Fatalf("string order disagrees with time order: %q not before %q", prev, got)
		}
		prev = got
	}
}

func TestFormatTimestampNormalizesToUTC(t *testing.T) {
	zone := time.FixedZone("UTC-7", -7*60*60)
	at := time.Date(2026, 8, 21, 2, 14, 3, 0, zone)
	if got, want := FormatTimestamp(at), "2026-08-21T09:14:03.000000000Z"; got != want {
		t.Fatalf("FormatTimestamp = %q, want %q", got, want)
	}
}

func TestParseTimestampRoundTrips(t *testing.T) {
	at := time.Date(2026, 8, 21, 9, 14, 3, 123456789, time.UTC)
	got, err := ParseTimestamp(FormatTimestamp(at))
	if err != nil {
		t.Fatalf("ParseTimestamp returned error: %v", err)
	}
	if !got.Equal(at) {
		t.Fatalf("round trip = %s, want %s", got, at)
	}
}

func TestParseTimestampReadsLegacySecondResolution(t *testing.T) {
	// A log written before nanosecond stamping has to keep reading back: an installed
	// agent upgrades mid-log, and the active runtime.jsonl spans both.
	got, err := ParseTimestamp("2026-08-21T09:14:03Z")
	if err != nil {
		t.Fatalf("ParseTimestamp returned error: %v", err)
	}
	if want := time.Date(2026, 8, 21, 9, 14, 3, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("parsed = %s, want %s", got, want)
	}
}

func TestParseTimestampRejectsNonTimestamps(t *testing.T) {
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
