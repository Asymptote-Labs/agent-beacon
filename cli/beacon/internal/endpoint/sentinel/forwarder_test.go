package sentinel

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/siempack"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

type fakeService struct {
	spec        service.ForwarderSpec
	unsupported string
	loadErr     error
	unloadErr   error
	units       []string
	loads       int
	unloads     int
	removed     int
	running     bool
	current     bool
}

func (f *fakeService) Supported() bool           { return f.unsupported == "" }
func (f *fakeService) UnsupportedReason() string { return f.unsupported }
func (f *fakeService) WriteUnit(vectorBin, configPath string) (string, error) {
	f.units = append(f.units, vectorBin+" "+configPath)
	f.current = true
	return "unit:" + f.spec.Label, nil
}
func (f *fakeService) UnitCurrent(string, string) (bool, error) { return f.current, nil }
func (f *fakeService) Load() error {
	f.loads++
	if f.loadErr != nil {
		return f.loadErr
	}
	f.running = true
	return nil
}
func (f *fakeService) Unload() error {
	f.unloads++
	if f.unloadErr != nil {
		return f.unloadErr
	}
	f.running = false
	return nil
}
func (f *fakeService) RemoveUnits() { f.removed++ }
func (f *fakeService) Status() service.Status {
	return service.Status{Label: f.spec.Label, Loaded: f.running, Running: f.running}
}

type forwarderFixture struct {
	dir       string
	svc       *fakeService
	validated []string
	reject    error
}

func newForwarderFixture(t *testing.T) *forwarderFixture {
	t.Helper()
	f := &forwarderFixture{dir: filepath.Join(t.TempDir(), "forwarders", "sentinel"), svc: &fakeService{}}
	oldService, oldFind, oldValidate, oldDir, oldNow := newForwarderService, findVector, validateVector, ForwarderDir, now
	newForwarderService = func(spec service.ForwarderSpec) forwarderService { f.svc.spec = spec; return f.svc }
	findVector = func(string) (asymptote.VectorInfo, error) {
		return asymptote.VectorInfo{Path: "/opt/beacon/bin/vector", Version: "0.56.0"}, nil
	}
	validateVector = func(_, config string) error {
		f.validated = append(f.validated, config)
		return f.reject
	}
	ForwarderDir = func() string { return f.dir }
	now = func() time.Time { return time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() {
		newForwarderService, findVector, validateVector, ForwarderDir, now = oldService, oldFind, oldValidate, oldDir, oldNow
	})
	return f
}

func validInstall() InstallOptions {
	return InstallOptions{
		Endpoint:       "https://beacon-dcr-abcd.eastus-1.ingest.monitor.azure.com/",
		DCRImmutableID: "dcr-0123456789abcdef0123456789abcdef",
		TenantID:       "11111111-2222-3333-4444-555555555555",
		ClientID:       "66666666-7777-8888-9999-000000000000",
		ClientSecret:   "the-client-secret",
		LogPath:        "/var/log/beacon-agent/runtime.jsonl",
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestForwarderInstallWritesStateValidatesThenRunsTheService(t *testing.T) {
	f := newForwarderFixture(t)
	result, err := Install(validInstall())
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	config := readFile(t, filepath.Join(f.dir, configFileName))
	for _, want := range []string{
		`endpoint = "https://beacon-dcr-abcd.eastus-1.ingest.monitor.azure.com"`,
		`dcr_immutable_id = "dcr-0123456789abcdef0123456789abcdef"`,
		`azure_tenant_id = "11111111-2222-3333-4444-555555555555"`,
		"azure_client_secret = '''SECRET[beacon_sentinel.client_secret]'''",
		`[secret.beacon_sentinel]`,
		`read_from = "beginning"`,
		`include = ["/var/log/beacon-agent/runtime.jsonl", "/var/log/beacon-agent/runtime.jsonl.1"]`,
		`data_dir = "` + siempack.TOMLEscape(filepath.Join(f.dir, dataDirName)) + `"`,
		`type = "internal_logs"`,
	} {
		if !strings.Contains(config, want) {
			t.Errorf("vector.toml is missing %q", want)
		}
	}
	if strings.Contains(config, "the-client-secret") || strings.Contains(config, "${") {
		t.Fatalf("vector.toml must hold no secret and no environment reference:\n%s", config)
	}
	settings := readFile(t, filepath.Join(f.dir, settingsFileName))
	if strings.Contains(settings, "the-client-secret") {
		t.Fatal("forwarder.json must not hold the secret")
	}
	var secrets map[string]string
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(f.dir, secretsFileName))), &secrets); err != nil || secrets["client_secret"] != "the-client-secret" {
		t.Fatalf("secrets.json = %v (%v)", secrets, err)
	}
	if testenv.HasPOSIXFileModes() {
		for _, path := range []string{f.dir, filepath.Join(f.dir, secretsFileName), filepath.Join(f.dir, configFileName)} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm()&0o077 != 0 {
				t.Errorf("%s has mode %v; only its owner may read it", path, info.Mode().Perm())
			}
		}
	}
	// Vector validated a scratch copy before anything real was written.
	if len(f.validated) != 1 || !strings.Contains(f.validated[0], ".preflight-") {
		t.Fatalf("validated %v, want one preflight config", f.validated)
	}
	if _, err := os.Stat(filepath.Dir(f.validated[0])); !os.IsNotExist(err) {
		t.Fatal("the preflight directory must be removed")
	}
	if len(f.svc.units) != 1 || f.svc.loads != 1 || !result.Service.Running {
		t.Fatalf("units=%v loads=%d result=%+v", f.svc.units, f.svc.loads, result)
	}
	if f.svc.spec.WindowsService != "BeaconSentinelForwarder" || len(f.svc.spec.Env) != 0 {
		t.Fatalf("spec = %+v", f.svc.spec)
	}
}

func TestForwarderInstallRefusesBadInput(t *testing.T) {
	newForwarderFixture(t)
	cases := map[string]func(*InstallOptions){
		"plain http endpoint": func(o *InstallOptions) { o.Endpoint = "http://dcr.example.com" },
		"endpoint with path":  func(o *InstallOptions) { o.Endpoint = "https://dcr.example.com/dataCollectionRules/x" },
		"bad DCR id":          func(o *InstallOptions) { o.DCRImmutableID = "my-dcr" },
		"bad tenant":          func(o *InstallOptions) { o.TenantID = "contoso.onmicrosoft.com" },
		"bad client":          func(o *InstallOptions) { o.ClientID = "app" },
		"no credential":       func(o *InstallOptions) { o.ClientSecret = "" },
		"both credentials":    func(o *InstallOptions) { o.CertificatePath = "x.pfx" },
		"bad read_from":       func(o *InstallOptions) { o.ReadFrom = "middle" },
		"http authority":      func(o *InstallOptions) { o.AuthorityHost = "http://login.example.com" },
	}
	for name, mutate := range cases {
		o := validInstall()
		mutate(&o)
		if _, err := Install(o); err == nil {
			t.Errorf("%s: Install accepted it", name)
		}
	}
}

// A config Vector rejects changes nothing: an existing forwarder keeps running as it was.
func TestForwarderInstallRejectedByVectorLeavesTheOldForwarder(t *testing.T) {
	f := newForwarderFixture(t)
	if _, err := Install(validInstall()); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, filepath.Join(f.dir, configFileName))
	beforeSecrets := readFile(t, filepath.Join(f.dir, secretsFileName))
	f.reject = errors.New("vector validate failed")
	changed := validInstall()
	changed.ClientSecret = "a-new-secret"
	changed.DCRImmutableID = "dcr-ffffffffffffffffffffffffffffffff"
	if _, err := Install(changed); err == nil {
		t.Fatal("Install must fail when Vector rejects the config")
	}
	if readFile(t, filepath.Join(f.dir, configFileName)) != before || readFile(t, filepath.Join(f.dir, secretsFileName)) != beforeSecrets {
		t.Fatal("a rejected install changed the installed config or credential")
	}
	if f.svc.loads != 1 || len(f.svc.units) != 1 {
		t.Fatalf("a rejected install touched the service: loads=%d units=%d", f.svc.loads, len(f.svc.units))
	}
}

func TestForwarderInstallWithACertificate(t *testing.T) {
	f := newForwarderFixture(t)
	pfx := filepath.Join(t.TempDir(), "app.pfx")
	if err := os.WriteFile(pfx, []byte("pfx bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := validInstall()
	o.ClientSecret, o.CertificatePath = "", pfx
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	config := readFile(t, filepath.Join(f.dir, configFileName))
	if !strings.Contains(config, `azure_credential_kind = "client_certificate_credential"`) ||
		!strings.Contains(config, `certificate_path = "`+siempack.TOMLEscape(filepath.Join(f.dir, certificateFileName))+`"`) ||
		strings.Contains(config, "certificate_password") || strings.Contains(config, "[secret.") {
		t.Fatalf("certificate config without a password:\n%s", config)
	}
	if readFile(t, filepath.Join(f.dir, certificateFileName)) != "pfx bytes" {
		t.Fatal("the certificate was not copied into the forwarder's directory")
	}
	if _, err := os.Stat(filepath.Join(f.dir, secretsFileName)); !os.IsNotExist(err) {
		t.Fatal("a certificate without a password needs no secrets file")
	}

	o.CertificatePassword = "pfx-password"
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	config = readFile(t, filepath.Join(f.dir, configFileName))
	if !strings.Contains(config, "certificate_password = '''SECRET[beacon_sentinel.certificate_password]'''") || strings.Contains(config, "pfx-password") {
		t.Fatalf("certificate config with a password:\n%s", config)
	}
	if !strings.Contains(readFile(t, filepath.Join(f.dir, secretsFileName)), "pfx-password") {
		t.Fatal("the certificate password belongs in secrets.json")
	}
}

func TestForwarderAuthorityHostReachesTheServiceEnvironment(t *testing.T) {
	f := newForwarderFixture(t)
	o := validInstall()
	o.AuthorityHost = "https://login.microsoftonline.us/"
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if got := f.svc.spec.Env; len(got) != 1 || got[0] != "AZURE_AUTHORITY_HOST=https://login.microsoftonline.us" {
		t.Fatalf("service environment = %v", got)
	}
}

func TestForwarderRemoveKeepsStateOnlyWhenAsked(t *testing.T) {
	f := newForwarderFixture(t)
	if _, err := Install(validInstall()); err != nil {
		t.Fatal(err)
	}
	if err := Remove(true); err != nil {
		t.Fatal(err)
	}
	if f.svc.unloads != 1 || f.svc.removed != 1 {
		t.Fatalf("unloads=%d removed=%d", f.svc.unloads, f.svc.removed)
	}
	if _, err := ReadSettings(); err != nil {
		t.Fatalf("keepState removed the state: %v", err)
	}
	if err := Remove(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.dir); !os.IsNotExist(err) {
		t.Fatal("Remove(false) must delete the forwarder's directory")
	}
	if _, err := ReadSettings(); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("ReadSettings after removal: %v", err)
	}
}

func TestForwarderReconcile(t *testing.T) {
	f := newForwarderFixture(t)
	if got, err := Reconcile(ReconcileOptions{}); err != nil || got.Skipped == "" {
		t.Fatalf("not installed: %+v %v", got, err)
	}
	if _, err := Install(validInstall()); err != nil {
		t.Fatal(err)
	}
	if got, err := Reconcile(ReconcileOptions{}); err != nil || !got.Unchanged || f.svc.loads != 1 {
		t.Fatalf("unchanged: %+v %v loads=%d", got, err, f.svc.loads)
	}
	// Install and repair restart it even when nothing changed: they may have replaced the log.
	if got, err := Reconcile(ReconcileOptions{Restart: true}); err != nil || !got.Restarted || f.svc.loads != 2 {
		t.Fatalf("restart: %+v %v loads=%d", got, err, f.svc.loads)
	}
	// A config an older version wrote is replaced and the service restarted.
	configPath := filepath.Join(f.dir, configFileName)
	if err := os.WriteFile(configPath, []byte("# old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Reconcile(ReconcileOptions{}); err != nil || !got.Restarted || f.svc.loads != 3 {
		t.Fatalf("changed: %+v %v loads=%d", got, err, f.svc.loads)
	}
	if strings.HasPrefix(readFile(t, configPath), "# old") {
		t.Fatal("Reconcile did not rewrite the config")
	}
	// One Vector rejects is a warning that keeps what is there.
	if err := os.WriteFile(configPath, []byte("# old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.reject = errors.New("vector validate failed")
	if got, err := Reconcile(ReconcileOptions{}); err != nil || got.Warning == "" || f.svc.loads != 3 {
		t.Fatalf("rejected: %+v %v loads=%d", got, err, f.svc.loads)
	}
	if readFile(t, configPath) != "# old\n" {
		t.Fatal("a rejected reconcile replaced the config")
	}
	// After an MSI upgrade removed the service, a rejected config still gets the service back, on
	// the previous config.
	if err := Remove(true); err != nil {
		t.Fatal(err)
	}
	f.svc.current = false
	if got, err := Reconcile(ReconcileOptions{Restart: true}); err != nil || got.Warning == "" || !got.Restarted || !f.svc.running {
		t.Fatalf("rejected after upgrade: %+v %v running=%t", got, err, f.svc.running)
	}
	if readFile(t, configPath) != "# old\n" {
		t.Fatal("a rejected reconcile replaced the config")
	}
	f.reject = nil
	// The MSI upgrade removes the service and keeps the state; the install that follows puts the
	// service back.
	if err := Remove(true); err != nil {
		t.Fatal(err)
	}
	f.svc.current = false
	if got, err := Reconcile(ReconcileOptions{Restart: true}); err != nil || !got.Restarted || !f.svc.running {
		t.Fatalf("after upgrade: %+v %v running=%t", got, err, f.svc.running)
	}
}

func TestForwarderOnAnUnsupportedHost(t *testing.T) {
	f := newForwarderFixture(t)
	f.svc.unsupported = "Windows has no per-user service manager"
	if _, err := Install(validInstall()); err == nil || !strings.Contains(err.Error(), "per-user") {
		t.Fatalf("Install on an unsupported host: %v", err)
	}
	if _, err := os.Stat(f.dir); !os.IsNotExist(err) {
		t.Fatal("an unsupported install must write nothing")
	}
}

func TestLastVectorProblemReadsTheNewestDailyLog(t *testing.T) {
	dir := t.TempDir()
	if lastVectorProblem(dir) != "" {
		t.Fatal("no logs means no problem")
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("vector-2026-11-03.log", `{"timestamp":"2026-11-03T10:00:00Z","message":"old","metadata":{"level":"WARN"}}`+"\n")
	write("vector-2026-11-04.log", `{"timestamp":"2026-11-04T10:00:00Z","message":"first","metadata":{"level":"WARN"}}`+"\n"+
		`{"timestamp":"2026-11-04T11:00:00Z","message":"Retrying after response.","metadata":{"level":"WARN"}}`+"\n")
	if got := lastVectorProblem(dir); got != "2026-11-04T11:00:00Z WARN Retrying after response." {
		t.Fatalf("lastVectorProblem = %q", got)
	}
}

// The manual pack's config is still for running Vector by hand with the values in its environment.
func TestManualVectorConfigKeepsItsEnvironmentContract(t *testing.T) {
	manual := manualVectorConfig()
	for _, want := range []string{
		`endpoint = "${BEACON_SENTINEL_DCE_ENDPOINT}"`,
		`azure_client_secret = "${AZURE_CLIENT_SECRET}"`,
		`data_dir = "/var/lib/vector/beacon-sentinel"`,
		`read_from = "end"`,
		`[sinks.beacon_sentinel.retry_strategy]`,
		`[sinks.beacon_sentinel.acknowledgements]`,
	} {
		if !strings.Contains(manual, want) {
			t.Errorf("manual vector.toml is missing %q", want)
		}
	}
	for _, unwanted := range []string{"{{", "SECRET[", "internal_logs"} {
		if strings.Contains(strings.ReplaceAll(manual, "{{LOG_PATH}}", ""), unwanted) {
			t.Errorf("manual vector.toml contains %q", unwanted)
		}
	}
}

// A Vector that never reaches running is removed, not left hanging with its binary locked.
func TestForwarderInstallRemovesAServiceThatNeverStarted(t *testing.T) {
	f := newForwarderFixture(t)
	f.svc.loadErr = errors.New("the BeaconSentinelForwarder service is still starting after 1m0s")
	if _, err := Install(validInstall()); err == nil || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("Install: %v", err)
	}
	if f.svc.unloads != 1 {
		t.Fatalf("unloads = %d, want the hung service removed", f.svc.unloads)
	}
	if _, err := ReadSettings(); err != nil {
		t.Fatalf("the settings must stay for the next install or reconcile: %v", err)
	}
}

// The Vector install was told to run is the one reconcile keeps running.
func TestForwarderReconcileKeepsTheVectorInstallWasGiven(t *testing.T) {
	newForwarderFixture(t)
	custom := filepath.Join(t.TempDir(), "vector")
	var asked []string
	findVector = func(explicit string) (asymptote.VectorInfo, error) {
		asked = append(asked, explicit)
		return asymptote.VectorInfo{Path: custom, Version: "0.56.0"}, nil
	}
	o := validInstall()
	o.VectorBin = custom
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if _, err := Reconcile(ReconcileOptions{Restart: true}); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 || asked[0] != custom || asked[1] != custom {
		t.Fatalf("findVector was asked for %q, want %s twice", asked, custom)
	}
}

// Vector's validate takes secret references literally, so the copy it checks has the credential
// written in, and is gone afterwards; the config Vector runs keeps only the reference.
func TestForwarderValidatesWithTheCredentialAndRunsWithTheReference(t *testing.T) {
	f := newForwarderFixture(t)
	var checked []string
	validateVector = func(_, config string) error {
		data, err := os.ReadFile(config)
		if err != nil {
			return err
		}
		checked = append(checked, string(data))
		return nil
	}
	pfx := filepath.Join(t.TempDir(), "app.pfx")
	if err := os.WriteFile(pfx, []byte("pfx bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := validInstall()
	o.ClientSecret, o.CertificatePath, o.CertificatePassword = "", pfx, `p"a\$s'`
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if len(checked) != 1 || !strings.Contains(checked[0], `certificate_password = '''p"a\$$s''''`) {
		t.Fatalf("the checked copy must carry the password itself:\n%v", checked)
	}
	config := readFile(t, filepath.Join(f.dir, configFileName))
	if strings.Contains(config, `p"a`) || !strings.Contains(config, "SECRET[beacon_sentinel.certificate_password]") {
		t.Fatalf("the installed config must hold only the reference:\n%s", config)
	}
	matches, _ := filepath.Glob(filepath.Join(f.dir, "*", ".validate-*"))
	more, _ := filepath.Glob(filepath.Join(f.dir, ".validate-*"))
	if len(matches)+len(more) != 0 {
		t.Fatalf("validation copies left behind: %v %v", matches, more)
	}
	// Reconcile checks a rewritten config the same way.
	if err := os.WriteFile(filepath.Join(f.dir, configFileName), []byte("# old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Reconcile(ReconcileOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(checked) != 2 || !strings.Contains(checked[1], `certificate_password = '''p"a\$$s''''`) {
		t.Fatalf("reconcile must check a copy with the password in it")
	}
}

// An install told not to start services still registers the forwarder, which an MSI upgrade's
// uninstall step removed; it is left stopped.
func TestForwarderReconcileWithoutStartingRegistersTheService(t *testing.T) {
	f := newForwarderFixture(t)
	if _, err := Install(validInstall()); err != nil {
		t.Fatal(err)
	}
	if err := Remove(true); err != nil {
		t.Fatal(err)
	}
	f.svc.current = false
	units, loads := len(f.svc.units), f.svc.loads
	got, err := Reconcile(ReconcileOptions{NoStart: true})
	if err != nil || !got.NotStarted || len(f.svc.units) != units+1 || f.svc.loads != loads {
		t.Fatalf("got %+v %v units=%d loads=%d", got, err, len(f.svc.units), f.svc.loads)
	}
}

// Without Beacon's settings there is nothing of Beacon's to remove, even when a service exists
// under the same name (the Jamf guide had people register the macOS label by hand).
func TestForwarderRemoveLeavesAServiceBeaconDidNotSetUp(t *testing.T) {
	f := newForwarderFixture(t)
	if err := Remove(false); err != nil {
		t.Fatal(err)
	}
	if f.svc.unloads != 0 || f.svc.removed != 0 {
		t.Fatalf("unloads=%d removed=%d; a service Beacon did not set up was touched", f.svc.unloads, f.svc.removed)
	}
}

// A Vector chosen through BEACON_VECTOR_BIN is recorded as the binary it resolved to.
func TestForwarderRecordsAVectorChosenThroughTheEnvironment(t *testing.T) {
	newForwarderFixture(t)
	t.Setenv(asymptote.VectorBinEnv, "/env/vector")
	findVector = func(explicit string) (asymptote.VectorInfo, error) {
		return asymptote.VectorInfo{Path: "/env/vector", Version: "0.56.0"}, nil
	}
	if _, err := Install(validInstall()); err != nil {
		t.Fatal(err)
	}
	settings, err := ReadSettings()
	if err != nil || settings.VectorBin != "/env/vector" {
		t.Fatalf("settings = %+v (%v)", settings, err)
	}
}

// A service that would not stop keeps its settings, so a later remove can still find it.
func TestForwarderRemoveKeepsTheSettingsWhenTheServiceWouldNotStop(t *testing.T) {
	f := newForwarderFixture(t)
	if _, err := Install(validInstall()); err != nil {
		t.Fatal(err)
	}
	f.svc.unloadErr = errors.New("the service did not stop")
	if err := Remove(false); err == nil {
		t.Fatal("Remove must report a service that would not stop")
	}
	if _, err := ReadSettings(); err != nil {
		t.Fatalf("the settings must stay for a retry: %v", err)
	}
}

// A repair that moves the log updates what status reports, along with what Vector reads.
func TestForwarderReconcileRecordsAMovedLog(t *testing.T) {
	newForwarderFixture(t)
	if _, err := Install(validInstall()); err != nil {
		t.Fatal(err)
	}
	if _, err := Reconcile(ReconcileOptions{LogPath: "/srv/beacon/runtime.jsonl", Restart: true}); err != nil {
		t.Fatal(err)
	}
	settings, err := ReadSettings()
	if err != nil || settings.LogPath != "/srv/beacon/runtime.jsonl" {
		t.Fatalf("settings = %+v (%v)", settings, err)
	}
}

// A credential Vector's secret backend cannot carry into the config is refused before anything runs.
func TestForwarderRefusesACredentialVectorCannotTake(t *testing.T) {
	newForwarderFixture(t)
	for _, secret := range []string{"ends'''here", "line\nbreak", "tab\tin"} {
		o := validInstall()
		o.ClientSecret = secret
		if _, err := Install(o); err == nil || !strings.Contains(err.Error(), "secret store") {
			t.Errorf("secret %q: %v", secret, err)
		}
	}
}
