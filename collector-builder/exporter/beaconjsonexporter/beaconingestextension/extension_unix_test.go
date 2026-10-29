//go:build linux || darwin

package beaconingestextension

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
	"go.uber.org/zap"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
)

func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bi")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

type fixture struct {
	ext    *ingest
	client localingest.Client
	log    string
}

func start(t *testing.T, mutate func(*Config)) *fixture {
	t.Helper()
	dir := shortDir(t)
	cfg := createDefaultConfig()
	cfg.SocketPath = filepath.Join(dir, "run", "ingest.sock")
	cfg.LogPath = filepath.Join(dir, "log", "runtime.jsonl")
	if mutate != nil {
		mutate(cfg)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.LogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	ext := newIngest(cfg, zap.NewNop())
	if err := ext.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ext.Shutdown(context.Background()) })
	return &fixture{
		ext:    ext,
		log:    cfg.LogPath,
		client: localingest.Client{SocketPath: cfg.SocketPath, TrustedOwner: os.Geteuid(), LogPath: cfg.LogPath},
	}
}

func event(t *testing.T, i int, claimedUser string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]interface{}{
		"timestamp":      fmt.Sprintf("2026-10-29T09:03:%02d.000000000Z", i%60),
		"vendor":         asymptoteobserve.Vendor,
		"product":        asymptoteobserve.Product,
		"schema_version": asymptoteobserve.SchemaVersion,
		"event":          map[string]interface{}{"action": "command.executed", "category": "command", "id": fmt.Sprintf("evt-%d", i)},
		"session":        map[string]interface{}{"id": fmt.Sprintf("s-%d", i)},
		"user":           map[string]interface{}{"name": claimedUser, "uid": "0"},
		"message":        fmt.Sprintf("event %d", i),
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func readLog(t *testing.T, path string) []map[string]interface{} {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]interface{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		var e map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			t.Fatalf("log line is not one JSON object: %q", scanner.Text())
		}
		out = append(out, e)
	}
	return out
}

func TestEventsSentToTheSocketLandInTheLogUnderTheSendersAccount(t *testing.T) {
	f := start(t, nil)
	batch := [][]byte{event(t, 1, "root"), event(t, 2, "someone-else")}
	resp, err := f.client.Send(batch)
	if err != nil || resp.Accepted != 2 {
		t.Fatalf("Send = %+v, %v", resp, err)
	}
	lines := readLog(t, f.log)
	if len(lines) != 2 {
		t.Fatalf("log has %d lines, want 2", len(lines))
	}
	uid := fmt.Sprint(os.Geteuid())
	want := resolveName(uint32(os.Geteuid()))
	for i, line := range lines {
		user := line["user"].(map[string]interface{})
		if user["uid"] != uid {
			t.Fatalf("line %d uid = %v, want the sender's %s", i, user["uid"], uid)
		}
		if want != "" && user["name"] != want {
			t.Fatalf("line %d name = %v, want %q", i, user["name"], want)
		}
		if line["message"] != fmt.Sprintf("event %d", i+1) {
			t.Fatalf("line %d out of order: %v", i, line["message"])
		}
	}
	if _, err := os.Stat(f.log + ".lock"); err != nil {
		t.Fatalf("the shared lock file was not used: %v", err)
	}
}

func TestRejectedEventsNeverReachTheLog(t *testing.T) {
	f := start(t, nil)
	resp, err := f.client.Send([][]byte{
		[]byte(`{"vendor":"beacon","product":"endpoint-agent"}`),
		[]byte(`{"evil":"` + strings.Repeat("x", 100) + `"}`),
		event(t, 1, "x"),
	})
	if err != nil || resp.Rejected != 2 || resp.Accepted != 1 {
		t.Fatalf("Send = %+v, %v", resp, err)
	}
	if got := len(readLog(t, f.log)); got != 1 {
		t.Fatalf("log has %d lines, want only the valid event", got)
	}
}

func TestLogRotatesThroughTheSharedWriter(t *testing.T) {
	f := start(t, func(c *Config) {
		c.RotateBytes = 600
		c.RotateArchives = 2
		c.RateBytesPerSecond = -1
	})
	for i := 0; i < 20; i++ {
		if _, err := f.client.Send([][]byte{event(t, i, "x")}); err != nil {
			t.Fatal(err)
		}
	}
	for _, archive := range []string{f.log + ".1", f.log + ".2"} {
		if _, err := os.Stat(archive); err != nil {
			t.Fatalf("expected archive %s: %v", filepath.Base(archive), err)
		}
	}
	if _, err := os.Stat(f.log + ".3"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("kept more archives than configured")
	}
}

func TestAWriteFailureIsReportedSoTheClientKeepsTheEvents(t *testing.T) {
	f := start(t, nil)
	var calls atomic.Int32
	f.ext.appendLine = func(string, []byte, int64, int) error {
		calls.Add(1)
		return errors.New("read-only file system")
	}
	resp, err := f.client.Send([][]byte{event(t, 1, "x"), event(t, 2, "x")})
	if err == nil || resp.Processed != 0 {
		t.Fatalf("Send = %+v, %v; want nothing processed", resp, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("append called %d times; the batch must stop at the first failure", calls.Load())
	}
}

func TestEventsForAnotherLogAreRefused(t *testing.T) {
	f := start(t, nil)
	c := f.client
	c.LogPath = filepath.Join(t.TempDir(), "runtime.jsonl")
	if _, err := c.Send([][]byte{event(t, 1, "x")}); !errors.Is(err, localingest.ErrLogMismatch) {
		t.Fatalf("Send for another log = %v, want ErrLogMismatch", err)
	}
	if _, err := os.Stat(f.log); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the collector wrote an event meant for another log")
	}
}

func TestShutdownRemovesTheSocket(t *testing.T) {
	f := start(t, nil)
	if err := f.ext.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.ext.Shutdown(context.Background()); err != nil {
		t.Fatalf("a second Shutdown: %v", err)
	}
	if _, err := os.Lstat(f.ext.socketPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket left after Shutdown: %v", err)
	}
	if _, err := f.client.Send([][]byte{event(t, 1, "x")}); !errors.Is(err, localingest.ErrNoSocket) {
		t.Fatalf("Send after Shutdown = %v, want ErrNoSocket so hooks spool", err)
	}
}

func TestShutdownWithoutStart(t *testing.T) {
	cfg := createDefaultConfig()
	cfg.LogPath = "/nonexistent/runtime.jsonl"
	if err := newIngest(cfg, nil).Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestASecondCollectorOnTheSameSocketFailsToStart(t *testing.T) {
	f := start(t, nil)
	cfg := *f.ext.cfg
	second := newIngest(&cfg, zap.NewNop())
	err := second.Start(context.Background(), nil)
	if err == nil {
		_ = second.Shutdown(context.Background())
		t.Fatal("two collectors served one socket")
	}
	// The first is untouched.
	if _, err := f.client.Send([][]byte{event(t, 1, "x")}); err != nil {
		t.Fatalf("the running collector stopped answering: %v", err)
	}
}

func TestStartReplacesAStaleSocket(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, "run", "ingest.sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	_ = stale.Close()
	f := start(t, func(c *Config) { c.SocketPath = sock })
	if _, err := f.client.Send([][]byte{event(t, 1, "x")}); err != nil {
		t.Fatalf("Send after replacing a stale socket: %v", err)
	}
}

// As root, with the log marked restricted, the files the extension creates take the restricted mode.
func TestRestrictedLogFilesAreNotWorldAccessible(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("the restricted marker is trusted only when root owns it")
	}
	dir := shortDir(t)
	logPath := filepath.Join(dir, "log", "runtime.jsonl")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := asymptoteobserve.SetRuntimeLogAccess(logPath, asymptoteobserve.LogAccessRestricted); err != nil {
		t.Fatal(err)
	}
	f := start(t, func(c *Config) { c.LogPath = logPath; c.SocketPath = filepath.Join(dir, "run", "ingest.sock") })
	if _, err := f.client.Send([][]byte{event(t, 1, "x")}); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		logPath:           asymptoteobserve.RestrictedRuntimeLogFileMode,
		logPath + ".lock": asymptoteobserve.RestrictedRuntimeLogLockMode,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode = %o, want %o", filepath.Base(path), info.Mode().Perm(), want)
		}
	}
}

func TestFactory(t *testing.T) {
	factory := NewFactory()
	if factory.Type().String() != "beacon_ingest" {
		t.Fatalf("type = %s", factory.Type())
	}
	cfg := factory.CreateDefaultConfig().(*Config)
	if cfg.SocketPath != localingest.DefaultSocketPath || cfg.RotateBytes <= 0 || cfg.RotateArchives <= 0 {
		t.Fatalf("default config = %+v", cfg)
	}
	set := extension.Settings{ID: component.NewID(factory.Type()), TelemetrySettings: component.TelemetrySettings{Logger: zap.NewNop()}}
	if _, err := factory.Create(context.Background(), set, cfg); err == nil {
		t.Fatal("created an extension without a log path")
	}
	cfg.LogPath = "/var/log/beacon-agent/runtime.jsonl"
	ext, err := factory.Create(context.Background(), set, cfg)
	if err != nil || ext == nil {
		t.Fatalf("Create = %v, %v", ext, err)
	}
}

func TestConfigValidate(t *testing.T) {
	for name, cfg := range map[string]Config{
		"negative rotate":   {LogPath: "x", RotateBytes: -1},
		"negative archives": {LogPath: "x", RotateArchives: -1},
		"negative conns":    {LogPath: "x", MaxConnections: -1},
		"negative burst":    {LogPath: "x", RateBurstBytes: -1},
		"negative per uid":  {LogPath: "x", MaxConnectionsPerUID: -1},
		"no log path":       {},
	} {
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, cfg)
		}
	}
	ok := Config{LogPath: "x", RateBytesPerSecond: -1}
	if err := ok.Validate(); err != nil {
		t.Errorf("a negative rate turns the bound off and must validate: %v", err)
	}
}
