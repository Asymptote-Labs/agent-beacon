package logging

import (
	"io"
	"os"
)

// ReadSessionLogs reads log data from a session log file starting at byteOffset.
// Returns the raw bytes (JSONL) and the new byte offset.
// If the file doesn't exist or is empty from offset, returns nil with no error.
func ReadSessionLogs(logFile string, byteOffset int64) ([]byte, int64, error) {
	f, err := os.Open(logFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, byteOffset, nil
		}
		return nil, byteOffset, err
	}
	defer f.Close()

	// Check file size
	info, err := f.Stat()
	if err != nil {
		return nil, byteOffset, err
	}

	fileSize := info.Size()
	if fileSize <= byteOffset {
		// File was truncated or nothing new — reset to current size
		return nil, fileSize, nil
	}

	// Seek to offset and read to EOF
	if _, err := f.Seek(byteOffset, io.SeekStart); err != nil {
		return nil, byteOffset, err
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, byteOffset, err
	}

	if len(data) == 0 {
		return nil, byteOffset, nil
	}

	return data, byteOffset + int64(len(data)), nil
}
