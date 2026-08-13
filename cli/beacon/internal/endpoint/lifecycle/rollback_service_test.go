package lifecycle

import (
	"strings"
	"testing"
)

// recordingService notes what rollback asked of the service manager, in order.
type recordingService struct{ calls []string }

func (r *recordingService) Unload() error  { r.calls = append(r.calls, "unload"); return nil }
func (r *recordingService) Restart() error { r.calls = append(r.calls, "restart"); return nil }

// Rollback has to distinguish a service this install started from one that was already running.
//
// Unloading is right for the first: leaving a half-installed endpoint registered is worse than
// removing it. It is wrong for the second. A reinstall over a healthy endpoint that fails partway
// -- readiness timing out, a harness config erroring -- would otherwise stop *and disable* the
// collector that was working before the attempt, and on systemd that disable survives reboot. The
// upgrade would not merely fail; it would take collection down with it.
func TestRollbackDoesNotDisableAServiceItFoundRunning(t *testing.T) {
	for name, tc := range map[string]struct {
		loaded, wasRunning bool
		want               []string
	}{
		"started by this install":         {loaded: true, wasRunning: false, want: []string{"unload"}},
		"already running before it":       {loaded: true, wasRunning: true, want: []string{"restart"}},
		"never got as far as the service": {loaded: false, wasRunning: true, want: nil},
		"nothing loaded, nothing running": {loaded: false, wasRunning: false, want: nil},
	} {
		t.Run(name, func(t *testing.T) {
			svc := &recordingService{}
			r := &installRollback{
				Manager:           svc,
				ServiceLoaded:     tc.loaded,
				ServiceWasRunning: tc.wasRunning,
				snapshots:         map[string]fileSnapshot{},
			}

			r.Rollback(Manifest{})

			if strings.Join(svc.calls, ",") != strings.Join(tc.want, ",") {
				t.Errorf("rollback called %v, want %v", svc.calls, tc.want)
			}
		})
	}
}

// The restart has to happen after the files are restored. The collector reads its configuration at
// startup, so restarting first would bring it up on the very config the failed install wrote.
func TestRollbackRestartsAfterRestoringFiles(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/otelcol.yaml"

	svc := &recordingService{}
	r := &installRollback{
		Manager:           svc,
		ServiceLoaded:     true,
		ServiceWasRunning: true,
		snapshots:         map[string]fileSnapshot{},
	}
	r.Track(path)

	r.Rollback(Manifest{})

	if len(svc.calls) != 1 || svc.calls[0] != "restart" {
		t.Fatalf("service calls = %v, want a single restart", svc.calls)
	}
}
