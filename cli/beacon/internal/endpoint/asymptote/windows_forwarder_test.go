package asymptote

import (
	"errors"
	"os"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

// scmForwarder behaves like the Windows service backend: its definition is an SCM registration
// that os.Stat cannot see, and a reconcile registers a missing one again, as it must after the MSI's
// uninstall step deleted it during an upgrade.
type scmForwarder struct {
	*fakeForwarder
	registered bool
	writes     int
}

func (f *scmForwarder) Installed() bool            { return f.registered }
func (f *scmForwarder) RestoresRegistration() bool { return true }
func (f *scmForwarder) UnitPath() (string, error) {
	return `HKLM\SYSTEM\CurrentControlSet\Services\BeaconAsymptoteForwarder`, nil
}
func (f *scmForwarder) UnitCurrent(string, string) (bool, error) {
	if !f.registered {
		return false, errors.New("The specified service does not exist as an installed service.")
	}
	return true, nil
}
func (f *scmForwarder) WriteUnit(vectorBin, configPath string) (string, error) {
	f.writes++
	f.registered = true
	return f.UnitPath()
}
func (f *scmForwarder) RemoveUnits() { f.removed++; f.registered = false }
func (f *scmForwarder) Unload() error {
	f.unloads++
	f.registered = false
	return nil
}

// An MSI upgrade removed the service and kept the connection; the install that follows registers
// it again and starts it, without touching the key or Vector's checkpoints.
func TestReconcileRegistersARemovedWindowsServiceAgainAndStartsIt(t *testing.T) {
	f := newReconcileFixture(t, "standard")
	scm := &scmForwarder{fakeForwarder: f.fwd}
	loads := f.fwd.loads

	result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: scm, Restart: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reregistered || !result.Restarted || !result.UnitUpdated || result.Warning != "" || result.Updated {
		t.Fatalf("result = %+v, want the service registered again and started with the config kept", result)
	}
	if scm.writes != 1 || !scm.registered || f.fwd.loads != loads+1 {
		t.Fatalf("writes=%d registered=%t loads=%d->%d", scm.writes, scm.registered, loads, f.fwd.loads)
	}
	if msg := result.Message(); msg != "Beacon Cloud forwarder service registered again and started." {
		t.Fatalf("message = %q", msg)
	}
	f.assertCredentialsAndCheckpointsUntouched(t)

	// Registered and running now: a second reconcile is the usual restart, not a registration.
	again, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: scm, Restart: true})
	if err != nil {
		t.Fatal(err)
	}
	if again.Reregistered || !again.Unchanged || !again.Restarted || scm.writes != 1 {
		t.Fatalf("second reconcile = %+v writes=%d", again, scm.writes)
	}
}

// An MSI upgrade told not to start services gets the registration back, stopped.
func TestReconcileWithoutStartingRegistersTheWindowsServiceOnly(t *testing.T) {
	f := newReconcileFixture(t, "metadata-only")
	scm := &scmForwarder{fakeForwarder: f.fwd}
	loads := f.fwd.loads

	result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: scm, NoStart: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reregistered || result.Restarted || result.Warning != "" || !scm.registered || f.fwd.loads != loads {
		t.Fatalf("result = %+v registered=%t loads=%d->%d; want registered and not started", result, scm.registered, loads, f.fwd.loads)
	}
	// A registered forwarder is not restarted either when services are not started.
	writeStaleConfig(t)
	updated, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: scm, NoStart: true})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Updated || updated.Restarted || f.fwd.loads != loads {
		t.Fatalf("result = %+v loads=%d->%d; want the config updated and nothing restarted", updated, loads, f.fwd.loads)
	}
}

// A Vector that rejects the kept config is not registered and started; the warning names connect.
func TestReconcileDoesNotRegisterAServiceForAConfigVectorRejects(t *testing.T) {
	f := newReconcileFixture(t, "standard")
	scm := &scmForwarder{fakeForwarder: f.fwd}
	enrollment, err := LoadEnrollment(true)
	if err != nil {
		t.Fatal(err)
	}
	enrollment.VectorBin = fakeVector(t, "0.56.0", 78)
	if err := SaveEnrollment(true, *enrollment); err != nil {
		t.Fatal(err)
	}
	result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: scm, Restart: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reregistered || scm.registered || result.Warning == "" {
		t.Fatalf("result = %+v registered=%t; want a warning and no registration", result, scm.registered)
	}
}

// The Windows package's uninstall removes only the service and keeps everything a reconnect would
// otherwise have to rotate the key to rebuild.
func TestDisconnectKeepingForwarderStateRemovesOnlyTheService(t *testing.T) {
	f := newReconcileFixture(t, "standard")
	scm := &scmForwarder{fakeForwarder: f.fwd, registered: true}
	cfg, err := endpointconfig.Load(true)
	if err != nil {
		t.Fatal(err)
	}
	if err := Disconnect(DisconnectOptions{UserMode: true, LogPath: cfg.LogPath, Forwarder: scm, KeepCredentials: true, KeepForwarderState: true}); err != nil {
		t.Fatal(err)
	}
	if scm.registered || f.fwd.unloads == 0 {
		t.Fatal("the service was not removed")
	}
	f.assertCredentialsAndCheckpointsUntouched(t)
	if _, err := os.Stat(VectorConfigPath(true)); err != nil {
		t.Fatalf("vector.toml was removed: %v", err)
	}
	kept, err := endpointconfig.Load(true)
	if err != nil || kept.ManagedIngest == nil {
		t.Fatalf("the managed_ingest block was cleared: %v", err)
	}
}

// supervisedForwarder stands in for the Windows user-mode forwarder, which nothing restarts after
// a sign-out: a restarting reconcile starts it even when it is not running.
type supervisedForwarder struct{ *fakeForwarder }

func (supervisedForwarder) StartsWhenStopped() bool { return true }

func TestRestartingReconcileStartsAStoppedSupervisedForwarder(t *testing.T) {
	f := newReconcileFixture(t, "standard")
	f.fwd.stopped = true
	loads := f.fwd.loads

	// A service manager's stopped forwarder stays stopped.
	if result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: f.fwd, Restart: true}); err != nil || result.Restarted || f.fwd.loads != loads {
		t.Fatalf("a stopped service was started: %+v %v", result, err)
	}
	result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: supervisedForwarder{f.fwd}, Restart: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Restarted || f.fwd.loads != loads+1 {
		t.Fatalf("result = %+v loads=%d->%d; want the supervised forwarder started", result, loads, f.fwd.loads)
	}
	// Without a restart requested it is left alone.
	if result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: supervisedForwarder{f.fwd}}); err != nil || result.Restarted {
		t.Fatalf("a reconcile without Restart started it: %+v %v", result, err)
	}
}
