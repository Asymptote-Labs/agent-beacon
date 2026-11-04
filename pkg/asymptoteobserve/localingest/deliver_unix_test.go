//go:build linux || darwin

package localingest

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// testLog is the log the deliverer tests write; their servers accept any.
const testLog = "/var/log/beacon-agent/runtime.jsonl"

func deliverer(t *testing.T, socketPath string) Deliverer {
	t.Helper()
	lastSendFailure.Store(0)
	t.Cleanup(func() { lastSendFailure.Store(0) })
	return Deliverer{
		Client: Client{SocketPath: socketPath, TrustedOwner: os.Geteuid(), Timeout: time.Second},
		Spool:  Spool{Dir: filepath.Join(t.TempDir(), "spool")},
		Euid:   func() int { return 1000 },
		// Each Write stands in for its own hook process unless a test says otherwise.
		FailureBackoff: -1,
	}
}

// directLog stands in for the writer's own append: it records lines, or refuses them the way a
// restricted log refuses an ordinary account.
type directLog struct {
	mu      sync.Mutex
	lines   []string
	refuse  bool
	failErr error
}

func (l *directLog) append(line []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.refuse {
		return &fs.PathError{Op: "open", Path: testLog, Err: fs.ErrPermission}
	}
	if l.failErr != nil {
		return l.failErr
	}
	l.lines = append(l.lines, string(line))
	return nil
}

func (l *directLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

func staleSocket(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(shortTempDir(t), "run", "ingest.sock")
	l, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	_ = l.Close()
	return sock
}

func TestDeliverApplies(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	system := filepath.Join(t.TempDir(), "runtime.jsonl")
	private := filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")

	user := Deliverer{Euid: func() int { return 501 }}
	root := Deliverer{Euid: func() int { return 0 }}
	if !user.Applies(system) {
		t.Fatal("an ordinary account writing the system log must go through the collector")
	}
	if user.Applies(private) {
		t.Fatal("a per-user log is written directly")
	}
	if root.Applies(system) {
		t.Fatal("root writes the system log directly")
	}
}

func TestWriteWithoutASocketAppendsDirectly(t *testing.T) {
	d := deliverer(t, filepath.Join(shortTempDir(t), "none.sock"))
	log := &directLog{}
	if err := d.Write(testLog, lines(t, 1)[0], log.append); err != nil {
		t.Fatal(err)
	}
	if log.count() != 1 {
		t.Fatalf("direct log has %d lines", log.count())
	}
	if n, _ := d.Pending(testLog); n != 0 {
		t.Fatal("spooled with no socket at all")
	}
}

func TestWriteThatDoesNotApplyNeverTouchesTheSocket(t *testing.T) {
	h := startServer(t, nil)
	d := deliverer(t, h.path)
	d.Euid = func() int { return 0 }
	log := &directLog{}
	if err := d.Write(testLog, lines(t, 1)[0], log.append); err != nil {
		t.Fatal(err)
	}
	if h.rec.count() != 0 || log.count() != 1 {
		t.Fatalf("collector %d, direct %d; root writes directly", h.rec.count(), log.count())
	}
}

func TestWriteSendsThroughTheSocket(t *testing.T) {
	h := startServer(t, nil)
	d := deliverer(t, h.path)
	log := &directLog{}
	if err := d.Write(testLog, lines(t, 1)[0], log.append); err != nil {
		t.Fatal(err)
	}
	if h.rec.count() != 1 || log.count() != 0 {
		t.Fatalf("collector %d, direct %d", h.rec.count(), log.count())
	}
}

// A socket a crashed collector left behind must not swallow a shared endpoint's events: the direct
// append works there, and nothing should be spooled.
func TestWriteThroughAStaleSocketToASharedLogAppendsDirectly(t *testing.T) {
	d := deliverer(t, staleSocket(t))
	log := &directLog{}
	if err := d.Write(testLog, lines(t, 1)[0], log.append); err != nil {
		t.Fatal(err)
	}
	if log.count() != 1 {
		t.Fatalf("direct log has %d lines", log.count())
	}
	if n, _ := d.Pending(testLog); n != 0 {
		t.Fatalf("Pending = %d; a writable log must not spool", n)
	}
}

func TestWriteSpoolsWhenARestrictedLogRefusesAndDrainsAfter(t *testing.T) {
	sock := staleSocket(t)
	d := deliverer(t, sock)
	log := &directLog{refuse: true}
	batch := lines(t, 3)
	for _, line := range batch[:2] {
		if err := d.Write(testLog, line, log.append); err != nil {
			t.Fatalf("Write with the collector down = %v; want it spooled", err)
		}
	}
	if n, _ := d.Pending(testLog); n != 2 {
		t.Fatalf("Pending = %d, want 2", n)
	}

	// The collector comes back on the same path.
	l, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	s := &Server{Append: rec.append}
	go func() { _ = s.Serve(l) }()
	t.Cleanup(func() { _ = s.Close() })

	if err := d.Write(testLog, batch[2], log.append); err != nil {
		t.Fatal(err)
	}
	if rec.count() != 3 {
		t.Fatalf("collector received %d lines, want the new one and both spooled", rec.count())
	}
	if n, _ := d.Pending(testLog); n != 0 {
		t.Fatalf("Pending after drain = %d", n)
	}
	got := map[string]bool{}
	for _, line := range rec.lines {
		got[decode(t, line)["message"].(string)] = true
	}
	for _, want := range []string{"m-0", "m-1", "m-2"} {
		if !got[want] {
			t.Fatalf("event %s never reached the collector: %v", want, got)
		}
	}
}

// Switching a restricted endpoint back to shared must not strand what was spooled meanwhile: the
// next direct append drains it.
func TestASpoolDrainsByDirectAppendOnceTheLogIsWritable(t *testing.T) {
	d := deliverer(t, filepath.Join(shortTempDir(t), "none.sock"))
	log := &directLog{refuse: true}
	_ = d.Write(testLog, lines(t, 1)[0], log.append)
	if n, _ := d.Pending(testLog); n != 1 {
		t.Fatalf("Pending = %d", n)
	}
	log.refuse = false
	if err := d.Write(testLog, lines(t, 1)[0], log.append); err != nil {
		t.Fatal(err)
	}
	if log.count() != 2 {
		t.Fatalf("direct log has %d lines; want the new one and the spooled one", log.count())
	}
	if n, _ := d.Pending(testLog); n != 0 {
		t.Fatalf("Pending = %d", n)
	}
}

func TestEachLogHasItsOwnSpool(t *testing.T) {
	d := deliverer(t, filepath.Join(shortTempDir(t), "none.sock"))
	refused := &directLog{refuse: true}
	other := "/tmp/elsewhere/runtime.jsonl"
	_ = d.Write(other, lines(t, 1)[0], refused.append)
	if n, _ := d.Pending(other); n != 1 {
		t.Fatalf("Pending(other) = %d", n)
	}
	if n, _ := d.Pending(testLog); n != 0 {
		t.Fatalf("a line meant for another log is waiting for the system log: %d", n)
	}
	system := &directLog{}
	_ = d.Write(testLog, lines(t, 1)[0], system.append)
	if system.count() != 1 {
		t.Fatalf("system log got %d lines; another log's spool must not drain into it", system.count())
	}
}

func TestWriteReportsOtherAppendErrors(t *testing.T) {
	d := deliverer(t, filepath.Join(shortTempDir(t), "none.sock"))
	log := &directLog{failErr: errors.New("disk full")}
	if err := d.Write(testLog, lines(t, 1)[0], log.append); err == nil {
		t.Fatal("an append error that is not a refusal was swallowed")
	}
	if n, _ := d.Pending(testLog); n != 0 {
		t.Fatal("spooled an error that spooling cannot fix")
	}
}

func TestWriteReportsARejectedEvent(t *testing.T) {
	h := startServer(t, nil)
	d := deliverer(t, h.path)
	log := &directLog{}
	err := d.Write(testLog, []byte(`{"not":"an event"}`), log.append)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("Write = %v, want ErrRejected", err)
	}
	if n, _ := d.Pending(testLog); n != 0 || log.count() != 0 {
		t.Fatal("a rejected event was spooled or written directly")
	}
}

func TestWriteFailsWhenItCanNeitherWriteNorSpool(t *testing.T) {
	d := deliverer(t, staleSocket(t))
	d.Spool.MaxBytes = 1
	log := &directLog{refuse: true}
	if err := d.Write(testLog, lines(t, 1)[0], log.append); err == nil {
		t.Fatal("Write succeeded with nowhere to put the line")
	}
}

func TestWriteDoesNotSendToAnUntrustedSocket(t *testing.T) {
	h := startServer(t, nil)
	d := deliverer(t, h.path)
	d.Client.TrustedOwner = os.Geteuid() + 1
	log := &directLog{}
	if err := d.Write(testLog, lines(t, 1)[0], log.append); err != nil {
		t.Fatal(err)
	}
	if h.rec.count() != 0 || log.count() != 1 {
		t.Fatalf("collector %d, direct %d", h.rec.count(), log.count())
	}
}

func TestWriteForAnotherLogIsLeftToTheDirectAppend(t *testing.T) {
	h := startServer(t, func(s *Server) { s.LogPath = testLog })
	d := deliverer(t, h.path)
	log := &directLog{}
	if err := d.Write("/tmp/elsewhere/runtime.jsonl", lines(t, 1)[0], log.append); err != nil {
		t.Fatal(err)
	}
	if h.rec.count() != 0 || log.count() != 1 {
		t.Fatalf("collector %d, direct %d", h.rec.count(), log.count())
	}
}

// hungCollector listens on a fresh ingest socket, accepts connections and never answers them. The
// returned func reports how many connections writers have made so far without relying on timing:
// it connects a probe from a named address and collects accepted connections until the probe's
// arrives. A listening Unix socket accepts in arrival order, so every earlier connection is counted.
func hungCollector(t *testing.T) (string, func() int) {
	t.Helper()
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "run", "ingest.sock")
	l, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	var held []net.Conn
	t.Cleanup(func() {
		_ = l.Close()
		for {
			select {
			case c := <-accepted:
				held = append(held, c)
				continue
			case <-done:
			}
			break
		}
		for _, c := range held {
			_ = c.Close()
		}
	})
	writers, probes := 0, 0
	return sock, func() int {
		t.Helper()
		probes++
		probeAddr := filepath.Join(dir, fmt.Sprintf("probe%d", probes))
		probe, err := net.DialUnix("unix", &net.UnixAddr{Name: probeAddr, Net: "unix"}, &net.UnixAddr{Name: sock, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		defer probe.Close()
		for {
			c := <-accepted
			held = append(held, c)
			if c.RemoteAddr() != nil && c.RemoteAddr().String() == probeAddr {
				return writers
			}
			writers++
		}
	}
}

func TestWriteWaitsOutAHungCollectorOnce(t *testing.T) {
	sock, connections := hungCollector(t)
	d := deliverer(t, sock)
	d.Client.Timeout = 200 * time.Millisecond
	d.FailureBackoff = 0 // the default: one process, several events
	log := &directLog{refuse: true}
	for i := 0; i < 3; i++ {
		if err := d.Write(testLog, lines(t, 1)[0], log.append); err != nil {
			t.Fatal(err)
		}
		if got := connections(); got != 1 {
			t.Fatalf("after write %d the collector saw %d connections; only the first write may try it", i, got)
		}
	}
	if n, _ := d.Pending(testLog); n != 3 {
		t.Fatalf("Pending = %d", n)
	}
}

func TestWriteRetriesTheSocketOnceTheBackoffPasses(t *testing.T) {
	h := startServer(t, nil)
	d := deliverer(t, h.path)
	d.FailureBackoff = 0
	lastSendFailure.Store(time.Now().Add(-defaultFailureBackoff - time.Second).UnixNano())
	if err := d.Write(testLog, lines(t, 1)[0], (&directLog{}).append); err != nil {
		t.Fatal(err)
	}
	if h.rec.count() != 1 || lastSendFailure.Load() != 0 {
		t.Fatal("did not retry the socket after the backoff, or did not clear the failure")
	}
}

// Hooks run as a new process per event, so a hung collector must cost the next process nothing
// either: the failure is remembered in the account's spool directory, not only in memory.
func TestASendFailureIsRememberedAcrossProcesses(t *testing.T) {
	sock, connections := hungCollector(t)
	d := deliverer(t, sock)
	d.Client.Timeout = 200 * time.Millisecond
	d.FailureBackoff = 0
	log := &directLog{refuse: true}
	if err := d.Write(testLog, lines(t, 1)[0], log.append); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d.Spool.Dir, sendFailureMarker)); err != nil {
		t.Fatalf("the failure was not recorded: %v", err)
	}
	if got := connections(); got != 1 {
		t.Fatalf("the first write made %d connections, want 1", got)
	}
	lastSendFailure.Store(0) // a new process
	if err := d.Write(testLog, lines(t, 1)[0], log.append); err != nil {
		t.Fatal(err)
	}
	if got := connections(); got != 1 {
		t.Fatalf("the next process tried the hung collector again (%d connections in all)", got)
	}
	if n, _ := d.Pending(testLog); n != 2 {
		t.Fatalf("Pending = %d, want both lines spooled", n)
	}
}

func TestASuccessfulSendClearsTheRememberedFailure(t *testing.T) {
	h := startServer(t, nil)
	d := deliverer(t, h.path)
	d.FailureBackoff = 0
	marker := filepath.Join(d.Spool.Dir, sendFailureMarker)
	if err := os.MkdirAll(d.Spool.Dir, SpoolDirMode); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-defaultFailureBackoff - time.Second)
	if err := os.WriteFile(marker, nil, SpoolFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(marker, old, old); err != nil {
		t.Fatal(err)
	}
	if err := d.Write(testLog, lines(t, 1)[0], (&directLog{}).append); err != nil {
		t.Fatal(err)
	}
	if h.rec.count() != 1 {
		t.Fatal("an expired failure kept the socket from being tried")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the failure marker survived a successful send: %v", err)
	}
}

// A collector that serves some other log will never take this line, so spooling it would only
// strand it: the refusal is returned instead.
func TestARefusedLineForALogTheCollectorDoesNotServeIsNotSpooled(t *testing.T) {
	h := startServer(t, func(s *Server) { s.LogPath = testLog })
	d := deliverer(t, h.path)
	other := "/tmp/elsewhere/runtime.jsonl"
	log := &directLog{refuse: true}
	err := d.Write(other, lines(t, 1)[0], log.append)
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("Write = %v, want the refusal", err)
	}
	if n, _ := d.Pending(other); n != 0 {
		t.Fatalf("Pending = %d; nothing will ever drain it", n)
	}
}

func TestWriteOutcome(t *testing.T) {
	h := startServer(t, nil)
	if got, err := deliverer(t, h.path).WriteOutcome(testLog, lines(t, 1)[0], (&directLog{}).append); err != nil || got != Sent {
		t.Fatalf("through the socket: %v, %v", got, err)
	}
	none := filepath.Join(shortTempDir(t), "none.sock")
	if got, err := deliverer(t, none).WriteOutcome(testLog, lines(t, 1)[0], (&directLog{}).append); err != nil || got != Appended {
		t.Fatalf("direct: %v, %v", got, err)
	}
	if got, err := deliverer(t, none).WriteOutcome(testLog, lines(t, 1)[0], (&directLog{refuse: true}).append); err != nil || got != Spooled {
		t.Fatalf("refused: %v, %v", got, err)
	}
}

func TestDefaultSpoolDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := DefaultSpoolDir(), filepath.Join(home, ".beacon", "endpoint", "spool"); got != want {
		t.Fatalf("DefaultSpoolDir = %q, want %q", got, want)
	}
}
