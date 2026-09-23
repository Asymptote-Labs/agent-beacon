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
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

type fakeForwarder struct {
	supported bool
	unitPath  string
	written   []string
	loads     int
	unloads   int
	removed   int
	loadErr   error
	writeErr  error
}

func (f *fakeForwarder) Supported() bool           { return f.supported }
func (f *fakeForwarder) UnsupportedReason() string { return "fake forwarder unsupported" }
func (f *fakeForwarder) Label() string             { return "fake.forwarder" }
func (f *fakeForwarder) UnitPath() (string, error) { return f.unitPath, nil }
func (f *fakeForwarder) WriteUnit(vectorBin, configPath string) (string, error) {
	f.written = append(f.written, vectorBin+" --config "+configPath)
	if f.writeErr != nil {
		return "", f.writeErr
	}
	if f.unitPath != "" {
		if err := os.WriteFile(f.unitPath, []byte("unit"), 0o644); err != nil {
			return "", err
		}
	}
	return f.unitPath, nil
}
func (f *fakeForwarder) Load() error   { f.loads++; return f.loadErr }
func (f *fakeForwarder) Unload() error { f.unloads++; return nil }
func (f *fakeForwarder) RemoveUnits() {
	f.removed++
	if f.unitPath != "" {
		_ = os.Remove(f.unitPath)
	}
}
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
	testenv.SetHome(t, home)
	isolateVectorDiscovery(t)
	fd := newFakeDashboard(t)
	fwd := &fakeForwarder{supported: true}
	vector := fakeVector(t, "0.56.0", 0)
	opts := connectOptions(t, fd, fwd, vector)
	opts.PrivacyMode = "metadata-only"

	result, err := Connect(context.Background(), opts)
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
		`inputs = ["beacon_runtime_metadata"]`,
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
	if enrollment.DeviceID != "dev-1" || enrollment.OrganizationName != "Asymptote Test" || enrollment.InstallID == "" || enrollment.VectorVersion != "0.56.0" || enrollment.PrivacyMode != "metadata_only" {
		t.Fatalf("enrollment = %+v", enrollment)
	}
	if result.ReEnrolled || result.Enrollment.DeviceID != "dev-1" || result.SecretsFile != SecretsPath(true) {
		t.Fatalf("result = %+v", result)
	}
	cfg, err := endpointconfig.Load(true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ManagedIngest == nil || !cfg.ManagedIngest.Enabled || cfg.ManagedIngest.DeviceID != "dev-1" || cfg.ManagedIngest.IngestURL != "https://ingest.example.test" || cfg.ManagedIngest.PrivacyMode != "metadata_only" {
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
	testenv.SetHome(t, home)
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

func TestConnectUsesAccountEnrollmentWithoutOpeningBrowser(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	isolateVectorDiscovery(t)
	var authorization string
	accountServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_id":         "dev-account",
			"device_key":        "bcn_device_abcdefgh_" + strings.Repeat("k", 43),
			"key_prefix":        "bcn_device_abcdefgh",
			"ingest_url":        "https://ingest.example.test",
			"organization_id":   "org-1",
			"organization_name": "Beacon Test",
			"email":             "person@example.com",
			"scopes":            []string{"ingest:write"},
		})
	}))
	defer accountServer.Close()

	fd := newFakeDashboard(t)
	opened := false
	opts := connectOptions(t, fd, &fakeForwarder{supported: true}, fakeVector(t, "0.56.0", 0))
	opts.Enroll.OpenBrowser = func(string) error { opened = true; return nil }
	opts.AccountEnroll = &AccountEnrollOptions{
		BaseURL:     accountServer.URL,
		AccessToken: "bcn_cli_account_token",
		HTTPClient:  accountServer.Client(),
	}
	result, err := Connect(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if opened {
		t.Fatal("account enrollment opened the browser")
	}
	if authorization != "Bearer bcn_cli_account_token" || result.Enrollment.DeviceID != "dev-account" {
		t.Fatalf("authorization=%q result=%#v", authorization, result.Enrollment)
	}
	if result.Enrollment.DashboardURL != accountServer.URL {
		t.Fatalf("dashboard URL = %q", result.Enrollment.DashboardURL)
	}
}

func TestConnectStopsBeforeTheBrowserWhenVectorIsMissingOrOld(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	isolateVectorDiscovery(t)
	fd := newFakeDashboard(t)
	fwd := &fakeForwarder{supported: true}
	opened := false
	opts := connectOptions(t, fd, fwd, filepath.Join(t.TempDir(), "missing-vector"))
	opts.Enroll.OpenBrowser = func(string) error { opened = true; return nil }

	if _, err := Connect(context.Background(), opts); err == nil || (!errors.Is(err, ErrVectorNotFound) && !strings.Contains(err.Error(), "vector")) {
		t.Fatalf("expected a Vector error, got %v", err)
	}
	opts = connectOptions(t, fd, fwd, fakeVector(t, "0.44.9", 0))
	opts.Enroll.OpenBrowser = func(string) error { opened = true; return nil }
	if _, err := Connect(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "0.50.0 or newer") {
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
	testenv.SetHome(t, home)
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
	testenv.SetHome(t, home)
	fd := newFakeDashboard(t)
	fwd := &fakeForwarder{supported: true}
	opened := false
	opts := connectOptions(t, fd, fwd, fakeVector(t, "0.56.0", 78))
	opts.Enroll.OpenBrowser = func(string) error { opened = true; return nil }
	if _, err := Connect(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "vector validate failed") {
		t.Fatalf("expected validate failure, got %v", err)
	}
	// The config is validated before enrollment, so a Vector that rejects it never opens
	// the browser and no key is minted or stored.
	if opened {
		t.Fatal("browser must not open when Vector rejects the forwarder config")
	}
	if _, err := os.Stat(SecretsPath(true)); !os.IsNotExist(err) {
		t.Fatal("no secrets file may exist when validation fails before enrollment")
	}
	if entries, _ := os.ReadDir(Dir(true)); len(entries) != 0 {
		t.Fatalf("preflight must clean up after itself, found %v", entries)
	}
	if len(fwd.written) != 0 || fwd.loads != 0 {
		t.Fatal("no unit may be written or loaded when validation fails")
	}
	if _, err := LoadEnrollment(true); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("enrollment must not be recorded after a failed connect, got %v", err)
	}
	// Pre-validation uses a temp file, so the rendered config is not left behind and the
	// machine is not connected. Disconnect can still clean up the secrets directory.
	if Connected(true) {
		t.Fatal("a connect that failed at validate must not count as connected")
	}
	if err := Disconnect(DisconnectOptions{UserMode: true, Forwarder: fwd}); err != nil {
		t.Fatal(err)
	}
}

func TestConnectedNeedsEnrollmentAndForwarderConfig(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	if Connected(true) {
		t.Fatal("fresh machine")
	}
	if err := SaveEnrollment(true, Enrollment{InstallID: "i", DeviceID: "d", OrganizationID: "o"}); err != nil {
		t.Fatal(err)
	}
	if Connected(true) {
		t.Fatal("an enrollment record alone (disconnect --keep-credentials) is not a connection")
	}
	if err := os.WriteFile(VectorConfigPath(true), []byte("# rendered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !Connected(true) {
		t.Fatal("record plus forwarder config is connected")
	}
	if err := os.Remove(EnrollmentPath(true)); err != nil {
		t.Fatal(err)
	}
	if Connected(true) {
		t.Fatal("a forwarder config alone (connect failed before recording) is not a connection")
	}
}

func TestDisconnectRemovesForwarderAndOptionallyKeepsCredentials(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
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
	// Disconnecting an endpoint that was never connected is not an error and must not
	// touch the service manager: uninstall runs this on every endpoint.
	before := fwd.unloads
	if err := Disconnect(DisconnectOptions{UserMode: true, Forwarder: fwd}); err != nil {
		t.Fatalf("second disconnect should be a no-op, got %v", err)
	}
	if fwd.unloads != before {
		t.Fatal("disconnect without managed state must not unload the forwarder")
	}
}

func TestStatusReportsCredentialState(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
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
	// Credentials alone (what disconnect --keep-credentials leaves) are not a connection.
	kept := Status(true, StatusOptions{SkipCredentialCheck: true})
	if kept.Enabled || kept.DeviceID != "dev-1" || !strings.Contains(kept.Message, "credentials for device dev-1 kept") {
		t.Fatalf("credentials-only status = %+v", kept)
	}
	if err := os.WriteFile(VectorConfigPath(true), []byte("# rendered"), 0o644); err != nil {
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

func TestConnectPinsInstallIDBeforeEnrollmentSoRetriesReuseIt(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	isolateVectorDiscovery(t)
	fd := newFakeDashboard(t)
	fwd := &fakeForwarder{supported: true}

	// First attempt fails after approval (the service manager cannot start the forwarder).
	fwd.loadErr = errors.New("bootstrap failed")
	if _, err := Connect(context.Background(), connectOptions(t, fd, fwd, fakeVector(t, "0.56.0", 0))); err == nil || !strings.Contains(err.Error(), "could not be started") {
		t.Fatalf("expected a load failure, got %v", err)
	}
	fwd.loadErr = nil
	fd.mu.Lock()
	firstID := fd.init["device"].(map[string]any)["install_id"].(string)
	fd.mu.Unlock()
	if pinned := ReadInstallID(true); pinned == "" || pinned != firstID {
		t.Fatalf("install id must be pinned before enrollment: file=%q sent=%q", pinned, firstID)
	}
	if _, err := LoadEnrollment(true); !errors.Is(err, ErrNotEnrolled) {
		t.Fatal("no enrollment record may exist after a failed connect")
	}

	// The retry sends the same id, so the server rotates the device instead of adding one.
	if _, err := Connect(context.Background(), connectOptions(t, fd, fwd, fakeVector(t, "0.56.0", 0))); err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if got := fd.init["device"].(map[string]any)["install_id"]; got != firstID {
		t.Fatalf("retry sent install_id %v, want %q", got, firstID)
	}
}

func TestConnectResultJSONUsesSnakeCaseAndNormalizedDashboardURL(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	isolateVectorDiscovery(t)
	fd := newFakeDashboard(t)
	opts := connectOptions(t, fd, &fakeForwarder{supported: true}, fakeVector(t, "0.56.0", 0))
	opts.Enroll.DashboardURL = fd.server.URL + "/"
	result, err := Connect(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Enrollment.DashboardURL != fd.server.URL {
		t.Fatalf("dashboard URL must be normalized without a trailing slash: %q", result.Enrollment.DashboardURL)
	}
	var encoded map[string]any
	if err := json.Unmarshal(mustJSON(t, result), &encoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := encoded["forwarder_state"]; !ok {
		t.Fatalf("connect --json must expose forwarder_state: %v", encoded)
	}
	if _, ok := encoded["ForwarderState"]; ok {
		t.Fatal("PascalCase field leaked into JSON")
	}
}

func TestLoopbackIngestURLIsAcceptedEndToEnd(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	isolateVectorDiscovery(t)
	fd := newFakeDashboard(t)
	fd.ingestURL = "http://127.0.0.1:9999"
	result, err := Connect(context.Background(), connectOptions(t, fd, &fakeForwarder{supported: true}, fakeVector(t, "0.56.0", 0)))
	if err != nil {
		t.Fatalf("a loopback development ingest URL must work end to end: %v", err)
	}
	if result.Enrollment.IngestURL != "http://127.0.0.1:9999" {
		t.Fatalf("ingest url = %q", result.Enrollment.IngestURL)
	}
	for _, c := range []struct {
		url  string
		want bool
	}{
		{"https://ingest.example.test", true},
		{"http://127.0.0.1:8080", true},
		{"http://localhost", true},
		{"http://ingest.example.test", false},
		{"ftp://127.0.0.1", false},
		{"", false},
	} {
		if got := IsSecureURL(c.url); got != c.want {
			t.Errorf("IsSecureURL(%q) = %t, want %t", c.url, got, c.want)
		}
	}
}

func TestConnectClearsBufferOnPrivacyModeChange(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	isolateVectorDiscovery(t)
	fd := newFakeDashboard(t)
	fwd := &fakeForwarder{supported: true}
	vector := fakeVector(t, "0.56.0", 0)

	opts := connectOptions(t, fd, fwd, vector)
	if _, err := Connect(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(DataDir(true), "buffered-event")
	if err := os.WriteFile(sentinel, []byte("old-standard-data"), 0o600); err != nil {
		t.Fatal(err)
	}

	opts2 := connectOptions(t, fd, fwd, vector)
	opts2.PrivacyMode = "metadata-only"
	if _, err := Connect(context.Background(), opts2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("switching to metadata-only must clear the buffer directory")
	}
	if _, err := os.Stat(DataDir(true)); os.IsNotExist(err) {
		t.Fatal("data directory must be recreated after clearing")
	}
}

func TestConnectRejectedConfigOnPrivacyChangeRotatesNoKeyAndKeepsForwarderRunning(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	isolateVectorDiscovery(t)
	fd := newFakeDashboard(t)
	fwd := &fakeForwarder{supported: true}

	// First connect succeeds with the default (standard) privacy mode.
	opts := connectOptions(t, fd, fwd, fakeVector(t, "0.56.0", 0))
	if _, err := Connect(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if fwd.loads != 1 || fwd.unloads != 0 {
		t.Fatalf("after first connect: loads=%d unloads=%d", fwd.loads, fwd.unloads)
	}
	before, err := LoadEnrollment(true)
	if err != nil {
		t.Fatal(err)
	}
	secretsBefore, err := os.ReadFile(SecretsPath(true))
	if err != nil {
		t.Fatal(err)
	}
	configBefore, err := os.ReadFile(VectorConfigPath(true))
	if err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	fd.init, fd.exchange = nil, nil
	fd.mu.Unlock()

	// Second connect changes privacy mode but vector validate fails (exit 78). Enrollment
	// would rotate the server-side key, so it must not be reached: the running forwarder
	// keeps its key and config and nothing on disk changes.
	opened := false
	opts2 := connectOptions(t, fd, fwd, fakeVector(t, "0.56.0", 78))
	opts2.PrivacyMode = "metadata-only"
	opts2.Enroll.OpenBrowser = func(string) error { opened = true; return nil }
	if _, err := Connect(context.Background(), opts2); err == nil || !strings.Contains(err.Error(), "vector validate failed") {
		t.Fatalf("expected validate failure, got %v", err)
	}
	fd.mu.Lock()
	reached := fd.init != nil || fd.exchange != nil
	fd.mu.Unlock()
	if opened || reached {
		t.Fatalf("a rejected config must fail before enrollment: opened=%t reached dashboard=%t", opened, reached)
	}
	if fwd.unloads != 0 || fwd.loads != 1 {
		t.Fatalf("a rejected config must leave the running forwarder alone: loads=%d unloads=%d", fwd.loads, fwd.unloads)
	}
	secretsAfter, _ := os.ReadFile(SecretsPath(true))
	configAfter, _ := os.ReadFile(VectorConfigPath(true))
	after, _ := LoadEnrollment(true)
	if string(secretsAfter) != string(secretsBefore) || string(configAfter) != string(configBefore) || after == nil || *after != *before {
		t.Fatal("secrets, config and enrollment must be untouched by a rejected reconnect")
	}
	if after.PrivacyMode != "standard" {
		t.Fatalf("enrollment privacy mode = %q, want the original standard", after.PrivacyMode)
	}
}

func TestConnectPreservesBufferWhenPrivacyModeUnchanged(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	isolateVectorDiscovery(t)
	fd := newFakeDashboard(t)
	fwd := &fakeForwarder{supported: true}
	vector := fakeVector(t, "0.56.0", 0)

	opts := connectOptions(t, fd, fwd, vector)
	if _, err := Connect(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(DataDir(true), "buffered-event")
	if err := os.WriteFile(sentinel, []byte("in-flight-data"), 0o600); err != nil {
		t.Fatal(err)
	}

	opts2 := connectOptions(t, fd, fwd, vector)
	if _, err := Connect(context.Background(), opts2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatal("re-connect with the same privacy mode must not clear the buffer")
	}
}

func TestDisconnectIgnoresALeftoverInstallIDFromACancelledConnect(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	// A connect that was cancelled after pinning the id leaves only asymptote/install-id.
	if err := WriteInstallID(true, "abc123"); err != nil {
		t.Fatal(err)
	}
	fwd := &fakeForwarder{supported: true}
	if err := Disconnect(DisconnectOptions{UserMode: true, Forwarder: fwd}); err != nil {
		t.Fatal(err)
	}
	if fwd.unloads != 0 || fwd.removed != 0 {
		t.Fatalf("a pinned install id alone must not touch the service manager: unloads=%d removed=%d", fwd.unloads, fwd.removed)
	}
	if _, err := os.Stat(Dir(true)); !os.IsNotExist(err) {
		t.Fatal("the leftover directory should still be cleaned up")
	}

	// A unit file on disk, with nothing else, is still a forwarder to stop.
	fwd = &fakeForwarder{supported: true, unitPath: filepath.Join(t.TempDir(), "forwarder.plist")}
	if err := os.WriteFile(fwd.unitPath, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Disconnect(DisconnectOptions{UserMode: true, Forwarder: fwd}); err != nil {
		t.Fatal(err)
	}
	if fwd.unloads != 1 {
		t.Fatalf("an installed unit must be unloaded, unloads=%d", fwd.unloads)
	}
}

// TestConnectWithRealVectorOnAFreshMachine runs the whole connect sequence against a real
// Vector binary, including the mode switch, so the validate steps are exercised with Vector's
// actual rules (it refuses a data_dir that does not exist yet) rather than the fake's.
func TestConnectWithRealVectorOnAFreshMachine(t *testing.T) {
	vector := os.Getenv("BEACON_TEST_VECTOR_BIN")
	if vector == "" {
		t.Skip("set BEACON_TEST_VECTOR_BIN to run connect against a real Vector")
	}
	testenv.SetHome(t, t.TempDir())
	isolateVectorDiscovery(t)
	fd := newFakeDashboard(t)
	fwd := &fakeForwarder{supported: true}

	opts := connectOptions(t, fd, fwd, vector)
	opts.PrivacyMode = "standard"
	if _, err := Connect(context.Background(), opts); err != nil {
		t.Fatalf("first connect on a fresh machine: %v", err)
	}
	opts = connectOptions(t, fd, fwd, vector)
	opts.PrivacyMode = "metadata-only"
	if _, err := Connect(context.Background(), opts); err != nil {
		t.Fatalf("switch to metadata-only: %v", err)
	}
	if fwd.loads != 2 || fwd.unloads != 1 {
		t.Fatalf("loads=%d unloads=%d", fwd.loads, fwd.unloads)
	}
}
