package collector

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"strings"
)

const (
	// startLogReadLimit bounds how much of the collector's log is read back after a failed start.
	// The line that explains an exit on start is at the end, and a launchd log under /tmp is
	// appended to across every restart, so there is no reason to read the whole file.
	startLogReadLimit = 64 << 10
	// startLogLineLimit keeps one pathological line from swamping the install error it is quoted in.
	startLogLineLimit = 400
)

// LogSize reports the current size of the collector log at path, or zero when there is none.
//
// Taken before the collector is started so LogErrorSince reads only what this start wrote: the
// launchd and supervised logs are appended to across runs, and quoting a line left by some earlier
// failure would misdirect the person reading the install error.
func LogSize(path string) int64 {
	if path == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// LogErrorSince returns the line that best explains a collector that did not become ready: the
// last line written after offset that reads as an error, or failing that the last non-empty line.
// It returns "" when nothing was written.
//
// The collector writes its fatal reason to stderr and exits -- "failed to bind to address
// 127.0.0.1:13133 ... address already in use" was the #447 case -- and an install that reports only
// "ports are not listening" hides that reason in a file the user has no cause to open.
func LogErrorSince(path string, offset int64) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	size := info.Size()
	if offset < 0 || offset > size {
		// Smaller than when the start began: rotated or truncated, so all of it is new.
		offset = 0
	}
	if size-offset > startLogReadLimit {
		offset = size - startLogReadLimit
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(f, startLogReadLimit))
	if err != nil {
		return ""
	}
	var lastLine, lastError string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 4096), startLogReadLimit)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		lastLine = line
		if looksLikeError(line) {
			lastError = line
		}
	}
	line := lastError
	if line == "" {
		line = lastLine
	}
	return truncateLogLine(line)
}

func looksLikeError(line string) bool {
	lower := strings.ToLower(line)
	for _, marker := range []string{"error", "failed", "fatal", "panic"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func truncateLogLine(line string) string {
	runes := []rune(line)
	if len(runes) <= startLogLineLimit {
		return line
	}
	return string(runes[:startLogLineLimit]) + "..."
}
