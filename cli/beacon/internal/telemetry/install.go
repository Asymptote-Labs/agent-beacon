package telemetry

import "time"

// InstalledBefore reports whether an install has already succeeded for this home, as recorded by
// RecordInstalled. It is false when telemetry is off, since nothing is sent then.
func InstalledBefore() bool {
	r := active()
	if r == nil {
		return false
	}
	return loadState(r.home).InstalledAt != ""
}

// RecordInstalled remembers that an install succeeded for this home, keeping the first time.
func RecordInstalled(now time.Time) {
	r := active()
	if r == nil || !r.persist {
		return
	}
	updateState(r.home, func(st *state) bool {
		if st.InstalledAt != "" {
			return false
		}
		st.InstalledAt = now.UTC().Format(time.RFC3339)
		return true
	})
}
