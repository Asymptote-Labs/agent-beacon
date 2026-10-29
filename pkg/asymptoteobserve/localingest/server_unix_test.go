//go:build linux || darwin

package localingest

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// shortTempDir returns a directory with a path short enough for a Unix socket name, which macOS
// caps at 104 bytes; t.TempDir() under /var/folders can exceed that.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "li")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

type recorder struct {
	mu    sync.Mutex
	lines [][]byte
	fail  func(n int, line []byte) error
}

func (r *recorder) append(line []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		if err := r.fail(len(r.lines), line); err != nil {
			return err
		}
	}
	r.lines = append(r.lines, append([]byte(nil), line...))
	return nil
}

func (r *recorder) setFail(fail func(n int, line []byte) error) {
	r.mu.Lock()
	r.fail = fail
	r.mu.Unlock()
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.lines)
}

type harness struct {
	server *Server
	rec    *recorder
	client Client
	path   string
	done   chan error
}

func startServer(t *testing.T, configure func(*Server)) *harness {
	t.Helper()
	dir := filepath.Join(shortTempDir(t), "run")
	path := filepath.Join(dir, "ingest.sock")
	l, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	s := &Server{
		Append:     rec.append,
		LookupName: func(uid uint32) string { return fmt.Sprintf("user%d", uid) },
	}
	if configure != nil {
		configure(s)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(l) }()
	h := &harness{server: s, rec: rec, path: path, done: done,
		client: Client{SocketPath: path, TrustedOwner: os.Geteuid(), Timeout: 2 * time.Second}}
	t.Cleanup(func() { _ = s.Close() })
	return h
}

func lines(t *testing.T, n int) [][]byte {
	t.Helper()
	out := make([][]byte, n)
	for i := range out {
		i := i
		out[i] = validLine(t, func(e map[string]interface{}) {
			e["event"].(map[string]interface{})["id"] = fmt.Sprintf("id-%d", i)
			e["message"] = fmt.Sprintf("m-%d", i)
			e["user"] = map[string]interface{}{"name": "root", "uid": "0"}
		})
	}
	return out
}

// opening is the preamble and a header naming no log, which a server without a LogPath accepts.
const opening = Preamble + "\n" + `{"log":""}` + "\n"

// rawExchange speaks the protocol by hand, for what a well-behaved Client never sends.
func rawExchange(t *testing.T, path string, payload string) (Response, error) {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(payload)); err != nil {
		return Response{}, err
	}
	_ = conn.(*net.UnixConn).CloseWrite()
	reply, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.Unmarshal(reply, &resp); err != nil {
		t.Fatalf("reply %q: %v", reply, err)
	}
	return resp, nil
}

func TestRoundTripStampsTheSendersKernelIdentity(t *testing.T) {
	h := startServer(t, nil)
	resp, err := h.client.Send(lines(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Accepted != 3 || resp.Processed != 3 || resp.Rejected != 0 {
		t.Fatalf("resp = %+v", resp)
	}
	if h.rec.count() != 3 {
		t.Fatalf("wrote %d lines, want 3", h.rec.count())
	}
	uid := fmt.Sprint(os.Geteuid())
	for i, line := range h.rec.lines {
		user := decode(t, line)["user"].(map[string]interface{})
		// Every line claimed root; the kernel says otherwise.
		if user["uid"] != uid || user["name"] != "user"+uid {
			t.Fatalf("line %d user = %#v, want uid %s", i, user, uid)
		}
		if m := decode(t, line)["message"]; m != fmt.Sprintf("m-%d", i) {
			t.Fatalf("line %d arrived out of order: %v", i, m)
		}
	}
}

func TestPeerCredentialsMatchTheConnectingProcess(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "p.sock")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	got := make(chan Peer, 1)
	go func() {
		conn, err := l.AcceptUnix()
		if err != nil {
			return
		}
		defer conn.Close()
		peer, err := peerCredentials(conn)
		if err != nil {
			t.Error(err)
		}
		got <- peer
	}()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	peer := <-got
	if int(peer.UID) != os.Geteuid() || int(peer.GID) != os.Getegid() {
		t.Fatalf("peer = %+v, want uid %d gid %d", peer, os.Geteuid(), os.Getegid())
	}
	if peer.PID != int32(os.Getpid()) {
		t.Fatalf("peer pid = %d, want %d", peer.PID, os.Getpid())
	}
}

func TestMixedBatchCountsRejectedLinesAndKeepsGoing(t *testing.T) {
	h := startServer(t, nil)
	batch := lines(t, 3)
	batch = append(batch[:1], append([][]byte{[]byte(`{"vendor":"evil"}`), []byte(`not json`)}, batch[1:]...)...)
	resp, err := h.client.Send(batch)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Accepted != 3 || resp.Rejected != 2 || resp.Processed != 5 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestAWriteFailureStopsTheBatchAndReportsHowFarItGot(t *testing.T) {
	h := startServer(t, nil)
	h.rec.setFail(func(n int, _ []byte) error {
		if n == 1 {
			return errors.New("disk full")
		}
		return nil
	})
	resp, err := h.client.Send(lines(t, 4))
	if err == nil {
		t.Fatal("Send succeeded past a write failure")
	}
	if resp.Processed != 1 || resp.Accepted != 1 || resp.Error != "write failed" {
		t.Fatalf("resp = %+v, want one processed and the failure reported", resp)
	}
	if h.rec.count() != 1 {
		t.Fatalf("wrote %d lines after the failure", h.rec.count())
	}
	// The reply must not leak the server-side error text to the client.
	if strings.Contains(err.Error(), "disk full") {
		t.Fatalf("error %q exposes the server's internal error", err)
	}
}

func TestAnAppendRejectionDropsOnlyThatLine(t *testing.T) {
	h := startServer(t, nil)
	h.rec.setFail(func(n int, _ []byte) error {
		if n == 0 {
			h.rec.fail = nil // runs under h.rec.mu
			return fmt.Errorf("refused: %w", ErrRejected)
		}
		return nil
	})
	resp, err := h.client.Send(lines(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Rejected != 1 || resp.Accepted != 2 || resp.Processed != 3 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestClientSplitsLargeBatches(t *testing.T) {
	h := startServer(t, func(s *Server) { s.RateBytesPerSecond = -1 })
	n := MaxLinesPerConn*2 + 17
	resp, err := h.client.Send(lines(t, n))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Accepted != n || h.rec.count() != n {
		t.Fatalf("resp = %+v, wrote %d, want %d", resp, h.rec.count(), n)
	}
}

func TestServerStopsABatchOverTheLineLimit(t *testing.T) {
	h := startServer(t, func(s *Server) { s.RateBytesPerSecond = -1 })
	var b strings.Builder
	b.WriteString(opening)
	for _, line := range lines(t, MaxLinesPerConn+5) {
		b.Write(line)
		b.WriteByte('\n')
	}
	resp, err := rawExchange(t, h.path, b.String())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Processed != MaxLinesPerConn || resp.Error == "" {
		t.Fatalf("resp = %+v, want the batch cut at %d", resp, MaxLinesPerConn)
	}
	if h.rec.count() != MaxLinesPerConn {
		t.Fatalf("wrote %d", h.rec.count())
	}
}

func TestServerRefusesAConnectionWithoutThePreamble(t *testing.T) {
	h := startServer(t, nil)
	resp, err := rawExchange(t, h.path, string(lines(t, 1)[0])+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error == "" || resp.Processed != 0 {
		t.Fatalf("resp = %+v", resp)
	}
	if h.rec.count() != 0 {
		t.Fatal("an event without the preamble was written")
	}
}

func TestServerSkipsAnOversizedLineAndReadsTheNext(t *testing.T) {
	h := startServer(t, nil)
	good := lines(t, 1)[0]
	payload := opening + strings.Repeat("x", MaxLineBytes*3) + "\n" + string(good) + "\n"
	resp, err := rawExchange(t, h.path, payload)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Rejected != 1 || resp.Accepted != 1 || resp.Processed != 2 {
		t.Fatalf("resp = %+v", resp)
	}
}

const uuidSized = "00000000-0000-0000-0000-000000000000"

func TestServerAcceptsALineAtExactlyTheLimit(t *testing.T) {
	h := startServer(t, nil)
	base := validLine(t, func(e map[string]interface{}) {
		e["message"] = ""
		e["user"] = map[string]interface{}{"uid": fmt.Sprint(os.Geteuid()), "name": fmt.Sprintf("user%d", os.Geteuid())}
		e["event"].(map[string]interface{})["id"] = uuidSized
	})
	line := validLine(t, func(e map[string]interface{}) {
		e["message"] = strings.Repeat("m", MaxLineBytes-len(base))
		e["user"] = map[string]interface{}{"uid": fmt.Sprint(os.Geteuid()), "name": fmt.Sprintf("user%d", os.Geteuid())}
		// The server replaces the id with one it derives, which is a UUID: the same length.
		e["event"].(map[string]interface{})["id"] = uuidSized
	})
	if len(line) != MaxLineBytes {
		t.Fatalf("fixture is %d bytes, want %d", len(line), MaxLineBytes)
	}
	resp, err := h.client.Send([][]byte{line})
	if err != nil || resp.Accepted != 1 {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}
}

func TestServerTakesAFinalLineWithoutANewline(t *testing.T) {
	h := startServer(t, nil)
	resp, err := rawExchange(t, h.path, opening+"\n"+string(lines(t, 1)[0]))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Accepted != 1 || resp.Processed != 1 {
		t.Fatalf("resp = %+v", resp)
	}
}

// A client that stalls mid-batch gets a reply naming how far the server got, so it resends only the
// rest; the line it cut short is not handled.
func TestServerAnswersAConnectionThatStalls(t *testing.T) {
	h := startServer(t, func(s *Server) { s.ConnTimeout = 150 * time.Millisecond })
	conn, err := net.Dial("unix", h.path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	line := lines(t, 1)[0]
	// A whole line, then half of the next, then nothing.
	if _, err := conn.Write([]byte(opening + string(line) + "\n" + string(line[:20]))); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	reply, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("no reply from a server whose client stalled: %v", err)
	}
	var resp Response
	if err := json.Unmarshal(reply, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Processed != 1 || resp.Error != "busy" {
		t.Fatalf("resp = %+v, want the one complete line reported and the rest left", resp)
	}
	if h.rec.count() != 1 {
		t.Fatalf("wrote %d lines, want only the complete one", h.rec.count())
	}
}

// The last line's write running past the budget must not cost the reply: the client would resend
// a line already written.
func TestASlowLastWriteStillGetsAReply(t *testing.T) {
	h := startServer(t, func(s *Server) { s.ConnTimeout = 100 * time.Millisecond })
	h.rec.setFail(func(int, []byte) error {
		time.Sleep(250 * time.Millisecond)
		return nil
	})
	resp, err := h.client.Send(lines(t, 1))
	if err != nil || resp.Accepted != 1 {
		t.Fatalf("Send = %+v, %v; want the written line acknowledged", resp, err)
	}
}

// A batch bigger than the server's read buffer, whose budget runs out after the buffer is used,
// gets a reply that matches what was written.
func TestABusyServerReportsHowFarItGotPastItsReadBuffer(t *testing.T) {
	h := startServer(t, func(s *Server) {
		s.ConnTimeout = 150 * time.Millisecond
		s.RateBytesPerSecond = -1
	})
	h.rec.setFail(func(int, []byte) error {
		time.Sleep(40 * time.Millisecond)
		return nil
	})
	big := make([][]byte, 12)
	for i := range big {
		big[i] = validLine(t, func(e map[string]interface{}) {
			e["message"] = fmt.Sprintf("m-%d-%s", i, strings.Repeat("x", 20*1024))
		})
	}
	resp, err := h.client.Send(big)
	if err == nil || resp.Error != "busy" {
		t.Fatalf("Send = %+v, %v; want the server to stop and report", resp, err)
	}
	if resp.Processed == 0 || resp.Processed != h.rec.count() {
		t.Fatalf("processed %d, wrote %d; the reply must match what was written", resp.Processed, h.rec.count())
	}
}

func TestRateLimitStopsAFloodingAccount(t *testing.T) {
	now := time.Unix(1000, 0)
	h := startServer(t, func(s *Server) {
		size := float64(len(lines(t, 1)[0]) + 1)
		s.RateBytesPerSecond = size
		s.RateBurstBytes = 2*size + 1
		s.now = func() time.Time { return now }
	})
	resp, err := h.client.Send(lines(t, 5))
	if err == nil || resp.Processed != 2 || resp.Error != "rate limited" {
		t.Fatalf("resp = %+v, err = %v; want two through and the rest refused", resp, err)
	}
	now = now.Add(3 * time.Second)
	resp, err = h.client.Send(lines(t, 2))
	if err != nil || resp.Accepted != 2 {
		t.Fatalf("after refill resp = %+v, err = %v", resp, err)
	}
}

func TestServerClosesConnectionsOverTheLimit(t *testing.T) {
	h := startServer(t, func(s *Server) { s.MaxConns = 1 })
	held, err := net.Dial("unix", h.path)
	if err != nil {
		t.Fatal(err)
	}
	// Wait until the server has taken the held connection's slot.
	deadline := time.Now().Add(2 * time.Second)
	for openConns(h.server) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := h.client.Send(lines(t, 1)); err == nil {
		t.Fatal("Send succeeded over the connection limit")
	}
	if h.rec.count() != 0 {
		t.Fatal("an event over the connection limit was written")
	}
	_ = held.Close()
	deadline = time.Now().Add(2 * time.Second)
	for {
		if _, err := h.client.Send(lines(t, 1)); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("Send still failing after the slot freed: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestConcurrentSendersEachLandWhole(t *testing.T) {
	h := startServer(t, func(s *Server) { s.RateBytesPerSecond = -1; s.MaxConnsPerUID = 64 })
	const senders, per = 16, 40
	var wg sync.WaitGroup
	errs := make(chan error, senders)
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.client.Send(lines(t, per)); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if h.rec.count() != senders*per {
		t.Fatalf("wrote %d, want %d", h.rec.count(), senders*per)
	}
	for _, line := range h.rec.lines {
		_ = decode(t, line)
	}
}

func TestCloseStopsServeAndRemovesTheSocket(t *testing.T) {
	h := startServer(t, nil)
	if err := h.server.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-h.done:
		if !errors.Is(err, ErrServerClosed) {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after Close")
	}
	if _, err := os.Lstat(h.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket still present after Close: %v", err)
	}
	if _, err := h.client.Send(lines(t, 1)); !errors.Is(err, ErrNoSocket) {
		t.Fatalf("Send after Close = %v, want ErrNoSocket", err)
	}
}

func TestServeRequiresAppend(t *testing.T) {
	l, err := Listen(filepath.Join(shortTempDir(t), "run", "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := (&Server{}).Serve(l); err == nil {
		t.Fatal("Serve ran without Append")
	}
}

func TestClientRefusesALineWithAnEmbeddedNewline(t *testing.T) {
	h := startServer(t, nil)
	_, err := h.client.Send([][]byte{[]byte("{\"a\":1}\n{\"b\":2}")})
	if err == nil || errors.Is(err, ErrNoSocket) {
		t.Fatalf("err = %v, want a refusal before sending", err)
	}
	if h.rec.count() != 0 {
		t.Fatal("wrote a line")
	}
}

func TestClientWithoutASocket(t *testing.T) {
	c := Client{SocketPath: filepath.Join(shortTempDir(t), "missing.sock"), TrustedOwner: os.Geteuid()}
	if err := c.Available(); !errors.Is(err, ErrNoSocket) {
		t.Fatalf("Available = %v, want ErrNoSocket", err)
	}
	if _, err := c.Send(lines(t, 1)); !errors.Is(err, ErrNoSocket) {
		t.Fatalf("Send = %v, want ErrNoSocket", err)
	}
}

func TestClientRefusesUntrustedSockets(t *testing.T) {
	t.Run("not a socket", func(t *testing.T) {
		dir := shortTempDir(t)
		path := filepath.Join(dir, "ingest.sock")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		c := Client{SocketPath: path, TrustedOwner: os.Geteuid()}
		if _, err := c.Send(lines(t, 1)); !errors.Is(err, ErrUntrustedSocket) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("owned by another account", func(t *testing.T) {
		h := startServer(t, nil)
		c := h.client
		c.TrustedOwner = os.Geteuid() + 1
		if _, err := c.Send(lines(t, 1)); !errors.Is(err, ErrUntrustedSocket) {
			t.Fatalf("err = %v", err)
		}
		if h.rec.count() != 0 {
			t.Fatal("sent to an untrusted socket")
		}
	})
	t.Run("directory writable by others", func(t *testing.T) {
		h := startServer(t, nil)
		if err := os.Chmod(filepath.Dir(h.path), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := h.client.Available(); !errors.Is(err, ErrUntrustedSocket) {
			t.Fatalf("Available = %v", err)
		}
		if _, err := h.client.Send(lines(t, 1)); !errors.Is(err, ErrUntrustedSocket) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("ancestor writable by others without the sticky bit", func(t *testing.T) {
		h := startServer(t, nil)
		ancestor := filepath.Dir(filepath.Dir(h.path))
		if err := os.Chmod(ancestor, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := h.client.Available(); !errors.Is(err, ErrUntrustedSocket) {
			t.Fatalf("Available = %v", err)
		}
		if err := os.Chmod(ancestor, 0o777|os.ModeSticky); err != nil {
			t.Fatal(err)
		}
		if err := h.client.Available(); err != nil {
			t.Fatalf("a sticky ancestor (like /tmp) should be trusted: %v", err)
		}
	})
	t.Run("ancestor writable by an ordinary group", func(t *testing.T) {
		h := startServer(t, nil)
		ancestor := filepath.Dir(filepath.Dir(h.path))
		if err := os.Chmod(ancestor, 0o775); err != nil {
			t.Fatal(err)
		}
		gid := os.Getegid()
		if systemGroups[gid] {
			// Running as root's group: stand in an ordinary group by distrusting it here.
			prev := systemGroups
			systemGroups = map[int]bool{}
			t.Cleanup(func() { systemGroups = prev })
		}
		if err := h.client.Available(); !errors.Is(err, ErrUntrustedSocket) {
			t.Fatalf("Available = %v, want a group-writable ancestor refused", err)
		}
	})
	t.Run("ancestor writable by a system group, as /private/var/run is on macOS", func(t *testing.T) {
		h := startServer(t, nil)
		ancestor := filepath.Dir(filepath.Dir(h.path))
		if err := os.Chmod(ancestor, 0o775); err != nil {
			t.Fatal(err)
		}
		prev := systemGroups
		systemGroups = map[int]bool{os.Getegid(): true}
		t.Cleanup(func() { systemGroups = prev })
		if err := h.client.Available(); err != nil {
			t.Fatalf("Available = %v, want a system group's write trusted", err)
		}
	})
	t.Run("symlink in place of the socket", func(t *testing.T) {
		h := startServer(t, nil)
		link := filepath.Join(filepath.Dir(h.path), "link.sock")
		if err := os.Symlink(h.path, link); err != nil {
			t.Fatal(err)
		}
		c := h.client
		c.SocketPath = link
		if err := c.Available(); !errors.Is(err, ErrUntrustedSocket) {
			t.Fatalf("Available = %v, want a link refused", err)
		}
	})
}

func TestListenSetsModesWhateverTheUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	dir := filepath.Join(shortTempDir(t), "run")
	path := filepath.Join(dir, "ingest.sock")
	l, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != SocketDirMode {
		t.Fatalf("dir mode = %v, want %v", info.Mode().Perm(), SocketDirMode)
	}
	info, err = os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != SocketMode {
		t.Fatalf("socket mode = %v, want a socket with %v", info.Mode(), SocketMode)
	}
}

func TestListenNarrowsAnExistingDirectory(t *testing.T) {
	dir := filepath.Join(shortTempDir(t), "run")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	l, err := Listen(filepath.Join(dir, "ingest.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if info, _ := os.Stat(dir); info.Mode().Perm() != SocketDirMode {
		t.Fatalf("dir mode = %v", info.Mode().Perm())
	}
}

func TestListenReplacesAStaleSocketOnly(t *testing.T) {
	dir := filepath.Join(shortTempDir(t), "run")
	path := filepath.Join(dir, "ingest.sock")

	stale, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	_ = stale.Close()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("fixture: stale socket missing: %v", err)
	}
	live, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen over a stale socket: %v", err)
	}
	defer live.Close()
	go func() {
		for {
			c, err := live.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	if _, err := Listen(path); err == nil || !strings.Contains(err.Error(), "already listening") {
		t.Fatalf("Listen over a live socket = %v, want a refusal", err)
	}
}

func TestListenRefusesToRemoveANonSocket(t *testing.T) {
	dir := filepath.Join(shortTempDir(t), "run")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ingest.sock")
	if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(path); err == nil {
		t.Fatal("Listen replaced a regular file")
	}
	if data, _ := os.ReadFile(path); string(data) != "keep me" {
		t.Fatal("the file was changed")
	}
}

func TestPeerCredentialsAreSupportedHere(t *testing.T) {
	if !peerCredentialsSupported {
		t.Fatalf("peer credentials unsupported on %s", runtime.GOOS)
	}
}

func openConns(s *Server) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

func TestServerRefusesABatchForAnotherLog(t *testing.T) {
	h := startServer(t, func(s *Server) { s.LogPath = "/var/log/beacon-agent/runtime.jsonl" })
	c := h.client
	c.LogPath = "/tmp/some-test/runtime.jsonl"
	resp, err := c.Send(lines(t, 3))
	if !errors.Is(err, ErrLogMismatch) {
		t.Fatalf("Send = %+v, %v; want ErrLogMismatch", resp, err)
	}
	if resp.Processed != 0 || h.rec.count() != 0 {
		t.Fatalf("a batch for another log was written: %+v", resp)
	}
	c.LogPath = "/var/log/beacon-agent/../beacon-agent/runtime.jsonl"
	if resp, err := c.Send(lines(t, 1)); err != nil || resp.Accepted != 1 {
		t.Fatalf("Send for the same log, spelled differently = %+v, %v", resp, err)
	}
}

func TestServerWithALogPathRefusesAHeaderWithoutOne(t *testing.T) {
	h := startServer(t, func(s *Server) { s.LogPath = "/var/log/beacon-agent/runtime.jsonl" })
	resp, err := h.client.Send(lines(t, 1))
	if !errors.Is(err, ErrLogMismatch) || h.rec.count() != 0 {
		t.Fatalf("Send = %+v, %v", resp, err)
	}
}

func TestServerRefusesAMalformedHeader(t *testing.T) {
	h := startServer(t, nil)
	for _, header := range []string{"not json", strings.Repeat("x", maxHeaderBytes+10)} {
		resp, err := rawExchange(t, h.path, Preamble+"\n"+header+"\n"+string(lines(t, 1)[0])+"\n")
		if err != nil {
			t.Fatal(err)
		}
		if resp.Error == "" || resp.Processed != 0 {
			t.Fatalf("header %.20q: resp = %+v", header, resp)
		}
	}
	if h.rec.count() != 0 {
		t.Fatal("an event behind a malformed header was written")
	}
}

// The real default path must pass the trust walk wherever the platform's own directories sit, or
// restricted mode records nothing there. Only the ancestors are checked: the socket itself exists
// only on an installed endpoint.
func TestDefaultSocketDirectoryAncestorsAreTrusted(t *testing.T) {
	dir, err := filepath.EvalSymlinks(filepath.Dir(filepath.Dir(DefaultSocketPath)))
	if err != nil {
		t.Skipf("no %s here: %v", filepath.Dir(filepath.Dir(DefaultSocketPath)), err)
	}
	for {
		info, err := os.Lstat(dir)
		if err != nil {
			t.Fatal(err)
		}
		uid, _ := fileOwner(info)
		if uid != 0 || !ancestorWriteSafe(info) {
			t.Fatalf("%s (%v, uid %d) would make the client refuse %s", dir, info.Mode(), uid, DefaultSocketPath)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

func TestOneAccountCannotHoldEveryConnection(t *testing.T) {
	h := startServer(t, func(s *Server) { s.MaxConnsPerUID = 2 })
	var held []net.Conn
	for i := 0; i < 2; i++ {
		c, err := net.Dial("unix", h.path)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	deadline := time.Now().Add(2 * time.Second)
	for openConns(h.server) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// Wait for both to be counted against the account.
	for time.Now().Before(deadline) {
		h.server.mu.Lock()
		n := h.server.perUID[uint32(os.Geteuid())]
		h.server.mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := h.client.Send(lines(t, 1)); err == nil {
		t.Fatal("a third connection from the same account was served")
	}
	for _, c := range held {
		_ = c.Close()
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		if _, err := h.client.Send(lines(t, 1)); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("still refused after the account's connections closed: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestABusyServerReportsHowFarItGotBeforeTheClientGivesUp(t *testing.T) {
	h := startServer(t, func(s *Server) {
		s.ConnTimeout = 200 * time.Millisecond
		s.RateBytesPerSecond = -1
	})
	h.rec.setFail(func(int, []byte) error {
		time.Sleep(80 * time.Millisecond) // a slow log: lock contention, a slow disk
		return nil
	})
	c := h.client
	c.Timeout = 3 * time.Second
	resp, err := c.Send(lines(t, 10))
	if err == nil || resp.Error != "busy" {
		t.Fatalf("Send = %+v, %v; want the server to stop and report", resp, err)
	}
	if resp.Processed == 0 || resp.Processed >= 10 || resp.Processed != h.rec.count() {
		t.Fatalf("processed %d, wrote %d; the reply must match what was written", resp.Processed, h.rec.count())
	}
}

func TestRemoveStale(t *testing.T) {
	dir := filepath.Join(shortTempDir(t), "run")
	sock := filepath.Join(dir, "ingest.sock")
	stale, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	_ = stale.Close()
	if err := RemoveStale(sock); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a stale socket was left")
	}
	h := startServer(t, nil)
	if err := RemoveStale(h.path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(h.path); err != nil {
		t.Fatal("a live socket was removed")
	}
	file := filepath.Join(dir, "not-a-socket")
	_ = os.WriteFile(file, nil, 0o600)
	if err := RemoveStale(file); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(file); err != nil {
		t.Fatal("a regular file was removed")
	}
	if err := RemoveStale(filepath.Join(dir, "absent")); err != nil {
		t.Fatal(err)
	}
}

func TestProbe(t *testing.T) {
	h := startServer(t, func(s *Server) { s.LogPath = "/var/log/beacon-agent/runtime.jsonl" })
	c := h.client
	c.LogPath = "/var/log/beacon-agent/runtime.jsonl"
	if err := c.Probe(); err != nil {
		t.Fatalf("Probe of a live collector = %v", err)
	}
	if h.rec.count() != 0 {
		t.Fatal("a probe wrote something")
	}
	c.LogPath = "/tmp/other.jsonl"
	if err := c.Probe(); !errors.Is(err, ErrLogMismatch) {
		t.Fatalf("Probe for another log = %v", err)
	}
	stale := filepath.Join(shortTempDir(t), "run", "ingest.sock")
	l, err := Listen(stale)
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	_ = l.Close()
	dead := Client{SocketPath: stale, TrustedOwner: os.Geteuid(), Timeout: time.Second}
	if err := dead.Available(); err != nil {
		t.Fatalf("fixture: Available on a dead socket = %v", err)
	}
	if err := dead.Probe(); err == nil || errors.Is(err, ErrNoSocket) {
		t.Fatalf("Probe of a dead socket = %v; want a connection failure", err)
	}
}

// The client dials the path it checked, with links resolved, so a link it passed through cannot
// be repointed between the check and the dial.
func TestClientDialsTheResolvedSocket(t *testing.T) {
	h := startServer(t, nil)
	link := filepath.Join(shortTempDir(t), "linkdir")
	if err := os.Symlink(filepath.Dir(h.path), link); err != nil {
		t.Fatal(err)
	}
	c := h.client
	c.SocketPath = filepath.Join(link, filepath.Base(h.path))
	socket, err := checkSocket(c.SocketPath, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(h.path)
	if socket != want {
		t.Fatalf("checkSocket returned %q, want the resolved %q", socket, want)
	}
	if _, err := c.Send(lines(t, 1)); err != nil || h.rec.count() != 1 {
		t.Fatalf("Send through a linked directory = %v", err)
	}
}

func TestServeClosesItsListenerWhenAcceptFails(t *testing.T) {
	dir := filepath.Join(shortTempDir(t), "run")
	l, err := Listen(filepath.Join(dir, "ingest.sock"))
	if err != nil {
		t.Fatal(err)
	}
	failing := &failingListener{Listener: l, err: errors.New("accept: something permanent")}
	s := &Server{Append: func([]byte) error { return nil }}
	if err := s.Serve(failing); err == nil || errors.Is(err, ErrServerClosed) {
		t.Fatalf("Serve = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "ingest.sock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a server that stopped serving left its socket for clients to queue on")
	}
}

func TestServeRetriesATemporaryAcceptError(t *testing.T) {
	dir := filepath.Join(shortTempDir(t), "run")
	sock := filepath.Join(dir, "ingest.sock")
	l, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	failing := &failingListener{Listener: l, err: syscall.EMFILE, times: 3}
	rec := &recorder{}
	s := &Server{Append: rec.append}
	go func() { _ = s.Serve(failing) }()
	t.Cleanup(func() { _ = s.Close() })
	c := Client{SocketPath: sock, TrustedOwner: os.Geteuid(), Timeout: 2 * time.Second}
	if _, err := c.Send(lines(t, 1)); err != nil || rec.count() != 1 {
		t.Fatalf("Send after temporary accept errors = %v", err)
	}
}

type failingListener struct {
	net.Listener
	mu    sync.Mutex
	err   error
	times int // 0 means always
}

func (l *failingListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if l.times == 0 || l.times > 0 {
		if l.times > 0 {
			l.times--
			if l.times == 0 {
				l.times = -1
			}
		}
		l.mu.Unlock()
		return nil, &net.OpError{Op: "accept", Net: "unix", Err: l.err}
	}
	l.mu.Unlock()
	return l.Listener.Accept()
}

func TestServerLogsAnAccountAtMostOncePerInterval(t *testing.T) {
	var mu sync.Mutex
	var logged []string
	h := startServer(t, func(s *Server) {
		s.Logf = func(format string, args ...interface{}) {
			mu.Lock()
			logged = append(logged, fmt.Sprintf(format, args...))
			mu.Unlock()
		}
	})
	for i := 0; i < 20; i++ {
		if _, err := rawExchange(t, h.path, "no preamble\n"); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logged) != 1 {
		t.Fatalf("logged %d lines for one account's flood, want 1: %q", len(logged), logged)
	}
}
