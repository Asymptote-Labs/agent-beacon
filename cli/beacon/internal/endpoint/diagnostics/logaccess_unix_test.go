//go:build linux || darwin

package diagnostics

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
)

func markRestricted(t *testing.T, marked bool) {
	t.Helper()
	prev := logMarkedRestricted
	logMarkedRestricted = func(string) bool { return marked }
	t.Cleanup(func() { logMarkedRestricted = prev })
}

type ingestEnv struct {
	deliverer localingest.Deliverer
	sock      string
}

func newIngestEnv(t *testing.T, serve bool) ingestEnv {
	t.Helper()
	base, err := os.MkdirTemp("", "bd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	_ = os.Chmod(base, 0o755)
	sock := filepath.Join(base, "run", "ingest.sock")
	env := ingestEnv{sock: sock, deliverer: localingest.Deliverer{
		Client: localingest.Client{SocketPath: sock, TrustedOwner: os.Geteuid(), Timeout: time.Second},
		Spool:  localingest.Spool{Dir: filepath.Join(t.TempDir(), "spool")},
	}}
	if serve {
		l, err := localingest.Listen(sock)
		if err != nil {
			t.Fatal(err)
		}
		s := &localingest.Server{Append: func([]byte) error { return nil }}
		go func() { _ = s.Serve(l) }()
		t.Cleanup(func() { _ = s.Close() })
	}
	return env
}

func systemConfig(t *testing.T, access string) endpointconfig.Config {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	dir := filepath.Join(t.TempDir(), "beacon-agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return endpointconfig.Config{LogPath: filepath.Join(dir, "runtime.jsonl"), LogAccess: access}
}

func TestLogAccessShared(t *testing.T) {
	markRestricted(t, false)
	c := checkLogAccess(systemConfig(t, ""), newIngestEnv(t, false).deliverer)
	if c.Status != StatusOK || c.Evidence != "log_access_shared" {
		t.Fatalf("check = %+v", c)
	}
}

func TestLogAccessMarkedButConfiguredShared(t *testing.T) {
	markRestricted(t, true)
	c := checkLogAccess(systemConfig(t, "shared"), newIngestEnv(t, false).deliverer)
	if c.Status != StatusWarn || c.Evidence != "log_access_mismatch" || c.Action == "" {
		t.Fatalf("check = %+v", c)
	}
}

func TestLogAccessRestrictedWithoutItsMarker(t *testing.T) {
	markRestricted(t, false)
	c := checkLogAccess(systemConfig(t, "restricted"), newIngestEnv(t, true).deliverer)
	if c.Status != StatusFail || c.Evidence != "log_access_marker_missing" {
		t.Fatalf("check = %+v", c)
	}
}

func TestLogAccessRestrictedWithoutTheSocket(t *testing.T) {
	markRestricted(t, true)
	env := newIngestEnv(t, false)
	c := checkLogAccess(systemConfig(t, "restricted"), env.deliverer)
	if c.Status != StatusFail || c.Evidence != "ingest_socket_missing" || c.Target != env.sock {
		t.Fatalf("check = %+v", c)
	}
}

func TestLogAccessRestrictedWithAnUntrustedSocket(t *testing.T) {
	markRestricted(t, true)
	env := newIngestEnv(t, true)
	env.deliverer.Client.TrustedOwner = os.Geteuid() + 1
	c := checkLogAccess(systemConfig(t, "restricted"), env.deliverer)
	if c.Status != StatusFail || c.Evidence != "ingest_socket_untrusted" {
		t.Fatalf("check = %+v", c)
	}
}

// A collector that was killed leaves its socket behind. Only a connection tells that from a live
// one, so doctor must not report the endpoint healthy.
func TestLogAccessRestrictedWithADeadSocket(t *testing.T) {
	markRestricted(t, true)
	env := newIngestEnv(t, false)
	l, err := localingest.Listen(env.sock)
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	_ = l.Close()
	c := checkLogAccess(systemConfig(t, "restricted"), env.deliverer)
	if c.Status != StatusFail || c.Evidence != "ingest_socket_unresponsive" {
		t.Fatalf("check = %+v", c)
	}
}

func TestLogAccessRestrictedWithACollectorForAnotherLog(t *testing.T) {
	markRestricted(t, true)
	env := newIngestEnv(t, false)
	l, err := localingest.Listen(env.sock)
	if err != nil {
		t.Fatal(err)
	}
	s := &localingest.Server{LogPath: "/elsewhere/runtime.jsonl", Append: func([]byte) error { return nil }}
	go func() { _ = s.Serve(l) }()
	t.Cleanup(func() { _ = s.Close() })
	c := checkLogAccess(systemConfig(t, "restricted"), env.deliverer)
	if c.Status != StatusFail || c.Evidence != "ingest_log_mismatch" {
		t.Fatalf("check = %+v", c)
	}
}

func TestLogAccessRestrictedAndHealthy(t *testing.T) {
	markRestricted(t, true)
	c := checkLogAccess(systemConfig(t, "restricted"), newIngestEnv(t, true).deliverer)
	if c.Status != StatusOK || c.Evidence != "log_access_restricted" {
		t.Fatalf("check = %+v", c)
	}
}

func TestLogAccessRestrictedWithSpooledEvents(t *testing.T) {
	markRestricted(t, true)
	env := newIngestEnv(t, true)
	cfg := systemConfig(t, "restricted")
	spoolTwo(t, env.deliverer, cfg.LogPath)
	c := checkLogAccess(cfg, env.deliverer)
	if c.Status != StatusWarn || c.Evidence != "ingest_spool_pending" {
		t.Fatalf("check = %+v", c)
	}
}

func TestRestrictedLogPermissions(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	cfg := systemConfig(t, "restricted")
	dir := filepath.Dir(cfg.LogPath)

	if c := checkRestrictedLogPermissions(cfg.LogPath); c.Evidence != "restricted_log_dir_too_open" || c.Status != StatusFail {
		t.Fatalf("0755 directory: %+v", c)
	}
	if err := os.Chmod(dir, 0o750|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	if c := checkRestrictedLogPermissions(cfg.LogPath); c.Evidence != "runtime_log_missing" {
		t.Fatalf("no log yet: %+v", c)
	}
	if err := os.WriteFile(cfg.LogPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(cfg.LogPath, 0o644)
	if c := checkRestrictedLogPermissions(cfg.LogPath); c.Evidence != "restricted_log_too_open" || c.Status != StatusFail {
		t.Fatalf("0644 log: %+v", c)
	}
	_ = os.Chmod(cfg.LogPath, 0o640)
	if c := checkRestrictedLogPermissions(cfg.LogPath); c.Status != StatusOK {
		t.Fatalf("0640 log: %+v", c)
	}
	// The world-writable check a shared log needs must not fire on a restricted one.
	if c := checkLogPermissionsFor(cfg); c.Status != StatusOK {
		t.Fatalf("checkLogPermissionsFor = %+v", c)
	}
}

func TestRestrictedLogPermissionsAsAnAccountThatCannotLook(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can always look")
	}
	cfg := systemConfig(t, "restricted")
	dir := filepath.Dir(cfg.LogPath)
	if err := os.WriteFile(cfg.LogPath, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o710); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o750)
	_ = os.Chmod(cfg.LogPath, 0o000)
	c := checkRestrictedLogPermissions(cfg.LogPath)
	if c.Status == StatusFail {
		t.Fatalf("an unreadable restricted log is the mode working, not a failure: %+v", c)
	}
}

// spoolTwo leaves two events in the deliverer's spool for logPath, the way a hook does when a
// restricted log refuses it and no collector answers.
func spoolTwo(t *testing.T, d localingest.Deliverer, logPath string) {
	t.Helper()
	d.Euid = func() int { return 1000 }
	d.FailureBackoff = -1
	// No collector answers while these are written; the spool is per log, not per socket.
	d.Client.SocketPath = filepath.Join(t.TempDir(), "absent.sock")
	refuse := func([]byte) error { return &fs.PathError{Op: "open", Path: logPath, Err: fs.ErrPermission} }
	for i := 0; i < 2; i++ {
		if err := d.Write(logPath, []byte(`{"a":1}`), refuse); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := d.Pending(logPath); n != 2 {
		t.Fatalf("fixture: Pending = %d", n)
	}
}

func TestLogAccessSharedWithSpooledEvents(t *testing.T) {
	markRestricted(t, false)
	env := newIngestEnv(t, false)
	cfg := systemConfig(t, "")
	spoolTwo(t, env.deliverer, cfg.LogPath)
	c := checkLogAccess(cfg, env.deliverer)
	if c.Status != StatusWarn || c.Evidence != "ingest_spool_pending" {
		t.Fatalf("check = %+v", c)
	}
}
