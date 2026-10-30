//go:build !unix && !windows

package writer

import "errors"

// RestrictSystemLog is unavailable here: the restricted mode needs the local ingest channel and
// the operating system's word on who sent each event, which Beacon has on Unix and Windows only.
func RestrictSystemLog(string, int) error {
	return errors.New("the restricted log access mode is not available on this platform")
}

// ShareSystemLog is EnsureSystemLogWritable here, where the log is always shared.
func ShareSystemLog(path string) error { return EnsureSystemLogWritable(path) }
