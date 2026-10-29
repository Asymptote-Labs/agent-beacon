//go:build linux || darwin

package writer

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
)

type collectorStub struct {
	mu  sync.Mutex
	got [][]byte
}

func (c *collectorStub) lines() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.got...)
}

// useIngest makes the writer an ordinary account writing a system log (outside the test's home)
// on an endpoint whose collector serves logPath, and returns that log's path.
func useIngest(t *testing.T, serve bool) (string, *collectorStub) {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	base, err := os.MkdirTemp("", "bw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	_ = os.Chmod(base, 0o755)
	sock := filepath.Join(base, "run", "ingest.sock")
	logPath := filepath.Join(t.TempDir(), "beacon-agent", "runtime.jsonl")

	prev := Ingest
	Ingest = localingest.Deliverer{
		Client:         localingest.Client{SocketPath: sock, TrustedOwner: os.Geteuid(), Timeout: time.Second},
		Spool:          localingest.Spool{Dir: filepath.Join(t.TempDir(), "spool")},
		Euid:           func() int { return 1000 },
		FailureBackoff: -1,
	}
	t.Cleanup(func() { Ingest = prev })

	stub := &collectorStub{}
	l, err := localingest.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	if !serve {
		// A collector that died without removing its socket.
		l.SetUnlinkOnClose(false)
		_ = l.Close()
		return logPath, stub
	}
	server := &localingest.Server{LogPath: logPath, Append: func(line []byte) error {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		stub.got = append(stub.got, append([]byte(nil), line...))
		return nil
	}}
	go func() { _ = server.Serve(l) }()
	t.Cleanup(func() { _ = server.Close() })
	return logPath, stub
}

func ingestEvent(message string) schema.Event {
	return schema.NewEvent(schema.NewEventOptions{
		Action:  "agent.detected",
		Harness: schema.HarnessInfo{Name: "test"},
		Message: message,
	})
}

func TestAppendEventGoesThroughTheCollectorOnARestrictedEndpoint(t *testing.T) {
	logPath, stub := useIngest(t, true)
	path, err := AppendEvent(ingestEvent("via socket"), Options{Path: logPath})
	if err != nil || path != logPath {
		t.Fatalf("AppendEvent = %q, %v", path, err)
	}
	got := stub.lines()
	if len(got) != 1 {
		t.Fatalf("collector received %d lines", len(got))
	}
	var e map[string]interface{}
	if err := json.Unmarshal(got[0], &e); err != nil {
		t.Fatal(err)
	}
	if e["message"] != "via socket" {
		t.Fatalf("message = %v", e["message"])
	}
	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the writer also touched the log directly")
	}
}

func TestAppendEventForAnotherLogIsWrittenDirectly(t *testing.T) {
	_, stub := useIngest(t, true)
	other := filepath.Join(t.TempDir(), "elsewhere", "runtime.jsonl")
	if _, err := AppendEvent(ingestEvent("custom path"), Options{Path: other}); err != nil {
		t.Fatal(err)
	}
	if len(stub.lines()) != 0 {
		t.Fatal("an event for another log went to the collector")
	}
	if data, err := os.ReadFile(other); err != nil || len(data) == 0 {
		t.Fatalf("the other log was not written: %v", err)
	}
}

func TestAppendEventThroughAStaleSocketToASharedLogIsWrittenDirectly(t *testing.T) {
	logPath, _ := useIngest(t, false)
	if _, err := AppendEvent(ingestEvent("stale"), Options{Path: logPath}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(logPath); err != nil || len(data) == 0 {
		t.Fatalf("the shared log was not written: %v", err)
	}
	if n, _ := Ingest.Pending(logPath); n != 0 {
		t.Fatalf("Pending = %d", n)
	}
}

func TestAppendEventSpoolsWhenARestrictedLogRefusesIt(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root is never refused by a file mode")
	}
	logPath, _ := useIngest(t, false)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, nil, 0o440); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendEvent(ingestEvent("later"), Options{Path: logPath}); err != nil {
		t.Fatalf("AppendEvent with the collector down: %v", err)
	}
	if n, _ := Ingest.Pending(logPath); n != 1 {
		t.Fatalf("Pending = %d", n)
	}
}

// A backfill sets a guard and a budget, which only a direct writer holding the log's lock can
// honor, so it never goes through the socket.
func TestGuardedAppendsAreWrittenDirectly(t *testing.T) {
	logPath, stub := useIngest(t, true)
	if _, err := AppendEvent(ingestEvent("budgeted"), Options{Path: logPath, Budget: NewBudget(1 << 20)}); err != nil {
		t.Fatal(err)
	}
	if len(stub.lines()) != 0 {
		t.Fatal("a budgeted append went through the socket")
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("the budgeted append was not written directly: %v", err)
	}
}
