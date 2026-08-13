package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordingService notes what rollback asked of the service manager, in order, interleaved with
// the file restores so the sequence can be asserted rather than just the set of calls.
type recordingService struct {
	calls *[]string
}

func (r recordingService) Unload() error {
	*r.calls = append(*r.calls, "unload")
	return nil
}

func (r recordingService) Load() error {
	*r.calls = append(*r.calls, "load")
	return nil
}

// Rollback has to distinguish a service this install started from one that was already running.
//
// Unloading and leaving it that way is right for the first: nothing was running before, and a
// half-installed endpoint left registered is worse than one left absent. It is wrong for the
// second. A reinstall over a healthy endpoint that fails partway would otherwise leave the
// collector stopped and, on systemd, disabled -- surviving reboot. The upgrade would not merely
// fail, it would end collection.
func TestRollbackRestoresAServiceItFoundRunning(t *testing.T) {
	for name, tc := range map[string]struct {
		loaded, wasRunning bool
		want               []string
	}{
		"started by this install":         {loaded: true, wasRunning: false, want: []string{"unload"}},
		"already running before it":       {loaded: true, wasRunning: true, want: []string{"unload", "load"}},
		"never got as far as the service": {loaded: false, wasRunning: true, want: nil},
		"nothing loaded, nothing running": {loaded: false, wasRunning: false, want: nil},
	} {
		t.Run(name, func(t *testing.T) {
			var calls []string
			r := &installRollback{
				Manager:           recordingService{calls: &calls},
				ServiceLoaded:     tc.loaded,
				ServiceWasRunning: tc.wasRunning,
				snapshots:         map[string]fileSnapshot{},
			}

			r.Rollback(Manifest{})

			if strings.Join(calls, ",") != strings.Join(tc.want, ",") {
				t.Errorf("rollback called %v, want %v", calls, tc.want)
			}
		})
	}
}

// The stop has to happen before the tracked files are restored, and this is the ordering the
// supervised backend makes load-bearing: its "unit file" is its pidfile, so restoring it puts back
// the pid from before the install -- a process already terminated. Unloading after that restore
// looks up a dead pid, stops nothing, and leaves the collector this failed install started running
// on the new config, holding the ports, with a second one started beside it.
func TestRollbackStopsBeforeRestoringTheServiceStateFile(t *testing.T) {
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "collector.pid")
	if err := os.WriteFile(pidfile, []byte(`{"pid":100}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var calls []string
	r := &installRollback{
		Manager:           recordingService{calls: &calls},
		ServiceLoaded:     true,
		ServiceWasRunning: true,
		snapshots:         map[string]fileSnapshot{},
	}
	// Track it the way Install does: the snapshot is taken while the file still holds the
	// pre-install pid.
	r.Track(pidfile)
	if err := os.WriteFile(pidfile, []byte(`{"pid":200}`), 0o644); err != nil {
		t.Fatal(err)
	}
	calls = append(calls, "restore-boundary")

	r.Rollback(Manifest{})

	unloadAt, boundaryAt := indexOf(calls, "unload"), indexOf(calls, "restore-boundary")
	if unloadAt < 0 {
		t.Fatalf("rollback never stopped the service: %v", calls)
	}
	if unloadAt < boundaryAt {
		t.Fatalf("calls = %v: unload must come after the snapshot was taken", calls)
	}
	// The restored file must be the pre-install one, and the stop must already have happened by
	// the time it lands.
	data, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "100") {
		t.Errorf("pidfile = %s, want the pre-install state restored", data)
	}
	if got := strings.Join(calls, ","); !strings.Contains(got, "unload,load") {
		t.Errorf("calls = %v, want the service stopped and then brought back", calls)
	}
}

func indexOf(haystack []string, needle string) int {
	for i, v := range haystack {
		if v == needle {
			return i
		}
	}
	return -1
}
