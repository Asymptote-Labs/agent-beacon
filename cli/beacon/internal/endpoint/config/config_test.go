package config

import (
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultUserConfigUsesHomeScopedPaths(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)

	cfg := Default(true, filepath.Join(home, "runtime.jsonl"))

	if !cfg.UserMode {
		t.Fatal("expected user mode config")
	}
	if cfg.Collector.GRPCPort != DefaultGRPCPort || cfg.Collector.HTTPPort != DefaultHTTPPort {
		t.Fatalf("unexpected ports: grpc=%d http=%d", cfg.Collector.GRPCPort, cfg.Collector.HTTPPort)
	}
	if got, want := cfg.Collector.ConfigPath, filepath.Join(home, ".beacon", "endpoint", "otelcol.yaml"); got != want {
		t.Fatalf("ConfigPath = %q, want %q", got, want)
	}
	if got, want := cfg.Collector.SpoolPath, filepath.Join(home, ".beacon", "endpoint", "spool", "otlp.jsonl"); got != want {
		t.Fatalf("SpoolPath = %q, want %q", got, want)
	}
	if len(cfg.Harnesses) != 2 || cfg.Harnesses[0] != "claude" || cfg.Harnesses[1] != "codex" {
		t.Fatalf("unexpected default harnesses: %#v", cfg.Harnesses)
	}
	inventory := InventoryConfig(cfg)
	if !inventory.Enabled || len(inventory.Runtimes) != 0 {
		t.Fatalf("unexpected inventory defaults: %#v", inventory)
	}
	if inventory.IncludeContents || inventory.MaxContentBytes != 0 {
		t.Fatalf("content capture should default off: %#v", inventory)
	}
}

func TestInventoryContentCaptureOptIn(t *testing.T) {
	include := true
	cfg := Config{Inventory: &Inventory{IncludeContents: &include, MaxContentBytes: 4096}}
	inventory := InventoryConfig(cfg)
	if !inventory.IncludeContents {
		t.Fatal("IncludeContents = false, want true when opted in")
	}
	if inventory.MaxContentBytes != 4096 {
		t.Fatalf("MaxContentBytes = %d, want 4096", inventory.MaxContentBytes)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	logPath := filepath.Join(home, "logs", "runtime.jsonl")

	cfg := Default(true, logPath)
	cfg.Collector.BinaryPath = filepath.Join(home, "bin", "otelcol")
	cfg.Collector.IncludeRuntimeMetrics = true
	cfg.Collector.IncludeCodexSpans = true
	cfg.ClaudeCaptureModelContext = true
	cfg.EventCategories = []string{"tool", "session"}
	enabled := true
	cfg.Inventory = &Inventory{Enabled: &enabled, Runtimes: []string{"cursor"}}

	path, err := Save(cfg)
	if err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	if got, want := path, filepath.Join(home, UserConfigPath); got != want {
		t.Fatalf("Save path = %q, want %q", got, want)
	}

	loaded, err := Load(true)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.LogPath != logPath {
		t.Fatalf("LogPath = %q, want %q", loaded.LogPath, logPath)
	}
	if loaded.Collector.BinaryPath != cfg.Collector.BinaryPath {
		t.Fatalf("BinaryPath = %q, want %q", loaded.Collector.BinaryPath, cfg.Collector.BinaryPath)
	}
	if !loaded.Collector.IncludeRuntimeMetrics {
		t.Fatal("IncludeRuntimeMetrics = false, want true")
	}
	if !loaded.Collector.IncludeCodexSpans {
		t.Fatal("IncludeCodexSpans = false, want true")
	}
	if !loaded.ClaudeCaptureModelContext {
		t.Fatal("ClaudeCaptureModelContext = false, want true")
	}
	if len(loaded.EventCategories) != 2 || loaded.EventCategories[1] != "session" {
		t.Fatalf("EventCategories did not round-trip: %#v", loaded.EventCategories)
	}
	inventory := InventoryConfig(loaded)
	if !inventory.Enabled || len(inventory.Runtimes) != 1 || inventory.Runtimes[0] != "cursor" {
		t.Fatalf("Inventory did not round-trip: %#v", inventory)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if strings.Contains(string(data), "content_retention") {
		t.Fatalf("saved config unexpectedly contains legacy content_retention: %s", string(data))
	}
}

func TestSaveLoadSplunkHECRoundTripAndPrivatePermissions(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	home := t.TempDir()
	testenv.SetHome(t, home)
	cfg := Default(true, filepath.Join(home, "logs", "runtime.jsonl"))
	cfg.Destinations = &Destinations{SplunkHEC: &SplunkHEC{
		Endpoint: "https://splunk.example:8088/services/collector",
		Token:    "hec-token",
		Index:    "beacon",
	}}

	path, err := Save(cfg)
	if err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0600); got != want {
		t.Fatalf("config permissions = %o, want %o", got, want)
	}

	loaded, err := Load(true)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	splunk := loaded.Destinations.SplunkHEC
	if !splunk.Enabled {
		t.Fatal("SplunkHEC.Enabled = false, want true")
	}
	if splunk.Source != DefaultSplunkSource || splunk.Sourcetype != DefaultSplunkSourcetype {
		t.Fatalf("Splunk defaults = source %q sourcetype %q", splunk.Source, splunk.Sourcetype)
	}
	if splunk.Token != "hec-token" || splunk.Index != "beacon" {
		t.Fatalf("Splunk config did not round-trip: %#v", splunk)
	}
}

func TestSaveRejectsIncompleteSplunkHEC(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	cfg := Default(true, filepath.Join(home, "runtime.jsonl"))
	cfg.Destinations = &Destinations{SplunkHEC: &SplunkHEC{Endpoint: "https://splunk.example:8088/services/collector"}}

	if _, err := Save(cfg); err == nil {
		t.Fatal("expected missing Splunk token error")
	}
}

func TestSaveLoadFalconHECRoundTripAndPrivatePermissions(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	home := t.TempDir()
	testenv.SetHome(t, home)
	cfg := Default(true, filepath.Join(home, "logs", "runtime.jsonl"))
	cfg.Destinations = &Destinations{FalconHEC: &FalconHEC{
		Endpoint: "https://cloud.us.humio.com/api/v1/ingest/hec",
		Token:    "ingest-token",
		Index:    "beacon-repo",
	}}

	path, err := Save(cfg)
	if err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0600); got != want {
		t.Fatalf("config permissions = %o, want %o", got, want)
	}

	loaded, err := Load(true)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	falcon := loaded.Destinations.FalconHEC
	if !falcon.Enabled {
		t.Fatal("FalconHEC.Enabled = false, want true")
	}
	if falcon.Source != DefaultFalconSource || falcon.Sourcetype != DefaultFalconSourcetype {
		t.Fatalf("Falcon defaults = source %q sourcetype %q", falcon.Source, falcon.Sourcetype)
	}
	if falcon.Token != "ingest-token" || falcon.Index != "beacon-repo" {
		t.Fatalf("Falcon config did not round-trip: %#v", falcon)
	}
}

func TestSaveRejectsIncompleteFalconHEC(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	cfg := Default(true, filepath.Join(home, "runtime.jsonl"))
	cfg.Destinations = &Destinations{FalconHEC: &FalconHEC{Endpoint: "https://cloud.us.humio.com/api/v1/ingest/hec"}}

	if _, err := Save(cfg); err == nil {
		t.Fatal("expected missing Falcon token error")
	}
}

func TestLoadIgnoresLegacyContentRetention(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	path := filepath.Join(home, UserConfigPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"user_mode":true,"content_retention":"metadata"}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	loaded, err := Load(true)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !loaded.UserMode {
		t.Fatal("UserMode = false, want true")
	}
	if _, err := Save(loaded); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if strings.Contains(string(data), "content_retention") {
		t.Fatalf("saved config unexpectedly preserved legacy content_retention: %s", string(data))
	}
}

func TestLoadRejectsCorruptJSON(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	path := filepath.Join(home, UserConfigPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0644); err != nil {
		t.Fatalf("write corrupt config: %v", err)
	}

	if _, err := Load(true); err == nil {
		t.Fatal("expected corrupt JSON error")
	}
}

// The forward's header values are credentials, but config.json must stay readable: ordinary
// accounts read the system config to find the system runtime log. So the values go to a 0600 file
// of their own and config.json carries only their names.
func TestSaveLoadOTLPForwardKeepsHeaderValuesOutOfConfigJSON(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	home := t.TempDir()
	testenv.SetHome(t, home)
	cfg := Default(true, filepath.Join(home, "logs", "runtime.jsonl"))
	cfg.Destinations = &Destinations{OTLPForward: &OTLPForward{
		Endpoint: " https://otel.backend.example ",
		Protocol: "http",
		Headers:  map[string]string{"authorization": "Bearer secret"},
	}}

	path, err := Save(cfg)
	if err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0644 {
		t.Fatalf("config.json permissions = %o, want 644", got)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "Bearer secret") || !strings.Contains(string(data), `"authorization"`) {
		t.Fatalf("config.json should name the header and not hold its value:\n%s", data)
	}
	secrets, err := os.Stat(OTLPForwardSecretsPath(true))
	if err != nil {
		t.Fatal(err)
	}
	if got := secrets.Mode().Perm(); got != 0600 {
		t.Fatalf("secrets permissions = %o, want 600", got)
	}

	loaded, err := Load(true)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	forward := loaded.Destinations.OTLPForward
	if !forward.Enabled || forward.Endpoint != "https://otel.backend.example" || forward.Protocol != OTLPForwardProtocolHTTP || forward.Headers["authorization"] != "Bearer secret" {
		t.Fatalf("OTLP forward did not round-trip: %#v", forward)
	}
	if strings.Join(forward.Harnesses, ",") != "claude_code,codex_cli,codex_desktop" {
		t.Fatalf("default harnesses = %v", forward.Harnesses)
	}

	// An account that cannot read the secrets still loads the forward, without the values.
	if err := os.Chmod(OTLPForwardSecretsPath(true), 0); err == nil && os.Geteuid() != 0 {
		loaded, err := Load(true)
		if err != nil {
			t.Fatalf("Load without the secrets returned error: %v", err)
		}
		if f := loaded.Destinations.OTLPForward; !f.Enabled || f.Headers != nil || strings.Join(f.HeaderNames, ",") != "authorization" {
			t.Fatalf("forward without secrets = %#v", f)
		}
	}

	// Removing the headers removes the secrets file.
	cfg.Destinations.OTLPForward.Headers = nil
	cfg.Destinations.OTLPForward.HeaderNames = nil
	if _, err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(OTLPForwardSecretsPath(true)); !os.IsNotExist(err) {
		t.Fatalf("secrets file left behind: %v", err)
	}
}

func TestValidateDestinationsRejectsBadOTLPForward(t *testing.T) {
	for name, forward := range map[string]OTLPForward{
		"no endpoint":        {Headers: map[string]string{"a": "b"}},
		"unknown protocol":   {Endpoint: "otel.example:4317", Protocol: "udp"},
		"http without a URL": {Endpoint: "otel.example:4318", Protocol: OTLPForwardProtocolHTTP},
		"header name colon":  {Endpoint: "otel.example:4317", Headers: map[string]string{"a:b": "c"}},
		"header value break": {Endpoint: "otel.example:4317", Headers: map[string]string{"a": "b\r\nx: y"}},
	} {
		t.Run(name, func(t *testing.T) {
			forward := forward
			if err := ValidateDestinations(&Destinations{OTLPForward: &forward}); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// Callers that edit config.json's raw JSON (the auto-update mode) write it as Save would: private
// when it holds a credential or already was, readable otherwise. On Windows the private case is the
// SYSTEM/Administrators-only DACL (sysacl.WriteSecretFile), which TestWriteSecretFile covers there.
func TestRewriteSystemConfigKeepsCredentialsPrivate(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	dir := t.TempDir()
	for _, tc := range []struct {
		name, body string
		wasPrivate bool
		want       os.FileMode
	}{
		{"hec token", `{"destinations":{"splunk_hec":{"endpoint":"https://splunk.example","token":"t"}}}`, false, 0600},
		{"already private", `{"auto_update":{"mode":"off"}}`, true, 0600},
		{"nothing secret", `{"auto_update":{"mode":"off"}}`, false, 0644},
		// A wrong-typed field elsewhere must not hide the token.
		{"token beside a bad field", `{"collector":{"grpc_port":"4317"},"destinations":{"falcon_hec":{"token":"t"}}}`, false, 0600},
		{"destinations of the wrong shape", `{"destinations":"oops"}`, false, 0644},
		{"unparseable", `{"destinations":`, false, 0600},
	} {
		path := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".json")
		if err := RewriteSystemConfig(path, []byte(tc.body), tc.wasPrivate); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != tc.want {
			t.Errorf("%s: permissions = %o, want %o", tc.name, got, tc.want)
		}
	}
}

// A package removal or cleanup can take the headers file while config.json still names them. Load
// says so instead of handing back a forward that silently sends without credentials.
func TestLoadReportsMissingOTLPForwardHeaders(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	cfg := Default(true, filepath.Join(home, "logs", "runtime.jsonl"))
	cfg.Destinations = &Destinations{OTLPForward: &OTLPForward{Endpoint: "otel.example:4317", Headers: map[string]string{"authorization": "Bearer x"}}}
	if _, err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	if loaded, err := Load(true); err != nil || loaded.Destinations.OTLPForward.HeadersMissing {
		t.Fatalf("HeadersMissing with the file present: %v", err)
	}
	if err := os.Remove(OTLPForwardSecretsPath(true)); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(true)
	if err != nil {
		t.Fatal(err)
	}
	if f := loaded.Destinations.OTLPForward; !f.HeadersMissing || f.Headers != nil || strings.Join(f.HeaderNames, ",") != "authorization" {
		t.Fatalf("forward without its headers file = %#v", f)
	}
}
