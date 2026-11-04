package localingest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// DefaultClientTimeout bounds one batch exchange. Hooks run inline with the agent, so a collector
// that does not answer promptly costs the agent this long once, after which the events are spooled.
// It is longer than the server's own budget for a connection (1.5s plus time to reply), so a
// busy server reports how far it got rather than leaving the client to resend lines it wrote.
const DefaultClientTimeout = 3 * time.Second

// Client sends batches to a collector's ingest socket, or on Windows its named pipe.
type Client struct {
	// SocketPath is the socket or pipe to send to. Empty means DefaultSocketPath.
	SocketPath string
	// Timeout bounds each connection, from dial to reply. Zero means DefaultClientTimeout.
	Timeout time.Duration
	// TrustedOwner is the uid the socket and its directory must belong to on Unix. Zero, the
	// default, is root; tests set their own uid.
	TrustedOwner int
	// TrustedSID is, on Windows, one more account the pipe may belong to besides SYSTEM and
	// Administrators, the only creators trusted otherwise. Empty in production; tests that serve a
	// pipe as their own account set their SID.
	TrustedSID string
	// LogPath is the runtime log the lines are meant for, sent in the header. The collector refuses
	// them, with ErrLogMismatch, if it writes another log.
	LogPath string
}

func (c Client) path() string {
	if c.SocketPath == "" {
		return DefaultSocketPath
	}
	return c.SocketPath
}

// Probe asks the collector to answer an empty batch for LogPath. It is what tells a live
// collector from a socket a killed one left behind, which looking at the socket file cannot.
// ErrNoSocket, ErrUntrustedSocket and ErrLogMismatch keep their meaning.
func (c Client) Probe() error {
	_, err := c.sendBatch(nil)
	return err
}

// Send delivers lines in batches of at most MaxLinesPerConn and returns how far it got: lines
// [0, Processed) were each written or rejected for good, and the caller keeps the rest. It stops at
// the first batch that does not finish, and returns the reason.
//
// Each line must be one complete event with no newline inside it; a trailing newline is dropped.
// ErrNoSocket means the endpoint has no ingest socket, so nothing was sent.
func (c Client) Send(lines [][]byte) (Response, error) {
	cleaned := make([][]byte, len(lines))
	for i, line := range lines {
		line = bytes.TrimSuffix(line, []byte{'\n'})
		if bytes.IndexByte(line, '\n') >= 0 {
			return Response{}, fmt.Errorf("line %d contains a newline", i)
		}
		cleaned[i] = line
	}
	var total Response
	for start := 0; start < len(cleaned); start += MaxLinesPerConn {
		end := start + MaxLinesPerConn
		if end > len(cleaned) {
			end = len(cleaned)
		}
		resp, err := c.sendBatch(cleaned[start:end])
		total.Accepted += resp.Accepted
		total.Rejected += resp.Rejected
		total.Processed += resp.Processed
		if err != nil {
			total.Error = resp.Error
			return total, err
		}
	}
	return total, nil
}

// halfCloser is a connection whose write side can be closed on its own: a Unix socket, or a
// message-mode named pipe, which signals it with a zero-byte message.
type halfCloser interface{ CloseWrite() error }

func (c Client) sendBatch(lines [][]byte) (Response, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultClientTimeout
	}
	// connect checks that the socket or pipe is the collector's before anything is sent to it, and
	// returns ErrNoSocket or ErrUntrustedSocket when it is missing or someone else's.
	conn, err := c.connect(timeout)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	header, err := json.Marshal(Header{Log: c.LogPath})
	if err != nil {
		return Response{}, err
	}
	w := bufio.NewWriter(conn)
	_, writeErr := w.WriteString(Preamble + "\n" + string(header) + "\n")
	for _, line := range lines {
		if writeErr != nil {
			break
		}
		if _, writeErr = w.Write(line); writeErr == nil {
			writeErr = w.WriteByte('\n')
		}
	}
	if writeErr == nil {
		writeErr = w.Flush()
	}
	if writeErr == nil {
		if hc, ok := conn.(halfCloser); ok {
			writeErr = hc.CloseWrite()
		}
	}

	// Read the reply even after a failed write: a server that stopped reading early (a full batch,
	// a refused preamble) still says how far it got before closing.
	reply, readErr := bufio.NewReader(io.LimitReader(conn, maxResponseBytes)).ReadBytes('\n')
	if readErr != nil && len(reply) == 0 {
		if writeErr != nil {
			return Response{}, fmt.Errorf("send to %s: %w", c.path(), writeErr)
		}
		return Response{}, fmt.Errorf("read reply from %s: %w", c.path(), readErr)
	}
	var resp Response
	if err := json.Unmarshal(bytes.TrimSpace(reply), &resp); err != nil {
		return Response{}, fmt.Errorf("malformed reply from %s: %w", c.path(), err)
	}
	if resp.Processed < 0 || resp.Processed > len(lines) || resp.Accepted < 0 || resp.Rejected < 0 ||
		resp.Accepted+resp.Rejected != resp.Processed {
		return Response{}, fmt.Errorf("inconsistent reply from %s: %+v for %d lines", c.path(), resp, len(lines))
	}
	if resp.Error == errLogMismatch {
		return resp, fmt.Errorf("%w: sent for %s", ErrLogMismatch, c.LogPath)
	}
	if resp.Error != "" {
		return resp, fmt.Errorf("collector stopped after %d of %d events: %s", resp.Processed, len(lines), resp.Error)
	}
	if resp.Processed != len(lines) {
		if writeErr != nil {
			return resp, fmt.Errorf("send to %s: %w", c.path(), writeErr)
		}
		return resp, fmt.Errorf("collector handled %d of %d events", resp.Processed, len(lines))
	}
	return resp, nil
}

// ErrUntrustedSocket is returned when something exists at the socket path but could have been put
// there by someone other than root, or on Windows a pipe of that name was created by someone other
// than SYSTEM or Administrators. A writer that sees it does not send there, because a socket or
// pipe a user planted would receive every other user's events.
var ErrUntrustedSocket = errors.New("untrusted ingest socket")
