// Package sessionwindow selects the sessions a bounded backfill sweep reads.
//
// Every session-store collector lists its sessions in its own order and reads all of them. A
// backfill that runs on its own, at install or connect, should read only recent history and should
// spend whatever budget it has on the newest sessions first, so the collectors share this one rule
// rather than each growing its own.
package sessionwindow

import (
	"sort"
	"time"
)

// Recent returns the refs modified at or after since, newest first. A ref whose modification time
// is unknown (zero or negative) is kept, after every dated one, because the store gave no reason to
// skip it. A zero since returns refs unchanged, in their original order: that is every ordinary
// sync.
//
// Refs left out are not consumed: their collector cursors are never touched, so a later
// `beacon endpoint <runtime> sync` still reads them.
func Recent[T any](refs []T, since time.Time, modifiedMS func(T) int64) []T {
	if since.IsZero() {
		return refs
	}
	cutoff := since.UnixMilli()
	out := make([]T, 0, len(refs))
	for _, ref := range refs {
		if ms := modifiedMS(ref); ms <= 0 || ms >= cutoff {
			out = append(out, ref)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		mi, mj := modifiedMS(out[i]), modifiedMS(out[j])
		if (mi <= 0) != (mj <= 0) {
			return mj <= 0
		}
		return mi > mj
	})
	return out
}
