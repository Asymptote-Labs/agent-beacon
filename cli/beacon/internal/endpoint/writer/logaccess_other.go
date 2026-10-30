//go:build !unix

package writer

import "errors"

// RestrictSystemLog is unavailable here: the restricted mode needs the local ingest socket and
// kernel peer credentials, which Beacon does not offer on Windows.
func RestrictSystemLog(string, int) error {
	return errors.New("the restricted log access mode is not available on this platform")
}

// ShareSystemLog is EnsureSystemLogWritable here, where the log is always shared.
func ShareSystemLog(path string) error { return EnsureSystemLogWritable(path) }
