package asymptote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
)

type fakeForwarder struct {
	supported bool
	written   []string
	loads     int
	unloads   int
	removed   int
	loadErr   error
}

func (f *fakeForwarder) Supported() bool           { return f.supported }
func (f *fakeForwarder) UnsupportedReason() string { return "fake forwarder unsupported" }
func (f *fakeForwarder) Label() string             { return "fake.forwarder" }
func (f *fakeForwarder) WriteUnit(vectorBin, configPath string) (string, error) {
	f.written = append(f.written, vectorBin+" --config "+configPath)
	return "/tmp/fake.unit", nil
}
func (f *fakeForwarder) Load() error   { f.loads++; return f.loadErr }
func (f *fakeForwarder) Unload() error { f.unloads++; return nil }
func (f *fakeForwarder) RemoveUnits()  { f.removed++ }
func (f *fakeForwarder) Status() service.Status {
	return service.Status{Label: "fake.forwarder", Loaded: f.loads > 0, Running: f.loads > 0}
}

// fakeVector writes an executable that answers --version and validate like Vector 0.56.
func fakeVector(t *testing.T, version string, validateExit int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake vector script needs a POSIX shell")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "vector")
	script := "#!/bin/sh\ncase \"$1\" in\n  --version) echo \"vector " + version + " (aarch64-apple-darwin test)\";;\n  validate) exit " + itoa(validateExit) + ";;\n  *) exit 0;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func itoa(n int) string { return strconv.Itoa(n) }

func connectOptions(t *testing.T, fd *fakeDashboard, fwd *fakeForwarder, vector string) ConnectOptions {
	t.Helper()
	return ConnectOptions{
		UserMode:  true,
		LogPath:   filepath.Join(t.TempDir(), "runtime.jsonl"),
		VectorBin: vector,
		Forwarder: fwd,
		Enroll: EnrollOptions{
			DashboardURL: fd.server.URL,
			Device:       DeviceInfo{Hostname: "mac.local", OS: "darwin", Arch: "arm64", BeaconVersion: "test"},
			OpenBrowser:  fd.approve(t),
			HTTPClient:   fd.server.Client(),
			Timeout:      5 * time.Second,
		},
		Now: func() time.Time { return time.Date(2026, 9, 4, 1, 0, 0, 0, time.UTC) },
	}
}

func TestConnectWritesSecretsConfigUnitAndEnrollmentInOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	isolateVectorDiscovery(t)
	fd := newFakeDashboard(t)
	fwd := &fakeForwarder{supported: true}
	vector := fakeVector(t, "0.56.0", 0)

	result, err := Connect(context.Background(), connectOptions(t, fd, fwd, vector))
	if err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}

	dir := Dir(true)
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("asymptote dir mode: %v %v", info, err)
	}
	for name, mode := range map[string]os.FileMode{SecretsFileName: 0o600, EnrollmentFileName: 0o600, VectorConfigName: 0o644} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Mode().Perm() != mode {
			t.Fatalf("%s mode = %o, want %o", name, info.Mode().Perm(), mode)
		}
	}
	if info, err := os.Stat(DataDir(true)); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("data dir: %v %v", info, err)
	}

	key, err := ReadDeviceKey(true)
	if err != nil || !strings.HasPrefix(key, "bcn_device_abcdefgh_") {
		t.Fatalf("stored key = %q (%v)", key, err)
	}
	toml, _ := os.ReadFile(VectorConfigPath(true))
	if strings.Contains(string(toml), "bcn_device") || strings.Contains(string(toml), "${") {
		t.Fatalf("vector.toml must hold neither the key nor env references:\n%s", toml)
	}
	for _, want := range []string{
		`uri = "https://ingest.example.test/v1/ingest/runtime"`,
		`path = "` + SecretsPath(true) + `"`,
		`data_dir = "` + DataDir(true) + `"`,
	} {
		if !strings.Contains(string(toml), want) {
			t.Fatalf("vector.toml missing %q:\n%s", want, toml)
		}
	}
	if len(fwd.written) != 1 || !strings.HasPrefix(fwd.written[0], vector+" --config "+VectorConfigPath(true)) || fwd.loads != 1 {
		t.Fatalf("forwarder calls = %v loads=%d", fwd.written, fwd.loads)
	}

	enrollment, err := LoadEnrollment(true)
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.DeviceID != "dev-1" || enrollment.OrganizationName != "Asymptote Test" || enrollment.InstallID == "" || enrollment.VectorVersion != "0.56.0" {
		t.Fatalf("enrollment = %+v", enrollment)
	}
	if result.ReEnrolled || result.Enrollment.DeviceID != "dev-1" || result.SecretsFile != SecretsPath(true) {
		t.Fatalf("result = %+v", result)
	}
	cfg, err := endpointconfig.Load(true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ManagedIngest == nil || !cfg.ManagedIngest.Enabled || cfg.ManagedIngest.DeviceID != "dev-1" || cfg.ManagedIngest.IngestURL != "https://ingest.example.test" {
		t.Fatalf("config managed_ingest = %+v", cfg.ManagedIngest)
	}
	raw, _ := os.ReadFile(endpointconfig.ConfigPath(true))
	if strings.Contains(string(raw), "bcn_device") {
		t.Fatalf("config.json must never contain the device key: %s", raw)
	}
	if endpointconfig.HasSecretDestinations(cfg) {
		t.Fatal("managed ingest must not count as a secret destination in config.json")
	}
}

func TestConnectReusesInstallIDOnReEnrollment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	isolateVectorDiscovery(t)
	fd := newFakeDashboard(t)
	fwd := &fakeForwarder{supported: true}
	vector := fakeVector(t, "0.57.1", 0)

	if _, err := Connect(context.Background(), connectOptions(t, fd, fwd, vector)); err != nil {
		t.Fatal(err)
	}
	first, _ := LoadEnrollment(true)
	result, err := Connect(context.Background(), connectOptions(t, fd, fwd, vector))
	if err != nil {
		t.Fatal(err)
	}
	if !result.ReEnrolled || result.Enrollment.InstallID != first.InstallID {
		t.Fatalf("re-enrollment should reuse install id %s: %+v", first.InstallID, result)
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if fd.init["device"].(map[string]any)["install_id"] != first.InstallID {
		t.Fatalf("second init sent install_id %v", fd.init["device"])
	}
	if fwd.loads != 2 {
		t.Fatalf("forwarder should be (re)loaded on each connect, loads=%d", fwd.loads)
	}
}

func TestConnectStopsBeforeTheBrowserWhenVectorIsMissingOrOld(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	isolateVectorDiscovery(t)
	fd := newFakeDashboard(t)
	fwd := &fakeForwarder{supported: true}
	opened := false
	opts := connectOptions(t, fd, fwd, filepath.Join(t.TempDir(), "missing-vector"))
	opts.Enroll.OpenBrowser = func(string) error { opened = true; return nil }

	if _, err := Connect(context.Background(), opts); err == nil || (!errors.Is(err, ErrVectorNotFound) && !strings.Contains(err.Error(), "vector")) {
		t.Fatalf("expected a Vector error, got %v", err)
	}
	opts = connectOptions(t, fd, fwd, fakeVector(t, "0.55.9", 0))
	opts.Enroll.OpenBrowser = func(string) error { opened = true; return nil }
	if _, err := Connect(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "0.56.0 or newer") {
		t.Fatalf("expected a too-old error, got %v", err)
	}
	if opened {
		t.Fatal("browser must not open when Vector is unusable")
	}
	if _, err := os.Stat(SecretsPath(true)); !os.IsNotExist(err) {
		t.Fatal("no secrets file may exist after a failed connect")
	}
}

func TestConnectRefusesUnsupportedServiceManagerBeforeEnrolling(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fd := newFakeDashboard(t)
	opened := false
	opts := connectOptions(t, fd, &fakeForwarder{supported: false}, fakeVector(t, "0.56.0", 0))
	opts.Enroll.OpenBrowser = func(string) error { opened = true; return nil }
	if _, err := Connect(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "fake forwarder unsupported") {
		t.Fatalf("expected unsupported error, got %v", err)
	}
	if opened {
		t.Fatal("browser must not open when no service manager is available")
	}
}

func TestConnectFailsWhenVectorValidateRejectsTheConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fd := newFakeDashboard(t)
	fwd := &fakeForwarder{supported: true}
	if _, err := Connect(context.Background(), connectOptions(t, fd, fwd, fakeVector(t, "0.56.0", 78))); err == nil || !strings.Contains(err.Error(), "vector validate failed") {
		t.Fatalf("expected validate failure, got %v", err)
	}
	if len(fwd.written) != 0 || fwd.loads != 0 {
		t.Fatal("no unit may be written or loaded when validation fails")
	}
	if _, err := LoadEnrollment(true); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("enrollment must not be recorded after a failed connect, got %v", err)
	}
}

func TestDisconnectRemovesForwarderAndOptionallyKeepsCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fd := newFakeDashboard(t)
	fwd := &fakeForwarder{supported: true}
	if _, err := Connect(context.Background(), connectOptions(t, fd, fwd, fakeVector(t, "0.56.0", 0))); err != nil {
		t.Fatal(err)
	}

	if err := Disconnect(DisconnectOptions{UserMode: true, Forwarder: fwd, KeepCredentials: true}); err != nil {
		t.Fatal(err)
	}
	if fwd.unloads != 1 || fwd.removed != 1 {
		t.Fatalf("forwarder unloads=%d removed=%d", fwd.unloads, fwd.removed)
	}
	if _, err := os.Stat(VectorConfigPath(true)); !os.IsNotExist(err) {
		t.Fatal("vector.toml should be removed")
	}
	if _, err := os.Stat(DataDir(true)); !os.IsNotExist(err) {
		t.Fatal("data dir should be removed")
	}
	if _, err := LoadEnrollment(true); err != nil {
		t.Fatalf("enrollment should be kept: %v", err)
	}
	if _, err := ReadDeviceKey(true); err != nil {
		t.Fatalf("device key should be kept: %v", err)
	}
	cfg, _ := endpointconfig.Load(true)
	if cfg.ManagedIngest != nil {
		t.Fatalf("config managed_ingest should be cleared, got %+v", cfg.ManagedIngest)
	}

	if err := Disconnect(DisconnectOptions{UserMode: true, Forwarder: fwd}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Dir(true)); !os.IsNotExist(err) {
		t.Fatal("asymptote dir should be removed entirely")
	}
	// Disconnecting an endpoint that was never connected is not an error.
	if err := Disconnect(DisconnectOptions{UserMode: true, Forwarder: fwd}); err != nil {
		t.Fatalf("second disconnect should be a no-op, got %v", err)
	}
}

func TestStatusReportsCredentialState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := Status(true, StatusOptions{}); got.Enabled || got.Message != "" {
		t.Fatalf("not enrolled status = %+v", got)
	}

	var answer int
	ingest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != CredentialCheckPath || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer bcn_device_") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(answer)
	}))
	defer ingest.Close()
	if err := SaveEnrollment(true, Enrollment{InstallID: "i", IngestURL: ingest.URL, DeviceID: "dev-1", KeyPrefix: "bcn_device_abcdefgh", OrganizationName: "Asymptote Test", EnrolledAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := WriteSecrets(true, "bcn_device_abcdefgh_"+strings.Repeat("k", 43)); err != nil {
		t.Fatal(err)
	}
	for status, want := range map[int]string{http.StatusOK: "valid", http.StatusNoContent: "valid", http.StatusUnauthorized: "revoked", http.StatusForbidden: "revoked", http.StatusBadGateway: "unknown"} {
		answer = status
		got := Status(true, StatusOptions{HTTPClient: ingest.Client()})
		if !got.Enabled || got.Credential != want || got.DeviceID != "dev-1" {
			t.Fatalf("status for %d = %+v, want credential %q", status, got, want)
		}
	}
	ingest.Close()
	got := Status(true, StatusOptions{HTTPClient: &http.Client{Timeout: 500 * time.Millisecond}})
	if got.Credential != "unknown" || !strings.Contains(got.CredentialMessage, "unreachable") {
		t.Fatalf("offline status = %+v", got)
	}
	if skipped := Status(true, StatusOptions{SkipCredentialCheck: true}); skipped.Credential != "" || !skipped.Enabled {
		t.Fatalf("skip should not touch the network: %+v", skipped)
	}
	var encoded map[string]any
	if err := json.Unmarshal(mustJSON(t, got), &encoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := encoded["device_key"]; ok {
		t.Fatal("status JSON must never include the device key")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
