package localingest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// DefaultClientTimeout bounds one batch exchange. Hooks run inline with the agent, so a collector
// that does not answer promptly costs the agent this long once, after which the events are spooled.
// It is longer than the server's own budget for a connection (1.5s plus time to reply), so a
// busy server reports how far it got rather than leaving the client to resend lines it wrote.
const DefaultClientTimeout = 3 * time.Second

// Client sends batches to a collector's ingest socket.
type Client struct {
	// SocketPath is the socket to send to. Empty means DefaultSocketPath.
	SocketPath string
	// Timeout bounds each connection, from dial to reply. Zero means DefaultClientTimeout.
	Timeout time.Duration
	// TrustedOwner is the uid the socket and its directory must belong to. Zero, the default, is
	// root; tests set their own uid.
	TrustedOwner int
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

// Available reports whether a trusted ingest socket exists, without connecting to it. It is how a
// writer decides whether the endpoint is in the restricted mode at all; ErrNoSocket means it is
// not.
func (c Client) Available() error {
	_, err := checkSocket(c.path(), c.TrustedOwner)
	return err
}

// Probe asks the collector to answer an empty batch for LogPath. It is what tells a live
// collector from a socket a killed one left behind, which Available cannot: that only looks at the
// file. ErrNoSocket and ErrLogMismatch keep their meaning.
func (c Client) Probe() error {
	socket, err := checkSocket(c.path(), c.TrustedOwner)
	if err != nil {
		return err
	}
	_, err = c.sendBatch(socket, nil)
	return err
}

// Send delivers lines in batches of at most MaxLinesPerConn and returns how far it got: lines
// [0, Processed) were each written or rejected for good, and the caller keeps the rest. It stops at
// the first batch that does not finish, and returns the reason.
//
// Each line must be one complete event with no newline inside it; a trailing newline is dropped.
// ErrNoSocket means the endpoint has no ingest socket, so nothing was sent. ErrOutcomeUnknown means
// the batch that failed got no usable reply, so the lines from Processed on may or may not have
// been written.
func (c Client) Send(lines [][]byte) (Response, error) {
	cleaned := make([][]byte, len(lines))
	for i, line := range lines {
		line = bytes.TrimSuffix(line, []byte{'\n'})
		if bytes.IndexByte(line, '\n') >= 0 {
			return Response{}, fmt.Errorf("line %d contains a newline", i)
		}
		cleaned[i] = line
	}
	socket, err := checkSocket(c.path(), c.TrustedOwner)
	if err != nil {
		return Response{}, err
	}
	var total Response
	for start := 0; start < len(cleaned); start += MaxLinesPerConn {
		end := start + MaxLinesPerConn
		if end > len(cleaned) {
			end = len(cleaned)
		}
		resp, err := c.sendBatch(socket, cleaned[start:end])
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

func (c Client) sendBatch(socket string, lines [][]byte) (Response, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultClientTimeout
	}
	conn, err := net.DialTimeout("unix", socket, timeout)
	if err != nil {
		return Response{}, fmt.Errorf("connect to %s: %w", c.path(), err)
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
		if uc, ok := conn.(*net.UnixConn); ok {
			writeErr = uc.CloseWrite()
		}
	}

	// Read the reply even after a failed write: a server that stopped reading early (a full batch,
	// a refused preamble) still says how far it got before closing.
	reply, readErr := bufio.NewReader(io.LimitReader(conn, maxResponseBytes)).ReadBytes('\n')
	// Past this point the connection was made and lines may have reached the server, so without a
	// reply it can be said how far it got, nothing is known about them.
	unknown := func(err error) error {
		if len(lines) == 0 {
			return err
		}
		return fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
	}
	if readErr != nil && len(reply) == 0 {
		if writeErr != nil {
			return Response{}, unknown(fmt.Errorf("send to %s: %w", c.path(), writeErr))
		}
		return Response{}, unknown(fmt.Errorf("read reply from %s: %w", c.path(), readErr))
	}
	var resp Response
	if err := json.Unmarshal(bytes.TrimSpace(reply), &resp); err != nil {
		return Response{}, unknown(fmt.Errorf("malformed reply from %s: %w", c.path(), err))
	}
	if resp.Processed < 0 || resp.Processed > len(lines) || resp.Accepted < 0 || resp.Rejected < 0 ||
		resp.Accepted+resp.Rejected != resp.Processed {
		return Response{}, unknown(fmt.Errorf("inconsistent reply from %s: %+v for %d lines", c.path(), resp, len(lines)))
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
// there by someone other than root. A writer that sees it does not send there, because a socket a
// user planted would receive every other user's events.
var ErrUntrustedSocket = errors.New("untrusted ingest socket")
