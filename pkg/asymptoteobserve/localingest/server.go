package localingest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync"
	"time"
)

const (
	defaultMaxConns = 64
	// defaultMaxConnsPerUID keeps one account from holding every connection slot, which would
	// leave every other account's hooks spooling. It leaves room for an agent running many tool
	// calls at once; a hook refused here spools its event rather than losing it.
	defaultMaxConnsPerUID = 16
	// defaultConnTimeout is the server's whole budget for one connection: reading, writing the log
	// and replying. It is shorter than DefaultClientTimeout, so a busy server stops and says how
	// far it got before the client gives up -- a client that gave up resends the whole batch, and
	// the lines the server did write would then be written twice.
	defaultConnTimeout = 1500 * time.Millisecond
	// replyGrace is the time past the budget the server allows itself to drain what the client is
	// still sending and write the reply.
	replyGrace = 500 * time.Millisecond
	// defaultRateBytesPerSecond and defaultRateBurstBytes bound how many bytes one account can push
	// into the log. Bytes, not events: the log rotates by size, so a flood of maximum-size events
	// counted as events would rotate everyone else's history out in seconds. An honest hook writes
	// a few kilobytes per tool call; the burst covers a full spool drain.
	defaultRateBytesPerSecond = 256 * 1024
	defaultRateBurstBytes     = 16 * 1024 * 1024
)

// Server accepts batches on a listener and hands each verified, stamped line to Append.
type Server struct {
	// Append writes one line, already stamped by StampLine, to the runtime log. An error wrapping
	// ErrRejected drops that line for good; any other error stops the batch, and the client keeps
	// the rest to send again.
	Append func(line []byte) error
	// LookupName resolves a peer's uid to its account name for the stamped user. Optional.
	LookupName func(uid uint32) string
	// LogPath is the runtime log Append writes. A batch whose header names another log is refused
	// without being read into the log. Empty accepts any header; only tests leave it empty.
	LogPath string
	// Logf reports refused connections and rejected lines. Optional.
	Logf func(format string, args ...interface{})

	// MaxConns bounds concurrent connections; further ones are closed at once, and their clients
	// spool. Zero means 64.
	MaxConns int
	// MaxConnsPerUID bounds one account's concurrent connections. Zero means 16.
	MaxConnsPerUID int
	// ConnTimeout bounds the work on one connection. Zero means 1.5s.
	ConnTimeout time.Duration
	// RateBytesPerSecond and RateBurstBytes bound the bytes each account can write. Zero means
	// the defaults; a negative rate turns the bound off.
	RateBytesPerSecond float64
	RateBurstBytes     float64

	// now is the clock the rate limiter reads; tests replace it.
	now func() time.Time

	mu        sync.Mutex
	listeners map[net.Listener]struct{}
	conns     map[net.Conn]struct{}
	slots     chan struct{}
	closed    bool
	wg        sync.WaitGroup
	buckets   map[uint32]*bucket
	perUID    map[uint32]int
	logStates map[uint32]*logState
}

// ErrServerClosed is returned by Serve after Close.
var ErrServerClosed = errors.New("localingest: server closed")

// Serve accepts connections on l until Close is called or l fails. It refuses to run where the
// kernel cannot say who a peer is, because then nothing it wrote could be attributed.
func (s *Server) Serve(l net.Listener) error {
	if s.Append == nil {
		return errors.New("localingest: Server.Append is required")
	}
	if !peerCredentialsSupported {
		return fmt.Errorf("localingest: peer credentials are not available on this platform: %w", errors.ErrUnsupported)
	}
	if !s.track(l) {
		_ = l.Close()
		return ErrServerClosed
	}
	defer s.untrack(l)
	var retry time.Duration
	for {
		conn, err := l.Accept()
		if err != nil {
			if s.isClosed() {
				return ErrServerClosed
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			if temporaryAcceptError(err) {
				// Out of descriptors or memory for a moment, as net/http treats it: wait and accept
				// again rather than stop serving for good.
				if retry == 0 {
					retry = 5 * time.Millisecond
				} else if retry *= 2; retry > time.Second {
					retry = time.Second
				}
				s.logf(noUID, "accept failed, retrying in %v: %v", retry, err)
				time.Sleep(retry)
				continue
			}
			// Anything else ends serving. The listener is closed so the socket goes away and
			// clients fall back at once, rather than queueing in a backlog nothing accepts.
			_ = l.Close()
			return err
		}
		retry = 0
		select {
		case s.slots <- struct{}{}:
		default:
			s.logf(noUID, "refusing connection: %d already open", cap(s.slots))
			_ = conn.Close()
			continue
		}
		if !s.trackConn(conn) {
			<-s.slots
			_ = conn.Close()
			return ErrServerClosed
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { <-s.slots }()
			defer s.untrackConn(conn)
			s.handle(conn)
		}()
	}
}

// Close stops every Serve call, closes open connections, and waits for their handlers.
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	var err error
	for l := range s.listeners {
		if cerr := l.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return err
}

func (s *Server) init() {
	if s.listeners == nil {
		s.listeners = map[net.Listener]struct{}{}
		s.conns = map[net.Conn]struct{}{}
		s.buckets = map[uint32]*bucket{}
		s.perUID = map[uint32]int{}
		n := s.MaxConns
		if n <= 0 {
			n = defaultMaxConns
		}
		s.slots = make(chan struct{}, n)
	}
}

func (s *Server) track(l net.Listener) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.init()
	s.listeners[l] = struct{}{}
	return true
}

func (s *Server) untrack(l net.Listener) {
	s.mu.Lock()
	delete(s.listeners, l)
	s.mu.Unlock()
}

func (s *Server) trackConn(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *Server) untrackConn(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	_ = c.Close()
}

func (s *Server) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// noUID keys log lines that belong to no one connection.
const noUID = ^uint32(0)

// logInterval is the least time between two log lines about one account. Any account can connect,
// and each refused connection or rejected line is something to log, so without a bound one
// account could fill the root collector's log as fast as it can send.
const logInterval = 10 * time.Second

type logState struct {
	last       time.Time
	suppressed int
}

func (s *Server) logf(uid uint32, format string, args ...interface{}) {
	if s.Logf == nil {
		return
	}
	now := time.Now()
	s.mu.Lock()
	if s.logStates == nil {
		s.logStates = map[uint32]*logState{}
	}
	st := s.logStates[uid]
	if st == nil {
		st = &logState{}
		s.logStates[uid] = st
	}
	if !st.last.IsZero() && now.Sub(st.last) < logInterval {
		st.suppressed++
		s.mu.Unlock()
		return
	}
	suppressed := st.suppressed
	st.last, st.suppressed = now, 0
	s.mu.Unlock()
	msg := fmt.Sprintf(format, args...)
	if suppressed > 0 {
		msg = fmt.Sprintf("%s (and %d more like it from this account since the last report)", msg, suppressed)
	}
	s.Logf("%s", msg)
}

func (s *Server) handle(conn net.Conn) {
	timeout := s.ConnTimeout
	if timeout <= 0 {
		timeout = defaultConnTimeout
	}
	// Reads may run past the budget by replyGrace: a read that fails at the budget itself would
	// end the connection with no reply after lines were already written, and the client would
	// send them all again. Past the budget the loop below stops handling lines and replies.
	budget := time.Now().Add(timeout)
	_ = conn.SetDeadline(budget.Add(replyGrace))

	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		s.logf(noUID, "refusing connection: not a Unix socket")
		return
	}
	peer, err := peerCredentials(unixConn)
	if err != nil {
		s.logf(noUID, "refusing connection: %v", err)
		return
	}
	if !s.acquireUID(peer.UID) {
		s.logf(peer.UID, "refusing connection from uid %d: it already has the most connections one account may hold", peer.UID)
		return
	}
	defer s.releaseUID(peer.UID)
	// The name is resolved once per connection, before any line, so a slow directory service is
	// paid for once and never between writes.
	var lookup func(uint32) string
	if s.LookupName != nil {
		name := s.LookupName(peer.UID)
		lookup = func(uint32) string { return name }
	}

	r := bufio.NewReaderSize(conn, MaxLineBytes+2)
	preamble, tooLong, err := readLine(r, len(Preamble)+1)
	if err != nil && !errors.Is(err, io.EOF) || tooLong || string(preamble) != Preamble {
		s.logf(peer.UID, "refusing connection from uid %d: no %s preamble", peer.UID, Preamble)
		writeResponse(conn, Response{Error: "expected " + Preamble})
		return
	}

	var resp Response
	stopped := false
	headerLine, tooLong, err := readLine(r, maxHeaderBytes)
	var header Header
	switch {
	case err != nil && !errors.Is(err, io.EOF), tooLong, json.Unmarshal(headerLine, &header) != nil:
		s.logf(peer.UID, "refusing connection from uid %d: no valid header", peer.UID)
		writeResponse(conn, Response{Error: "expected a header"})
		return
	case s.LogPath != "" && !sameLog(header.Log, s.LogPath):
		// Read and drop the rest, so the client can finish writing and read why.
		resp.Error = errLogMismatch
		stopped = true
	}
	lines := 0
	for {
		line, tooLong, err := readLine(r, MaxLineBytes)
		if err != nil && !errors.Is(err, io.EOF) {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				// Out of time with the rest of the batch unread, or a line cut short: whatever is
				// unread is not handled, and the reply says how far this got so the client resends
				// only the rest.
				if resp.Error == "" {
					resp.Error = "busy"
				}
				break
			}
			// The connection broke (a reset): there is nobody to reply to.
			s.logf(peer.UID, "connection from uid %d ended early: %v", peer.UID, err)
			return
		}
		if len(line) == 0 && !tooLong {
			if err != nil {
				break
			}
			continue // a blank line carries nothing
		}
		if stopped {
			if err != nil {
				break
			}
			continue // drain, so the client can finish writing and read the reply
		}
		lines++
		switch {
		case time.Now().After(budget):
			// Out of time: stop writing and say how far this got, while the client still waits.
			resp.Error = "busy"
			stopped = true
		case lines > MaxLinesPerConn:
			resp.Error = fmt.Sprintf("batch is over %d lines", MaxLinesPerConn)
			stopped = true
		case tooLong:
			resp.Rejected++
			s.logf(peer.UID, "rejected an event from uid %d: over %d bytes", peer.UID, MaxLineBytes)
		case !s.allow(peer.UID, len(line)):
			resp.Error = "rate limited"
			stopped = true
		default:
			stamped, stampErr := StampLine(line, peer, lookup)
			if stampErr != nil {
				resp.Rejected++
				s.logf(peer.UID, "rejected an event from uid %d: %v", peer.UID, stampErr)
				break
			}
			if appendErr := s.Append(stamped); appendErr != nil {
				if errors.Is(appendErr, ErrRejected) {
					resp.Rejected++
					break
				}
				resp.Error = "write failed"
				s.logf(peer.UID, "could not write an event from uid %d: %v", peer.UID, appendErr)
				stopped = true
				break
			}
			resp.Accepted++
		}
		if err != nil {
			break
		}
	}
	resp.Processed = resp.Accepted + resp.Rejected
	_ = conn.SetWriteDeadline(time.Now().Add(replyGrace))
	writeResponse(conn, resp)
}

func (s *Server) acquireUID(uid uint32) bool {
	limit := s.MaxConnsPerUID
	if limit <= 0 {
		limit = defaultMaxConnsPerUID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.perUID[uid] >= limit {
		return false
	}
	s.perUID[uid]++
	return true
}

func (s *Server) releaseUID(uid uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.perUID[uid] <= 1 {
		delete(s.perUID, uid)
		return
	}
	s.perUID[uid]--
}

func sameLog(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func writeResponse(w io.Writer, resp Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = w.Write(append(data, '\n'))
}

// readLine reads up to the next newline. A line longer than max is consumed to its end and
// reported as tooLong with no content. err is the read error that ended the line, if any; a final
// line with no newline before EOF is returned with io.EOF.
func readLine(r *bufio.Reader, max int) (line []byte, tooLong bool, err error) {
	var buf []byte
	for {
		chunk, readErr := r.ReadSlice('\n')
		if !tooLong {
			if len(buf)+len(chunk) > max+1 {
				tooLong = true
				buf = nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if readErr == bufio.ErrBufferFull {
			continue
		}
		if tooLong {
			return nil, true, readErr
		}
		buf = bytes.TrimSuffix(buf, []byte{'\n'})
		if len(buf) > max {
			return nil, true, readErr
		}
		return buf, false, readErr
	}
}

// bucket is a token bucket for one account.
type bucket struct {
	tokens float64
	last   time.Time
}

// allow charges size bytes to uid's bucket.
func (s *Server) allow(uid uint32, size int) bool {
	rate := s.RateBytesPerSecond
	if rate < 0 {
		return true
	}
	if rate == 0 {
		rate = defaultRateBytesPerSecond
	}
	burst := s.RateBurstBytes
	if burst <= 0 {
		burst = defaultRateBurstBytes
	}
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.buckets[uid]
	if b == nil {
		b = &bucket{tokens: burst, last: now}
		s.buckets[uid] = b
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * rate
		if b.tokens > burst {
			b.tokens = burst
		}
		b.last = now
	}
	cost := float64(size + 1)
	if b.tokens < cost {
		return false
	}
	b.tokens -= cost
	return true
}
