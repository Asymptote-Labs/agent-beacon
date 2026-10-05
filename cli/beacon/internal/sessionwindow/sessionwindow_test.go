package sessionwindow

import (
	"reflect"
	"testing"
	"time"
)

type ref struct {
	id string
	ms int64
}

func ids(refs []ref) []string {
	out := []string{}
	for _, r := range refs {
		out = append(out, r.id)
	}
	return out
}

func TestRecentKeepsOrderWithoutAWindow(t *testing.T) {
	refs := []ref{{"a", 3}, {"b", 1}, {"c", 2}}
	got := Recent(refs, time.Time{}, func(r ref) int64 { return r.ms })
	if !reflect.DeepEqual(ids(got), []string{"a", "b", "c"}) {
		t.Fatalf("order changed without a window: %v", ids(got))
	}
}

func TestRecentDropsOldSessionsAndSortsNewestFirst(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	since := now.Add(-30 * 24 * time.Hour)
	refs := []ref{
		{"old", since.Add(-time.Hour).UnixMilli()},
		{"edge", since.UnixMilli()},
		{"unknown", 0},
		{"newest", now.UnixMilli()},
		{"middle", now.Add(-time.Hour).UnixMilli()},
	}
	got := Recent(refs, since, func(r ref) int64 { return r.ms })
	want := []string{"newest", "middle", "edge", "unknown"}
	if !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("Recent = %v, want %v", ids(got), want)
	}
}
