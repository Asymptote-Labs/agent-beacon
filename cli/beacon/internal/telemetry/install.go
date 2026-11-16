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
	st := loadState(r.home)
	if st.InstalledAt != "" {
		return
	}
	st.InstalledAt = now.UTC().Format(time.RFC3339)
	_ = saveState(r.home, st)
}
