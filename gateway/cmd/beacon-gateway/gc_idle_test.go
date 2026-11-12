package main

import (
	"slices"
	"testing"
)

// Memory is released once per idle spell: at the first check after a whole interval with no
// collection, never while collections keep coming, and not again until the gateway has been busy;
// the collection the release itself runs is not being busy.
func TestIdleRelease(t *testing.T) {
	// Collections that ran in each interval before a check.
	activity := []int{0, 3, 5, 0, 0, 0, 1, 0, 0, 2, 0}
	want := []int{3, 7, 10} // the checks that release
	r := idleReleaser{released: true}
	var gcs uint64
	var got []int
	for i, n := range activity {
		gcs += uint64(n)
		if r.idle(gcs) {
			got = append(got, i)
			gcs++ // the release's own collection
			r.didRelease(gcs)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("released at checks %v, want %v", got, want)
	}
}
