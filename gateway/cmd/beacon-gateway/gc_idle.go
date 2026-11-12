package main

import (
	"runtime"
	rtdebug "runtime/debug"
	"runtime/metrics"
	"time"
)

// idleRelease is how long the gateway goes without a collection before it returns the memory its
// last busy spell grew to. The collector runs only as the heap grows, and the runtime gives free
// memory back to the system slowly, so after a burst an idle sidecar kept its peak footprint: 20
// seconds after load, RSS was still 95% of it.
const idleRelease = 30 * time.Second

// startIdleRelease returns memory to the system once the gateway has gone idleRelease without a
// collection, and again after each busy spell.
func startIdleRelease() {
	sample := []metrics.Sample{{Name: "/gc/cycles/total:gc-cycles"}}
	cycles := func() uint64 {
		metrics.Read(sample)
		return sample[0].Value.Uint64()
	}
	go func() {
		r := idleReleaser{last: cycles(), released: true} // nothing to give back before a busy spell
		for range time.Tick(idleRelease) {
			if r.idle(cycles()) {
				// A sync.Pool keeps what was put back since the last collection through the
				// next one, and the calls that ended the busy spell put their body buffers back
				// after it: one collection first, so FreeOSMemory's own frees them.
				runtime.GC()
				rtdebug.FreeOSMemory()
				r.didRelease(cycles())
			}
		}
	}()
}

// idleReleaser decides when to release memory: at the first check after a whole interval with no
// collection, and not again until a collection other than its own has run. A collection means the
// gateway is allocating; while it is, memory released would only be taken back.
type idleReleaser struct {
	last     uint64 // collections counted at the last check
	released bool   // released since the last collection
}

// idle reports, given the collections counted now, whether to release memory.
func (r *idleReleaser) idle(cycles uint64) bool {
	if cycles != r.last {
		r.last, r.released = cycles, false
		return false
	}
	return !r.released
}

// didRelease records a release and the collections counted after it, its own included.
func (r *idleReleaser) didRelease(cycles uint64) {
	r.last, r.released = cycles, true
}
