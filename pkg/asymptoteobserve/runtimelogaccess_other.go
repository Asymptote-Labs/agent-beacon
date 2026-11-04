//go:build !unix && !windows

package asymptoteobserve

// readLogAccessMarker reports no marker here: the restricted mode needs the local ingest
// channel and the operating system's word on who sent each event, which Beacon offers on Unix
// and Windows only.
func readLogAccessMarker(string) string { return "" }
