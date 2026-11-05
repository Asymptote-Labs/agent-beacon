package lifecycle

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// writeConnectedState lays down what a finished connect leaves behind, without a service.
func writeConnectedState(t *testing.T) map[string][]byte {
	t.Helper()
	if err := asymptote.SaveEnrollment(true, asymptote.Enrollment{InstallID: "i", IngestURL: "https://ingest.example", DeviceID: "d", OrganizationID: "o", PrivacyMode: "metadata_only"}); err != nil {
		t.Fatal(err)
	}
	if err := asymptote.WriteSecrets(true, "bcn_device_test_key"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(asymptote.VectorConfigPath(true), []byte("# rendered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	checkpoint := filepath.Join(asymptote.DataDir(true), "beacon_runtime", "checkpoints.json")
	if err := os.MkdirAll(filepath.Dir(checkpoint), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checkpoint, []byte(`{"position":42}`), 0o600); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, path := range []string{asymptote.EnrollmentPath(true), asymptote.SecretsPath(true), asymptote.VectorConfigPath(true), checkpoint} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		files[path] = data
	}
	return files
}

// Repair used to tear the Beacon Cloud forwarder down with the rest of the endpoint: the
// uninstall half removed vector.toml and Vector's checkpoints and stopped the service, and the
// install half never put them back, so a repaired endpoint silently stopped forwarding until a
// re-connect rotated its key. The teardown now keeps all of it for Install to reconcile.
func TestRepairTeardownKeepsTheManagedForwarder(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	opts := repairUninstallOptions(InstallOptions{UserMode: true})
	if !opts.KeepManagedIngest {
		t.Fatal("repair's uninstall step must keep the Beacon Cloud forwarder")
	}
	files := writeConnectedState(t)
	if err := Uninstall(opts); err != nil {
		t.Fatal(err)
	}
	for path, want := range files {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(want) {
			t.Fatalf("repair's teardown changed %s: %v", path, err)
		}
	}
	if !asymptote.Connected(true) {
		t.Fatal("the endpoint must still be connected after repair's teardown")
	}
}

func TestUninstallStillDisconnectsTheManagedForwarder(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	writeConnectedState(t)
	if err := Uninstall(UninstallOptions{UserMode: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(asymptote.Dir(true)); !os.IsNotExist(err) {
		t.Fatalf("uninstall left %s behind: %v", asymptote.Dir(true), err)
	}
}

// The macOS package installs with --no-start and runs `beacon endpoint asymptote reconcile`
// itself, so Install must not restart the forwarder when it is asked to start nothing.
func TestInstallWithoutStartingServicesLeavesTheForwarderToTheCaller(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("install preflight is macOS-only")
	}
	home := t.TempDir()
	testenv.SetHome(t, home)
	collectorPath := filepath.Join(home, "bin", "beacon-otelcol")
	if err := os.MkdirAll(filepath.Dir(collectorPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(collectorPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	installFakeInventoryJob(t, true)
	old := reconcileManagedForwarder
	called := false
	reconcileManagedForwarder = func(bool, string) (asymptote.ReconcileResult, error) {
		called = true
		return asymptote.ReconcileResult{}, nil
	}
	t.Cleanup(func() { reconcileManagedForwarder = old })
	result, err := Install(InstallOptions{
		UserMode:      true,
		LogPath:       filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl"),
		Harnesses:     []string{},
		GRPCPort:      freePort(t),
		HTTPPort:      freePort(t),
		CollectorPath: collectorPath,
		StartService:  false,
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if called || result.ManagedForwarder != nil {
		t.Fatal("a --no-start install reconciled the forwarder")
	}
}

// Repair's teardown keeps config.json, so the reinstall carries the managed_ingest block over
// instead of rewriting the file as if the still-forwarding endpoint were not connected.
func TestRepairTeardownKeepsTheManagedIngestRecord(t *testing.T) {
	for _, withManifest := range []bool{false, true} {
		testenv.SetHome(t, t.TempDir())
		writeConnectedState(t)
		cfg := endpointconfig.Default(true, filepath.Join(t.TempDir(), "runtime.jsonl"))
		cfg.ManagedIngest = &endpointconfig.ManagedIngest{Enabled: true, IngestURL: "https://ingest.example", DeviceID: "d", PrivacyMode: "metadata_only"}
		configPath, err := endpointconfig.Save(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if withManifest {
			if _, err := writeManifest(true, Manifest{Files: []string{configPath}}); err != nil {
				t.Fatal(err)
			}
		}
		if err := Uninstall(repairUninstallOptions(InstallOptions{UserMode: true})); err != nil {
			t.Fatal(err)
		}
		kept, err := endpointconfig.Load(true)
		if err != nil {
			t.Fatalf("manifest=%v: repair's teardown removed config.json: %v", withManifest, err)
		}
		if kept.ManagedIngest == nil || kept.ManagedIngest.PrivacyMode != "metadata_only" {
			t.Fatalf("manifest=%v: managed_ingest = %+v, want it kept", withManifest, kept.ManagedIngest)
		}
		if got := buildConfig(InstallOptions{UserMode: true}); got.ManagedIngest == nil {
			t.Fatalf("manifest=%v: the reinstall's config dropped managed_ingest", withManifest)
		}
	}
}

// `uninstall --keep-config` keeps harness settings, not Beacon's own config.json: only repair keeps it.
func TestUninstallKeepConfigStillRemovesBeaconConfig(t *testing.T) {
	for _, withManifest := range []bool{false, true} {
		testenv.SetHome(t, t.TempDir())
		cfg := endpointconfig.Default(true, filepath.Join(t.TempDir(), "runtime.jsonl"))
		configPath, err := endpointconfig.Save(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if withManifest {
			if _, err := writeManifest(true, Manifest{Files: []string{configPath}}); err != nil {
				t.Fatal(err)
			}
		}
		_ = Uninstall(UninstallOptions{UserMode: true, KeepConfig: true})
		if _, err := os.Stat(configPath); !os.IsNotExist(err) {
			t.Fatalf("manifest=%v: uninstall --keep-config left %s behind: %v", withManifest, configPath, err)
		}
	}
}
