package osuser

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// GroupIDs is the credential a process run as the account carries, so an answer it cannot read
// fully must fail rather than return fewer groups than it parsed.
func TestGroupIDs(t *testing.T) {
	prev := runID
	t.Cleanup(func() { runID = prev })
	for _, tc := range []struct {
		out    string
		err    error
		want   []uint32
		failed bool
	}{
		{out: "20 12 61 701\n", want: []uint32{20, 12, 61, 701}},
		{out: "1000\n", want: []uint32{1000}},
		{out: "4294967294\n", want: []uint32{4294967294}},
		{out: "4294967296\n", failed: true},
		{out: "-2\n", failed: true},
		{out: "", failed: true},
		{out: "20 staff\n", failed: true},
		{err: errors.New("no such user"), failed: true},
	} {
		runID = func(context.Context, int) (string, error) { return tc.out, tc.err }
		got, err := GroupIDs(501)
		if tc.failed {
			if err == nil {
				t.Errorf("GroupIDs with %q, %v = %v, want an error", tc.out, tc.err, got)
			}
			continue
		}
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("GroupIDs with %q = %v, %v; want %v", tc.out, got, err, tc.want)
		}
	}
}
