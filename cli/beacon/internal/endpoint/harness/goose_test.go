package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// goose's config.yaml is a file the operator writes by hand -- their provider, their model, their
// extensions, their permissions -- and Beacon changes two keys in it. Most of what is pinned here
// is therefore about what Beacon must *not* disturb, plus the one value that is easy to get wrong
// and fails silently: the endpoint must be the collector's HTTP address, because goose's build
// carries no gRPC transport.

func gooseConfigFixture(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(goosePathRootEnv, "")
	t.Setenv("XDG_CONFIG_HOME", "")
	// Cleared so a developer with any of these exported does not have the status assertions below
	// resolve against their own environment.
	for _, key := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_SDK_DISABLED",
		"OTEL_EXPORTER_OTLP_PROTOCOL", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
	} {
		t.Setenv(key, "")
	}
	path, err := gooseConfigPath()
	if err != nil {
		t.Fatalf("gooseConfigPath: %v", err)
	}
	return path
}

func writeGooseConfig(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func readGooseConfig(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var root map[string]interface{}
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return root
}

// ---------------------------------------------------------------------------
// Configure
// ---------------------------------------------------------------------------

func TestConfigureGooseWritesTheEndpointAndTimeout(t *testing.T) {
	path := gooseConfigFixture(t)

	written, err := ConfigureGoose(ConfigureOptions{Endpoint: "http://127.0.0.1:4318", UserMode: true})
	if err != nil {
		t.Fatalf("ConfigureGoose returned error: %v", err)
	}
	if written != path {
		t.Fatalf("ConfigureGoose wrote %q, want %q", written, path)
	}
	root := readGooseConfig(t, path)
	if got := root[gooseOTLPEndpointKey]; got != "http://127.0.0.1:4318" {
		t.Fatalf("%s = %v, want the local HTTP endpoint", gooseOTLPEndpointKey, got)
	}
	// Milliseconds, which is what OTEL_EXPORTER_OTLP_TIMEOUT means and what goose promotes it as.
	if got := root[gooseOTLPTimeoutKey]; got != gooseOTLPTimeoutMillis {
		t.Fatalf("%s = %v, want %d", gooseOTLPTimeoutKey, got, gooseOTLPTimeoutMillis)
	}
}

// The whole substance of this harness. goose's opentelemetry-otlp build enables only the HTTP
// transport, so the gRPC port every other OTLP harness is given would fail silently here -- the
// exporter builds, and the telemetry never arrives. The two callers that configure goose pass the
// HTTP endpoint; this pins that a gRPC-looking one is not what ends up reported as working.
func TestGooseStatusRejectsANonLocalEndpoint(t *testing.T) {
	path := gooseConfigFixture(t)
	writeGooseConfig(t, path, "otel_exporter_otlp_endpoint: https://otel.example.com:4318\n")

	status, msg := gooseStatus(path)
	if status != TelemetryMisconfigured {
		t.Fatalf("status = %q, want misconfigured for a remote endpoint (%s)", status, msg)
	}
}

// config.yaml holds an operator's provider, model, extensions and permissions. A map round-trip
// would reorder every key alphabetically and delete every comment; Beacon changes two keys and
// must leave the rest of the file looking like the file they wrote.
func TestConfigureGoosePreservesCommentsAndKeyOrder(t *testing.T) {
	path := gooseConfigFixture(t)
	original := `# goose configuration -- hand written, do not reformat
GOOSE_MODE: smart_approve

# the provider we settled on after the bake-off
active_provider: anthropic
GOOSE_TEMPERATURE: 0.2

extensions:
  developer:
    enabled: true # the built-in one
`
	writeGooseConfig(t, path, original)

	if _, err := ConfigureGoose(ConfigureOptions{Endpoint: "http://127.0.0.1:4318", UserMode: true}); err != nil {
		t.Fatalf("ConfigureGoose returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	rewritten := string(data)

	for _, comment := range []string{
		"# goose configuration -- hand written, do not reformat",
		"# the provider we settled on after the bake-off",
		"# the built-in one",
	} {
		if !strings.Contains(rewritten, comment) {
			t.Errorf("comment %q did not survive the rewrite:\n%s", comment, rewritten)
		}
	}
	// Key order, which a map round-trip would sort: GOOSE_MODE must still come before
	// active_provider, and both before the keys Beacon appended.
	order := []string{"GOOSE_MODE", "active_provider", "GOOSE_TEMPERATURE", "extensions", gooseOTLPEndpointKey}
	previous := -1
	for _, key := range order {
		index := strings.Index(rewritten, key+":")
		if index < 0 {
			t.Fatalf("key %q is missing from the rewritten file:\n%s", key, rewritten)
		}
		if index < previous {
			t.Fatalf("key %q moved ahead of an earlier key; the file was reordered:\n%s", key, rewritten)
		}
		previous = index
	}
	// Nesting is re-emitted at two spaces, not yaml.Marshal's default four: an edit that touched
	// two top-level keys must not silently re-indent the operator's extensions block.
	if !strings.Contains(rewritten, "\n  developer:") {
		t.Errorf("nested keys were re-indented away from two spaces:\n%s", rewritten)
	}
	// The operator's own values are unchanged, not merely present.
	root := readGooseConfig(t, path)
	if root["GOOSE_MODE"] != "smart_approve" || root["active_provider"] != "anthropic" {
		t.Fatalf("Beacon changed a value it does not own: %#v", root)
	}
	if extensions, ok := root["extensions"].(map[string]interface{}); !ok || extensions["developer"] == nil {
		t.Fatalf("the extensions block did not survive: %#v", root["extensions"])
	}
}

// A reconfigure replaces the existing value in place rather than appending a second key, which
// would leave a duplicate that YAML resolves to the last one and a reader resolves to whichever
// they see first.
func TestConfigureGooseReplacesAnExistingEndpointInPlace(t *testing.T) {
	path := gooseConfigFixture(t)
	writeGooseConfig(t, path, `otel_exporter_otlp_endpoint: "http://127.0.0.1:9999"
GOOSE_MODE: chat
`)

	if _, err := ConfigureGoose(ConfigureOptions{Endpoint: "http://127.0.0.1:4318", UserMode: true}); err != nil {
		t.Fatalf("ConfigureGoose returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if count := strings.Count(string(data), gooseOTLPEndpointKey+":"); count != 1 {
		t.Fatalf("%s appears %d times, want once:\n%s", gooseOTLPEndpointKey, count, data)
	}
	if got := readGooseConfig(t, path)[gooseOTLPEndpointKey]; got != "http://127.0.0.1:4318" {
		t.Fatalf("%s = %v, want the new endpoint", gooseOTLPEndpointKey, got)
	}
	// The replacement clears the old scalar's style, so a value that had been quoted does not keep
	// rendering the new one in quotes -- and, more importantly, a block scalar does not.
	if strings.Contains(string(data), `"http://127.0.0.1:4318"`) {
		t.Fatalf("the replaced value kept the old quoting style:\n%s", data)
	}
}

// An empty file is the common case on a machine where goose is installed but never configured. It
// decodes to a zero Node rather than to a mapping, so without the document-creation branch the
// first key would be set on nothing.
func TestConfigureGooseCreatesAMissingOrEmptyConfig(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T, path string){
		"missing":    func(t *testing.T, path string) {},
		"empty":      func(t *testing.T, path string) { writeGooseConfig(t, path, "") },
		"whitespace": func(t *testing.T, path string) { writeGooseConfig(t, path, "  \n\n") },
	} {
		t.Run(name, func(t *testing.T) {
			path := gooseConfigFixture(t)
			setup(t, path)
			if _, err := ConfigureGoose(ConfigureOptions{Endpoint: "http://127.0.0.1:4318"}); err != nil {
				t.Fatalf("ConfigureGoose returned error: %v", err)
			}
			if got := readGooseConfig(t, path)[gooseOTLPEndpointKey]; got != "http://127.0.0.1:4318" {
				t.Fatalf("%s = %v after configuring a %s file", gooseOTLPEndpointKey, got, name)
			}
		})
	}
}

// Invalid YAML is refused rather than overwritten. This is the opposite call from the hook
// installer, and deliberately: there Beacon owns the file, here the operator does, and a parse
// failure is far more likely to mean a config Beacon cannot read than one goose cannot.
func TestConfigureGooseRefusesInvalidYAML(t *testing.T) {
	path := gooseConfigFixture(t)
	writeGooseConfig(t, path, "GOOSE_MODE: [unclosed\n")

	if _, err := ConfigureGoose(ConfigureOptions{Endpoint: "http://127.0.0.1:4318"}); err == nil {
		t.Fatal("ConfigureGoose overwrote a config it could not parse")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "[unclosed") {
		t.Fatalf("the unparseable config was modified:\n%s", data)
	}
}

// Every configure backs the file up first, matching the other harness writers. The operator's
// config is the thing being edited, so the copy is what makes the edit reversible.
func TestConfigureGooseBacksUpTheExistingConfig(t *testing.T) {
	path := gooseConfigFixture(t)
	writeGooseConfig(t, path, "GOOSE_MODE: chat\n")

	if _, err := ConfigureGoose(ConfigureOptions{Endpoint: "http://127.0.0.1:4318"}); err != nil {
		t.Fatalf("ConfigureGoose returned error: %v", err)
	}
	matches, err := filepath.Glob(path + ".beacon.*.bak")
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("found %d backups, want 1", len(matches))
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(data) != "GOOSE_MODE: chat\n" {
		t.Fatalf("backup does not hold the original config:\n%s", data)
	}
}

// ---------------------------------------------------------------------------
// Status: the three ways the file says yes and goose exports nothing
// ---------------------------------------------------------------------------

func TestGooseStatusReportsEnabledForALocalEndpoint(t *testing.T) {
	path := gooseConfigFixture(t)
	writeGooseConfig(t, path, "otel_exporter_otlp_endpoint: http://127.0.0.1:4318\n")

	if status, msg := gooseStatus(path); status != TelemetryEnabled {
		t.Fatalf("status = %q (%s), want enabled", status, msg)
	}
}

func TestGooseStatusReportsDisabledWithNoEndpoint(t *testing.T) {
	path := gooseConfigFixture(t)
	writeGooseConfig(t, path, "GOOSE_MODE: chat\n")

	if status, _ := gooseStatus(path); status != TelemetryDisabled {
		t.Fatalf("status = %q, want disabled when no endpoint is configured", status)
	}
}

// promote_config_to_env copies the file's endpoint into the environment only when the variable is
// not already set, so OTEL_EXPORTER_OTLP_ENDPOINT in goose's launch environment silently overrides
// the file. An operator who pointed another tool at a remote collector has redirected goose too,
// and the config file still says local.
func TestGooseStatusDetectsAnEnvironmentEndpointOverride(t *testing.T) {
	path := gooseConfigFixture(t)
	writeGooseConfig(t, path, "otel_exporter_otlp_endpoint: http://127.0.0.1:4318\n")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://otel.vendor.example")

	status, msg := gooseStatus(path)
	if status != TelemetryMisconfigured {
		t.Fatalf("status = %q, want misconfigured when the environment overrides the file", status)
	}
	if !strings.Contains(msg, "otel.vendor.example") {
		t.Fatalf("message does not name the overriding endpoint: %s", msg)
	}
}

// An environment variable that agrees with the file is not an override.
func TestGooseStatusAcceptsAMatchingEnvironmentEndpoint(t *testing.T) {
	path := gooseConfigFixture(t)
	writeGooseConfig(t, path, "otel_exporter_otlp_endpoint: http://127.0.0.1:4318\n")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")

	if status, msg := gooseStatus(path); status != TelemetryEnabled {
		t.Fatalf("status = %q (%s), want enabled", status, msg)
	}
}

func TestGooseStatusDetectsOtelSDKDisabled(t *testing.T) {
	path := gooseConfigFixture(t)
	writeGooseConfig(t, path, "otel_exporter_otlp_endpoint: http://127.0.0.1:4318\n")
	t.Setenv("OTEL_SDK_DISABLED", "TRUE")

	status, msg := gooseStatus(path)
	if status != TelemetryMisconfigured {
		t.Fatalf("status = %q, want misconfigured when the SDK is disabled", status)
	}
	if !strings.Contains(msg, "OTEL_SDK_DISABLED") {
		t.Fatalf("message does not name the variable: %s", msg)
	}
}

// The sharpest of the three. goose is built without the gRPC transport, so a gRPC protocol
// selection makes it disable OTLP export rather than export -- it would panic its exporter threads
// otherwise. One stderr warning is the only signal, and the config file still says local.
func TestGooseStatusDetectsAGRPCProtocolSelection(t *testing.T) {
	for name, env := range map[string]struct{ key, value string }{
		"shared":          {"OTEL_EXPORTER_OTLP_PROTOCOL", "grpc"},
		"signal-specific": {"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "grpc"},
	} {
		t.Run(name, func(t *testing.T) {
			path := gooseConfigFixture(t)
			writeGooseConfig(t, path, "otel_exporter_otlp_endpoint: http://127.0.0.1:4318\n")
			t.Setenv(env.key, env.value)

			status, msg := gooseStatus(path)
			if status != TelemetryMisconfigured {
				t.Fatalf("status = %q, want misconfigured when gRPC is selected", status)
			}
			if !strings.Contains(msg, "HTTP") {
				t.Fatalf("message does not say what to use instead: %s", msg)
			}
		})
	}
}

// The HTTP spellings, and the unset default, must not be reported as a problem. The spec default
// when the variable is unset is http/protobuf, which is what goose's own signal_protocol_is_http
// treats as acceptable.
func TestGooseStatusAcceptsTheHTTPProtocols(t *testing.T) {
	for _, protocol := range []string{"", "http/protobuf", "http/json", "HTTP/PROTOBUF"} {
		t.Run(protocol, func(t *testing.T) {
			path := gooseConfigFixture(t)
			writeGooseConfig(t, path, "otel_exporter_otlp_endpoint: http://127.0.0.1:4318\n")
			t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", protocol)

			if status, msg := gooseStatus(path); status != TelemetryEnabled {
				t.Fatalf("status = %q (%s), want enabled for protocol %q", status, msg, protocol)
			}
		})
	}
}

// The signal-specific variable wins over the shared one, as the OTel specification requires and as
// goose's own resolution does. Without that ordering, an operator who set the shared variable to
// gRPC for another tool and the traces one back to HTTP for goose would be reported as broken.
func TestGooseProtocolPrefersTheSignalSpecificVariable(t *testing.T) {
	path := gooseConfigFixture(t)
	writeGooseConfig(t, path, "otel_exporter_otlp_endpoint: http://127.0.0.1:4318\n")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "http/protobuf")

	if status, msg := gooseStatus(path); status != TelemetryEnabled {
		t.Fatalf("status = %q (%s), want enabled; the traces protocol overrides the shared one",
			status, msg)
	}
}

// ---------------------------------------------------------------------------
// Path resolution
// ---------------------------------------------------------------------------

// goose calls etcetera's choose_app_strategy, which is XDG everywhere except Windows -- macOS
// included. That is the opposite of what a reader would assume, and getting it wrong fails
// silently by writing a file goose never reads.
func TestGooseConfigPathFollowsTheXDGLayout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(goosePathRootEnv, "")
	t.Setenv("XDG_CONFIG_HOME", "")

	path, err := gooseConfigPath()
	if err != nil {
		t.Fatalf("gooseConfigPath: %v", err)
	}
	if runtime := filepath.Base(filepath.Dir(path)); runtime != "goose" {
		t.Fatalf("config sits in %q, want a goose directory", runtime)
	}
	if filepath.Base(path) != "config.yaml" {
		t.Fatalf("config file is %q, want config.yaml", filepath.Base(path))
	}
}

func TestGooseConfigPathHonorsXDGConfigHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(goosePathRootEnv, "")
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	path, err := gooseConfigPath()
	if err != nil {
		t.Fatalf("gooseConfigPath: %v", err)
	}
	if want := filepath.Join(xdg, "goose", "config.yaml"); path != want {
		t.Fatalf("gooseConfigPath = %q, want %q", path, want)
	}
}

// GOOSE_PATH_ROOT relocates every goose directory and outranks XDG_CONFIG_HOME, matching goose's
// own resolution -- and a relative value is ignored, because goose ignores it.
func TestGooseConfigPathHonorsAnAbsolutePathRootAndIgnoresARelativeOne(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	root := t.TempDir()
	t.Setenv(goosePathRootEnv, root)
	path, err := gooseConfigPath()
	if err != nil {
		t.Fatalf("gooseConfigPath: %v", err)
	}
	if want := filepath.Join(root, "config", "config.yaml"); path != want {
		t.Fatalf("gooseConfigPath = %q, want %q", path, want)
	}

	t.Setenv(goosePathRootEnv, "relative/goose")
	path, err = gooseConfigPath()
	if err != nil {
		t.Fatalf("gooseConfigPath: %v", err)
	}
	if strings.Contains(path, "relative") {
		t.Fatalf("gooseConfigPath = %q; goose ignores a relative GOOSE_PATH_ROOT and so must this", path)
	}
}

// ---------------------------------------------------------------------------
// Discovery
// ---------------------------------------------------------------------------

// The canonical harness name, so the row this produces lines up with the events in the runtime log
// rather than introducing a second spelling for the same runtime.
func TestDiscoverGooseUsesTheCanonicalHarnessName(t *testing.T) {
	gooseConfigFixture(t)
	h := DiscoverGoose()
	if h.Name != "goose" {
		t.Fatalf("harness name = %q, want goose", h.Name)
	}
	if h.Capability != "otel_config" {
		t.Fatalf("capability = %q, want otel_config", h.Capability)
	}
	if h.TelemetryStatus != TelemetryMissing {
		t.Fatalf("status = %q, want missing with no config file present", h.TelemetryStatus)
	}
}

func TestDiscoverGooseReportsAConfiguredEndpoint(t *testing.T) {
	path := gooseConfigFixture(t)
	writeGooseConfig(t, path, "otel_exporter_otlp_endpoint: http://127.0.0.1:4318\n")

	h := DiscoverGoose()
	if h.TelemetryStatus != TelemetryEnabled {
		t.Fatalf("status = %q (%s), want enabled", h.TelemetryStatus, h.Message)
	}
	if h.ConfigPath != path {
		t.Fatalf("config path = %q, want %q", h.ConfigPath, path)
	}
	// A config file goose wrote is itself evidence goose is on the machine, which matters on a host
	// where the binary is not on Beacon's PATH -- a desktop-app install, or a shell Beacon did not
	// inherit.
	if !h.Detected {
		t.Fatal("a goose config file exists but the harness was not reported as detected")
	}
}

func TestDiscoverGooseIsInDiscoverAll(t *testing.T) {
	gooseConfigFixture(t)
	for _, h := range DiscoverAll() {
		if h.Name == "goose" {
			return
		}
	}
	t.Fatal("goose is missing from DiscoverAll; `beacon endpoint status` would never list it")
}

// ValidateConfigured is given the collector's gRPC address, and goose is the one harness that must
// never be measured against it. Its own status message is the more specific answer, and the shared
// wrapper would overwrite it with "endpoint could not be fully validated" for a goose install that
// is correctly pointed at the HTTP port.
func TestValidateConfiguredKeepsGoosesOwnMessage(t *testing.T) {
	path := gooseConfigFixture(t)
	writeGooseConfig(t, path, "otel_exporter_otlp_endpoint: http://127.0.0.1:4318\n")

	for _, result := range ValidateConfigured("http://127.0.0.1:4317") {
		if result.Harness != "goose" {
			continue
		}
		if result.Status != TelemetryEnabled {
			t.Fatalf("goose status = %q (%s), want enabled", result.Status, result.Message)
		}
		if strings.Contains(result.Message, "could not be fully validated") {
			t.Fatalf("goose was measured against the gRPC endpoint: %s", result.Message)
		}
		return
	}
	t.Fatal("goose is missing from ValidateConfigured")
}
