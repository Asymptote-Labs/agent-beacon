//go:build !windows

package diagnostics

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func restrictedLogAt(t *testing.T, fileMode os.FileMode, listen bool) string {
	t.Helper()
	path := logAt(t, 0o750, fileMode)
	socketDir, err := os.MkdirTemp("", "bdsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "hooks.sock")
	if listen {
		l, err := net.Listen("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = l.Close() })
	}
	// The marker needs a real group id; as root (gid 0) stand one in and give the log to it.
	gid := os.Getgid()
	if gid == 0 {
		gid = 4242
		if err := os.Chown(path, 0, gid); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(asymptoteobserve.UseRestrictedLogMarkerForTest(filepath.Join(t.TempDir(), "log-access.json")))
	if err := asymptoteobserve.WriteRestrictedLogMarker(asymptoteobserve.RestrictedLog{LogPath: path, Socket: socket, Group: asymptoteobserve.RuntimeLogGroup, GID: gid}); err != nil {
		t.Fatal(err)
	}
	return path
}

// In restricted mode a log no hook can write is the point, not a fault.
func TestRestrictedLogPermissionsAcceptAGroupOnlyLog(t *testing.T) {
	path := restrictedLogAt(t, 0o640, true)
	if got := checkLogPermissions(path, false); got.Status != StatusOK || got.Evidence != "restricted_log" {
		t.Fatalf("check = %+v, want ok with restricted_log", got)
	}
}

func TestRestrictedLogPermissionsRejectAWorldReadableLog(t *testing.T) {
	path := restrictedLogAt(t, 0o644, true)
	if got := checkLogPermissions(path, false); got.Status != StatusFail || got.Evidence != "restricted_log_open_to_others" {
		t.Fatalf("check = %+v, want a failure: other users can read the log", got)
	}
}

func TestRestrictedLogPermissionsReportANotListeningCollector(t *testing.T) {
	path := restrictedLogAt(t, 0o640, false)
	if got := checkLogPermissions(path, false); got.Status != StatusFail || got.Evidence != "restricted_log_socket_missing" {
		t.Fatalf("check = %+v, want a failure: hook events are being lost", got)
	}
}
