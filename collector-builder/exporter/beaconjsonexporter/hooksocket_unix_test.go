//go:build unix

package beaconjsonexporter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const hookSocketTestLine = `{"vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"action":"prompt.submitted","id":"a"}}`

// In restricted log mode the exporter is the log's only writer: hooks send it lines over the hook
// socket and it appends them. Each signal builds its own exporter, and they share one listener
// that lives until the last of them shuts down.
func TestHookSocketAppendsHookLinesAndIsSharedAcrossSignals(t *testing.T) {
	socketDir, err := os.MkdirTemp("", "bxsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "hooks.sock")
	logs, path := newTestExporter(t, func(cfg *Config) { cfg.HookSocket = socket })
	traces, _ := newTestExporter(t, func(cfg *Config) { cfg.HookSocket = socket; cfg.Path = path })

	ctx := context.Background()
	if err := logs.start(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := traces.start(ctx, nil); err != nil {
		t.Fatalf("the second signal could not share the socket: %v", err)
	}
	if err := asymptoteobserve.SendRuntimeLogLine(socket, []byte(hookSocketTestLine), 0); err != nil {
		t.Fatal(err)
	}
	if got := readLog(t, path); got != hookSocketTestLine {
		t.Fatalf("log = %q, want exactly the hook line", got)
	}
	// Anything that is not an event is refused and never reaches the log.
	if err := asymptoteobserve.SendRuntimeLogLine(socket, []byte("garbage"), 0); err == nil {
		t.Fatal("the socket accepted a line that is not an event")
	}

	if err := logs.shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := asymptoteobserve.SendRuntimeLogLine(socket, []byte(strings.Replace(hookSocketTestLine, `"a"`, `"b"`, 1)), 0); err != nil {
		t.Fatalf("the socket closed while another signal still used it: %v", err)
	}
	if err := traces.shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatalf("socket left behind after the last shutdown: %v", err)
	}
}

func TestHookSocketMustBeAbsolute(t *testing.T) {
	cfg := createDefaultConfig()
	cfg.Path = "/tmp/runtime.jsonl"
	cfg.HookSocket = "hooks.sock"
	if err := cfg.Validate(); err == nil {
		t.Fatal("a relative hook_socket was accepted")
	}
}
