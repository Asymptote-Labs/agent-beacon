package asymptote

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/managedprivacy"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// reconcileFixture is an endpoint connected by a successful Connect, with a forwarder whose unit
// file exists, a checkpoint in Vector's data directory, and the enrollment server shut down so
// any network request a reconcile made would fail.
type reconcileFixture struct {
	fwd        *fakeForwarder
	vector     string
	secrets    []byte
	secretsMod time.Time
	checkpoint string
}

func newReconcileFixture(t *testing.T, privacyMode string) *reconcileFixture {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	isolateVectorDiscovery(t)
	fd := newFakeDashboard(t)
	fwd := &fakeForwarder{supported: true, unitPath: filepath.Join(t.TempDir(), "forwarder.plist")}
	vector := fakeVector(t, "0.56.0", 0)
	opts := connectOptions(t, fd, fwd, vector)
	opts.PrivacyMode = privacyMode
	if _, err := Connect(context.Background(), opts); err != nil {
		t.Fatalf("connect: %v", err)
	}
	fd.server.Close()
	checkpoint := filepath.Join(DataDir(true), "beacon_runtime", "checkpoints.json")
	if err := os.MkdirAll(filepath.Dir(checkpoint), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checkpoint, []byte(`{"checkpoints":[{"fingerprint":{"device_and_inode":[1,2]},"position":42}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	secrets, err := os.ReadFile(SecretsPath(true))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(SecretsPath(true))
	if err != nil {
		t.Fatal(err)
	}
	return &reconcileFixture{fwd: fwd, vector: vector, secrets: secrets, secretsMod: info.ModTime(), checkpoint: checkpoint}
}

// assertCredentialsAndCheckpointsUntouched pins what a reconcile must never change.
func (f *reconcileFixture) assertCredentialsAndCheckpointsUntouched(t *testing.T) {
	t.Helper()
	secrets, err := os.ReadFile(SecretsPath(true))
	if err != nil || !bytes.Equal(secrets, f.secrets) {
		t.Fatalf("the secrets file changed: %v", err)
	}
	if info, err := os.Stat(SecretsPath(true)); err != nil || !info.ModTime().Equal(f.secretsMod) {
		t.Fatalf("the secrets file was rewritten: %v", err)
	}
	if data, err := os.ReadFile(f.checkpoint); err != nil || !strings.Contains(string(data), `"position":42`) {
		t.Fatalf("Vector's checkpoints were reset: %q %v", data, err)
	}
	if ConnectIncomplete(true) || !Connected(true) {
		t.Fatal("a reconcile must leave the endpoint connected")
	}
}

// staleTransform stands in for vector.toml as an older version rendered it: the Metadata-only
// transform before it stripped every content field.
func writeStaleConfig(t *testing.T) []byte {
	t.Helper()
	current, err := os.ReadFile(VectorConfigPath(true))
	if err != nil {
		t.Fatal(err)
	}
	stale := bytes.Replace(current, []byte("[transforms.beacon_runtime_metadata]"), []byte("# stale pack\n[transforms.beacon_runtime_metadata]"), 1)
	if bytes.Equal(stale, current) {
		t.Fatal("fixture: the rendered config has no Metadata-only transform to make stale")
	}
	if err := os.WriteFile(VectorConfigPath(true), stale, 0o644); err != nil {
		t.Fatal(err)
	}
	return stale
}

func expectedRender(t *testing.T, privacyMode string) string {
	t.Helper()
	enrollment, err := LoadEnrollment(true)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := endpointconfig.Load(true)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderVectorConfig(RenderOptions{
		LogPath:     cfg.LogPath,
		IngestURL:   enrollment.IngestURL,
		SecretsFile: SecretsPath(true),
		DataDir:     DataDir(true),
		PrivacyMode: privacyMode,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rendered
}

func TestReconcileReplacesAStaleTransformAndRestartsTheForwarder(t *testing.T) {
	f := newReconcileFixture(t, "metadata-only")
	writeStaleConfig(t)
	loads, unloads := f.fwd.loads, f.fwd.unloads

	result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: f.fwd})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated || !result.Restarted || result.Warning != "" {
		t.Fatalf("result = %+v, want updated and restarted", result)
	}
	if result.PrivacyMode != managedprivacy.MetadataOnly {
		t.Fatalf("privacy mode = %q, want the recorded metadata_only", result.PrivacyMode)
	}
	got, _ := os.ReadFile(VectorConfigPath(true))
	if string(got) != expectedRender(t, managedprivacy.MetadataOnly) {
		t.Fatal("vector.toml is not the current pack's Metadata-only render")
	}
	if info, err := os.Stat(VectorConfigPath(true)); err != nil || (testenv.HasPOSIXFileModes() && info.Mode().Perm() != 0o644) {
		t.Fatalf("vector.toml mode: %v %v", info, err)
	}
	if f.fwd.loads != loads+1 || f.fwd.unloads != unloads {
		t.Fatalf("forwarder loads %d->%d unloads %d->%d, want exactly one restart", loads, f.fwd.loads, unloads, f.fwd.unloads)
	}
	if strings.Contains(string(got), "bcn_device") {
		t.Fatal("the rendered config carries the device key")
	}
	f.assertCredentialsAndCheckpointsUntouched(t)
	leftovers, _ := filepath.Glob(filepath.Join(Dir(true), ".vector-*"))
	if len(leftovers) != 0 {
		t.Fatalf("reconcile left temporary files: %v", leftovers)
	}
}

func TestReconcileLeavesACurrentConfigAndTheForwarderAlone(t *testing.T) {
	f := newReconcileFixture(t, "standard")
	before, _ := os.ReadFile(VectorConfigPath(true))
	beforeInfo, _ := os.Stat(VectorConfigPath(true))
	loads, unloads := f.fwd.loads, f.fwd.unloads

	result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: f.fwd})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Unchanged || result.Updated || result.Restarted {
		t.Fatalf("result = %+v, want unchanged", result)
	}
	after, _ := os.ReadFile(VectorConfigPath(true))
	afterInfo, _ := os.Stat(VectorConfigPath(true))
	if !bytes.Equal(before, after) || !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Fatal("an unchanged config was rewritten")
	}
	if f.fwd.loads != loads || f.fwd.unloads != unloads {
		t.Fatal("an unchanged config restarted the forwarder")
	}
	f.assertCredentialsAndCheckpointsUntouched(t)
}

// Switching to restricted log access replaces the runtime log with a copy that starts with the same
// line, and Vector keeps tailing the replaced file until it restarts. Install, repair and the
// package upgrade therefore ask for a restart even when the config is current; nothing is
// rewritten.
func TestReconcileRestartsARunningForwarderWithACurrentConfigWhenAsked(t *testing.T) {
	f := newReconcileFixture(t, "standard")
	before, _ := os.ReadFile(VectorConfigPath(true))
	beforeInfo, _ := os.Stat(VectorConfigPath(true))
	loads, unloads := f.fwd.loads, f.fwd.unloads

	result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: f.fwd, Restart: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Unchanged || result.Updated || result.UnitUpdated || !result.Restarted || result.Warning != "" {
		t.Fatalf("result = %+v, want an unchanged config and a restart", result)
	}
	if f.fwd.loads != loads+1 || f.fwd.unloads != unloads {
		t.Fatalf("forwarder loads %d->%d unloads %d->%d, want exactly one restart", loads, f.fwd.loads, unloads, f.fwd.unloads)
	}
	after, _ := os.ReadFile(VectorConfigPath(true))
	afterInfo, _ := os.Stat(VectorConfigPath(true))
	if !bytes.Equal(before, after) || !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Fatal("a requested restart rewrote a current config")
	}
	if !strings.Contains(result.Message(), "restarted") {
		t.Fatalf("message %q does not say the forwarder restarted", result.Message())
	}
	f.assertCredentialsAndCheckpointsUntouched(t)
}

// A requested restart never starts a forwarder an operator stopped.
func TestReconcileRestartLeavesAStoppedForwarderStopped(t *testing.T) {
	f := newReconcileFixture(t, "standard")
	f.fwd.stopped = true
	loads := f.fwd.loads

	result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: f.fwd, Restart: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Unchanged || result.Restarted {
		t.Fatalf("result = %+v, want unchanged without a restart", result)
	}
	if f.fwd.loads != loads {
		t.Fatal("a requested restart started a forwarder an operator stopped")
	}
}

// A restart that fails is reported: on launchd the bootout has already run, so the forwarder may
// be stopped.
func TestReconcileReportsAFailedRequestedRestart(t *testing.T) {
	f := newReconcileFixture(t, "standard")
	f.fwd.loadErr = errors.New("bootstrap failed")

	result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: f.fwd, Restart: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Restarted || !strings.Contains(result.Warning, "bootstrap failed") || !strings.Contains(result.Warning, "beacon endpoint status") {
		t.Fatalf("result = %+v, want a warning naming the failed restart", result)
	}
}

// A requested restart never takes down a forwarder that is running fine. The kept config after a
// refused render, and a current config the forwarder's own Vector now rejects, would both leave
// nothing running after a restart, so neither restarts.
func TestReconcileRequestedRestartNeverStopsAWorkingForwarder(t *testing.T) {
	for _, stale := range []bool{true, false} {
		f := newReconcileFixture(t, "metadata-only")
		if stale {
			writeStaleConfig(t)
		}
		before, _ := os.ReadFile(VectorConfigPath(true))
		enrollment, err := LoadEnrollment(true)
		if err != nil {
			t.Fatal(err)
		}
		enrollment.VectorBin = fakeVector(t, "0.56.0", 78)
		if err := SaveEnrollment(true, *enrollment); err != nil {
			t.Fatal(err)
		}
		loads := f.fwd.loads

		result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: f.fwd, Restart: true})
		if err != nil {
			t.Fatal(err)
		}
		if result.Updated || result.Restarted || !strings.Contains(result.Warning, "reject") {
			t.Fatalf("stale=%v: result = %+v, want a warning and no restart", stale, result)
		}
		if f.fwd.loads != loads {
			t.Fatalf("stale=%v: a requested restart loaded a config the forwarder's Vector rejects", stale)
		}
		if got, _ := os.ReadFile(VectorConfigPath(true)); !bytes.Equal(got, before) {
			t.Fatalf("stale=%v: the config changed", stale)
		}
	}
}

func TestReconcileKeepsTheOldConfigWhenVectorRejectsTheNewOne(t *testing.T) {
	f := newReconcileFixture(t, "metadata-only")
	stale := writeStaleConfig(t)
	// The forwarder's own Vector now refuses every config.
	enrollment, err := LoadEnrollment(true)
	if err != nil {
		t.Fatal(err)
	}
	enrollment.VectorBin = fakeVector(t, "0.56.0", 78)
	if err := SaveEnrollment(true, *enrollment); err != nil {
		t.Fatal(err)
	}
	loads := f.fwd.loads

	result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: f.fwd})
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated || result.Restarted || !strings.Contains(result.Warning, "Vector rejected") {
		t.Fatalf("result = %+v, want a kept config with a warning", result)
	}
	got, _ := os.ReadFile(VectorConfigPath(true))
	if !bytes.Equal(got, stale) {
		t.Fatal("a rejected render replaced the running config")
	}
	if f.fwd.loads != loads {
		t.Fatal("a rejected render restarted the forwarder")
	}
	f.assertCredentialsAndCheckpointsUntouched(t)
	leftovers, _ := filepath.Glob(filepath.Join(Dir(true), ".vector-*"))
	if len(leftovers) != 0 {
		t.Fatalf("reconcile left temporary files: %v", leftovers)
	}
}

func TestReconcileIsANoOpWhenNotConnected(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	isolateVectorDiscovery(t)
	fwd := &fakeForwarder{supported: true}
	result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: fwd})
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped == "" || result.Updated || result.Warning != "" {
		t.Fatalf("result = %+v, want skipped", result)
	}
	if fwd.loads != 0 || fwd.unloads != 0 || len(fwd.written) != 0 {
		t.Fatal("a reconcile on an endpoint that is not connected touched the service manager")
	}
	if _, err := os.Stat(Dir(true)); !os.IsNotExist(err) {
		t.Fatalf("a reconcile on an endpoint that is not connected created %s", Dir(true))
	}
}

func TestReconcileSkipsAnIncompleteConnect(t *testing.T) {
	f := newReconcileFixture(t, "metadata-only")
	stale := writeStaleConfig(t)
	if err := markConnectPending(true, time.Now(), true); err != nil {
		t.Fatal(err)
	}
	result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: f.fwd})
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped == "" || result.Updated {
		t.Fatalf("result = %+v, want skipped", result)
	}
	if got, _ := os.ReadFile(VectorConfigPath(true)); !bytes.Equal(got, stale) {
		t.Fatal("an incomplete connect's config was rewritten")
	}
}

func TestReconcileSkipsWhenTheServiceManagerCannotRunTheForwarder(t *testing.T) {
	f := newReconcileFixture(t, "metadata-only")
	stale := writeStaleConfig(t)
	f.fwd.supported = false
	result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: f.fwd})
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != "fake forwarder unsupported" || result.Updated {
		t.Fatalf("result = %+v, want skipped as unsupported", result)
	}
	if got, _ := os.ReadFile(VectorConfigPath(true)); !bytes.Equal(got, stale) {
		t.Fatal("an unsupported service manager's config was rewritten")
	}
}

func TestReconcileWarnsWhenVectorIsMissing(t *testing.T) {
	f := newReconcileFixture(t, "metadata-only")
	stale := writeStaleConfig(t)
	if err := os.Remove(f.vector); err != nil {
		t.Fatal(err)
	}
	result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: f.fwd})
	if err != nil {
		t.Fatal(err)
	}
	if result.Warning == "" || result.Updated {
		t.Fatalf("result = %+v, want a warning", result)
	}
	if got, _ := os.ReadFile(VectorConfigPath(true)); !bytes.Equal(got, stale) {
		t.Fatal("the config was rewritten without a Vector to validate it")
	}
	f.assertCredentialsAndCheckpointsUntouched(t)
}

func TestReconcileKeepsTheRecordedPrivacyModeAndRefusesConflictingRecords(t *testing.T) {
	f := newReconcileFixture(t, "standard")
	cfg, err := endpointconfig.Load(true)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ManagedIngest.PrivacyMode = managedprivacy.MetadataOnly
	if _, err := endpointconfig.Save(cfg); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(VectorConfigPath(true))
	result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: f.fwd})
	if err != nil {
		t.Fatal(err)
	}
	if result.Warning == "" || result.Updated {
		t.Fatalf("result = %+v, want conflicting privacy records to be refused", result)
	}
	if after, _ := os.ReadFile(VectorConfigPath(true)); !bytes.Equal(before, after) {
		t.Fatal("conflicting privacy records changed the config")
	}
}

func TestReconcileUpdatesAStoppedForwarderWithoutStartingIt(t *testing.T) {
	f := newReconcileFixture(t, "metadata-only")
	writeStaleConfig(t)
	f.fwd.loads = 0 // the fake reports Loaded only after a Load
	result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: f.fwd})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated || result.Restarted {
		t.Fatalf("result = %+v, want updated without a restart", result)
	}
	if f.fwd.loads != 0 {
		t.Fatal("reconcile started a forwarder that was not running")
	}
	f.assertCredentialsAndCheckpointsUntouched(t)
}

// On systemd an operator can stop the forwarder without disabling it; the unit still reports
// Loaded. Reconcile must update the config without starting it again.
func TestReconcileDoesNotStartAnEnabledButStoppedForwarder(t *testing.T) {
	f := newReconcileFixture(t, "metadata-only")
	writeStaleConfig(t)
	f.fwd.stopped = true
	loads := f.fwd.loads
	result, err := ReconcileForwarder(ReconcileOptions{UserMode: true, Forwarder: f.fwd})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated || result.Restarted {
		t.Fatalf("result = %+v, want updated without a restart", result)
	}
	if f.fwd.loads != loads {
		t.Fatal("reconcile started a forwarder an operator stopped")
	}
	f.assertCredentialsAndCheckpointsUntouched(t)
}
