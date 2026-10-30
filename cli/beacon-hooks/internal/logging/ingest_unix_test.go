//go:build linux || darwin

package logging

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
)

type ingestFixture struct {
	sock    string
	logPath string
	mu      sync.Mutex
	got     [][]byte
	server  *localingest.Server
}

func (f *ingestFixture) received() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.got...)
}

func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// useIngest points the hook writer at a socket of the test's own, as an ordinary account, and at a
// system log outside the test's home.
func useIngest(t *testing.T) *ingestFixture {
	t.Helper()
	setHome(t, t.TempDir())
	base := shortSocketDir(t)
	f := &ingestFixture{
		sock:    filepath.Join(base, "run", "ingest.sock"),
		logPath: filepath.Join(t.TempDir(), "beacon-agent", "runtime.jsonl"),
	}
	prev := EndpointIngest
	EndpointIngest = localingest.Deliverer{
		Client: localingest.Client{SocketPath: f.sock, TrustedOwner: os.Geteuid(), Timeout: time.Second},
		Spool:  localingest.Spool{Dir: filepath.Join(t.TempDir(), "spool")},
		Euid:   func() int { return 1000 },
		// Each emit stands in for a separate hook process, which never remembers an earlier
		// process's failed send.
		FailureBackoff: -1,
		CollectorLog:   f.logPath,
	}
	t.Cleanup(func() { EndpointIngest = prev })
	t.Setenv("BEACON_ENDPOINT_LOG", f.logPath)
	return f
}

func setHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
}

func (f *ingestFixture) start(t *testing.T) {
	t.Helper()
	l, err := localingest.Listen(f.sock)
	if err != nil {
		t.Fatal(err)
	}
	f.server = &localingest.Server{Append: func(line []byte) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.got = append(f.got, append([]byte(nil), line...))
		return nil
	}}
	go func() { _ = f.server.Serve(l) }()
	t.Cleanup(func() { _ = f.server.Close() })
}

// leaveStaleSocket leaves the socket a collector that died without cleaning up would leave.
func (f *ingestFixture) leaveStaleSocket(t *testing.T) {
	t.Helper()
	l, err := localingest.Listen(f.sock)
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	_ = l.Close()
}

func emit(t *testing.T, message string) {
	t.Helper()
	logger := NewLoggerForPlatform("pre-tool", "claude_code")
	if err := logger.EndpointEvent("tool.invoked", "tool", "info", message, map[string]interface{}{
		"session": map[string]interface{}{"id": "sess-" + message},
	}); err != nil {
		t.Fatalf("EndpointEvent(%s): %v", message, err)
	}
}

func messages(t *testing.T, lines [][]byte) []string {
	t.Helper()
	var out []string
	for _, line := range lines {
		var e map[string]interface{}
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("not JSON: %s", line)
		}
		out = append(out, fmt.Sprint(e["message"]))
	}
	return out
}

func TestHookEventsGoThroughTheIngestSocket(t *testing.T) {
	f := useIngest(t)
	f.start(t)
	emit(t, "one")
	emit(t, "two")
	got := messages(t, f.received())
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("collector received %v", got)
	}
	if _, err := os.Stat(f.logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the hook touched the log directly: %v", err)
	}
	// The line is the same complete event the hook would have appended: it carries an id.
	var e map[string]interface{}
	_ = json.Unmarshal(f.received()[0], &e)
	if id, _ := e["event"].(map[string]interface{})["id"].(string); id == "" {
		t.Fatal("the event reached the collector without its event.id")
	}
}

func TestHookEventsWithoutASocketAreWrittenDirectly(t *testing.T) {
	f := useIngest(t)
	emit(t, "direct")
	lines := readLines(t, f.logPath)
	if len(lines) != 1 || messages(t, lines)[0] != "direct" {
		t.Fatalf("log = %s", lines)
	}
}

// A socket a crashed collector left behind must not swallow a shared endpoint's events.
func TestHookEventsThroughAStaleSocketOnASharedLogAreWrittenDirectly(t *testing.T) {
	f := useIngest(t)
	f.leaveStaleSocket(t)
	emit(t, "stale")
	if lines := readLines(t, f.logPath); len(lines) != 1 {
		t.Fatalf("log has %d lines", len(lines))
	}
	if n, _ := EndpointIngest.Pending(f.logPath); n != 0 {
		t.Fatalf("Pending = %d; a writable log must not spool", n)
	}
}

// A restricted log refuses this account's direct append. While the collector is down the event is
// spooled rather than lost, and it arrives with the next event once the collector is back.
func TestHookEventsSpoolWhenARestrictedLogRefusesThemAndArriveAfter(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root is never refused by a file mode")
	}
	f := useIngest(t)
	if err := os.MkdirAll(filepath.Dir(f.logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.logPath, nil, 0o440); err != nil {
		t.Fatal(err)
	}
	f.leaveStaleSocket(t)
	emit(t, "while-down")
	if n, _ := EndpointIngest.Pending(f.logPath); n != 1 {
		t.Fatalf("Pending = %d, want the refused event spooled", n)
	}
	f.start(t)
	emit(t, "after")
	got := messages(t, f.received())
	if len(got) != 2 || got[0] != "after" || got[1] != "while-down" {
		t.Fatalf("collector received %v; want the new event and the spooled one", got)
	}
	if n, _ := EndpointIngest.Pending(f.logPath); n != 0 {
		t.Fatalf("Pending after recovery = %d", n)
	}
}

func TestAPrivateLogNeverUsesTheSocket(t *testing.T) {
	f := useIngest(t)
	f.start(t)
	home := t.TempDir()
	setHome(t, home)
	private := filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", private)
	emit(t, "mine")
	if len(f.received()) != 0 {
		t.Fatal("a per-user log's event went to the collector")
	}
	if lines := readLines(t, private); len(lines) != 1 {
		t.Fatalf("private log has %d lines", len(lines))
	}
}

func TestRootHooksWriteDirectly(t *testing.T) {
	f := useIngest(t)
	f.start(t)
	EndpointIngest.Euid = func() int { return 0 }
	emit(t, "as-root")
	if len(f.received()) != 0 {
		t.Fatal("root's event went through the socket")
	}
	if lines := readLines(t, f.logPath); len(lines) != 1 {
		t.Fatalf("log has %d lines", len(lines))
	}
}

func readLines(t *testing.T, path string) [][]byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var out [][]byte
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		out = append(out, append([]byte(nil), scanner.Bytes()...))
	}
	return out
}
