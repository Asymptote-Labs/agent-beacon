package handoff

import (
	"bufio"
	"errors"
	"io"
	"os"
	"strings"
)

// Bounds for reading the head of a transcript while listing. A listing opens every
// transcript, so it reads only enough to find where the session ran and what it was about.
const (
	headMaxLines    = 64
	headMaxBytes    = 4 << 20
	headMaxLineSize = 1 << 20
)

// scanHead passes the first lines of a JSONL transcript to consume until it returns true, within
// the listing bounds above. A listing opens every transcript, so it reads only its head.
func scanHead(path string, consume func(line []byte) (done bool)) {
	scanHeadLines(path, headMaxLineSize, consume)
}

// scanHeadLines is scanHead for a transcript whose lines can run longer than a listing's usual
// bound: a line up to maxLineSize is kept, and one past it is skipped. The byte budget grows with
// the line bound, so the first long line can still be read whole.
func scanHeadLines(path string, maxLineSize int, consume func(line []byte) (done bool)) {
	maxBytes := int64(headMaxBytes)
	if int64(maxLineSize) > maxBytes {
		maxBytes = int64(maxLineSize) + int64(headMaxBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	reader := bufio.NewReaderSize(io.LimitReader(f, maxBytes), 64<<10)
	for lines := 0; lines < headMaxLines; lines++ {
		line, err := readBoundedLine(reader, maxLineSize)
		if len(line) > 0 && consume(line) {
			return
		}
		if err != nil {
			return
		}
	}
}

// readBoundedLine returns the next line without its newline. A line longer than max is skipped
// whole and returned empty, so one huge tool result cannot stall a listing.
func readBoundedLine(r *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	oversized := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !oversized {
			if len(line)+len(chunk) > max {
				oversized = true
				line = nil
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if oversized {
			return nil, err
		}
		return []byte(strings.TrimRight(string(line), "\r\n")), err
	}
}
