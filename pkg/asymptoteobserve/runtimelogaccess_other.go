//go:build !unix

package asymptoteobserve

// readLogAccessMarker reports no marker off Unix: the restricted mode needs the local ingest
// socket and kernel peer credentials, which Beacon does not offer on Windows, so the system log
// there keeps the shared access its ACL grants.
func readLogAccessMarker(string) string { return "" }
