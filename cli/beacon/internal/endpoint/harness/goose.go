package harness

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"
)

// goose's OpenTelemetry export, pointed at the local collector.
//
// This is the second of goose's two collection paths and it is not a duplicate of the first. The
// hook path sees prompts, tool calls, commands and file edits; goose populates no tool output on a
// hook at all and sends no usage event, so token counts, reported cost, the provider and model, and
// the assistant's own reasoning reach Beacon only here. A goose endpoint wants both installed.
//
// What goose exports, from crates/goose/src/otel/otlp.rs and the spans in goose-agent: traces,
// metrics and logs, with a resource carrying service.name=goose, and OTel GenAI semconv spans --
// `chat` with gen_ai.provider.name, gen_ai.request.model, gen_ai.response.model,
// gen_ai.response.finish_reasons and the full gen_ai.usage.* set including cache reads and writes;
// `execute_tool` with gen_ai.tool.name and gen_ai.tool.call.id. Those are the names Beacon's
// collector exporter already reads, which is why this file only has to get the endpoint right.
//
// Two things about that endpoint are easy to get wrong and are the whole substance of this file.

// gooseOTLPProtocolNote records the constraint that separates goose from every other OTLP harness
// Beacon configures.
//
// goose's opentelemetry-otlp build enables only the HTTP transport (`http-proto`), not
// `grpc-tonic`. Claude Code, Codex and Gemini are all pointed at the collector's gRPC port; goose
// must be pointed at the HTTP one, and pointing it at 4317 would not fail loudly -- the exporter
// builds either way and the failure surfaces as telemetry that never arrives.
//
// Worse, goose *checks* for the mismatch in the one direction it can see: if the environment sets
// OTEL_EXPORTER_OTLP_PROTOCOL to a gRPC variant, goose disables the signal entirely and prints one
// warning to stderr, because exporting would panic its background threads. So an operator who set
// that variable for another tool has silently turned goose's export off, and nothing in the config
// file says so. gooseStatus reads the environment for exactly that reason.
const gooseOTLPProtocolNote = "goose exports OTLP over HTTP only; point it at the collector's HTTP port"

// goose config keys. These are read by promote_config_to_env, which copies them into the
// environment before the exporters are built -- and only when the corresponding variable is not
// already set, so an environment variable wins over the file.
const (
	gooseOTLPEndpointKey = "otel_exporter_otlp_endpoint"
	gooseOTLPTimeoutKey  = "otel_exporter_otlp_timeout"
)

// gooseOTLPTimeoutMillis is the export timeout written alongside the endpoint.
//
// Milliseconds, which is what OTEL_EXPORTER_OTLP_TIMEOUT means and what goose promotes it as. Ten
// seconds: the collector is on loopback, so an export that has not completed by then is not slow,
// it is not running -- and goose's exporters run on their own threads, so a longer value costs a
// hung background thread rather than a hung turn.
const gooseOTLPTimeoutMillis = 10000

// goosePathRootEnv is goose's override for every one of its directories, honored here for the same
// reason the hook installer honors it: on a machine that sets it, ~/.config/goose is not where
// goose looks, so writing there would configure a file nothing reads. goose requires it to be
// absolute and ignores it otherwise.
const goosePathRootEnv = "GOOSE_PATH_ROOT"

func DiscoverGoose() Harness {
	h := Harness{Name: "goose", DisplayName: "goose", Capability: "otel_config"}
	detectExecutable(&h, "goose")
	path, err := gooseConfigPath()
	if err != nil {
		h.TelemetryStatus = TelemetryMissing
		h.Message = err.Error()
		return h
	}
	h.ConfigPath = path
	if !h.Detected && fileExists(path) {
		h.Detected = true
	}
	if fileExists(path) {
		status, msg := gooseStatus(path)
		h.TelemetryStatus = status
		h.Message = msg
	} else {
		h.TelemetryStatus = TelemetryMissing
		h.Message = "goose config file was not found"
	}
	return h
}

// ConfigureGoose points goose's OTLP exporters at the local collector.
//
// opts.Endpoint must be the collector's HTTP endpoint; see gooseOTLPProtocolNote. The caller picks
// it, because the caller is what knows the configured port -- and the two callers that configure
// the gRPC-speaking harnesses pass a different value, which is precisely the mistake this comment
// exists to prevent.
func ConfigureGoose(opts ConfigureOptions) (string, error) {
	path, err := gooseConfigPath()
	if err != nil {
		return "", err
	}

	// The document is edited as a yaml.Node rather than round-tripped through a map, and that is
	// not fussiness: config.yaml is a file the operator writes by hand. It holds their provider,
	// their model, their extensions and their permissions, and a map round-trip would reorder every
	// key alphabetically and delete every comment in it. Beacon is changing two keys and must leave
	// the rest of the file looking like the file they wrote.
	var document yaml.Node
	existing, readErr := os.ReadFile(path)
	switch {
	case readErr == nil:
		if len(strings.TrimSpace(string(existing))) > 0 {
			if err := yaml.Unmarshal(existing, &document); err != nil {
				return "", fmt.Errorf("goose config YAML is invalid: %w", err)
			}
		}
		if err := backup(path, existing); err != nil {
			return "", err
		}
	case os.IsNotExist(readErr):
	default:
		return "", readErr
	}

	mapping, err := gooseRootMapping(&document)
	if err != nil {
		return "", err
	}
	setYAMLMappingValue(mapping, gooseOTLPEndpointKey, opts.Endpoint)
	setYAMLMappingValue(mapping, gooseOTLPTimeoutKey, fmt.Sprint(gooseOTLPTimeoutMillis))

	data, err := marshalYAMLDocument(&document)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	// 0600, matching the other harness config writers. config.yaml sits beside secrets.yaml in a
	// directory goose keeps per user, and nothing else needs to read it.
	return path, os.WriteFile(path, data, 0600)
}

// gooseRootMapping returns the document's top-level mapping, creating one for an empty document.
//
// An empty file is the common case on a machine where goose has been installed but never
// configured, and it decodes to a zero Node rather than to a mapping -- so without this the first
// key would be set on nothing.
func gooseRootMapping(document *yaml.Node) (*yaml.Node, error) {
	if document.Kind == 0 {
		document.Kind = yaml.DocumentNode
		document.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
		return document.Content[0], nil
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) == 0 {
		return nil, fmt.Errorf("goose config YAML is not a document")
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("goose config YAML top level is not a mapping")
	}
	return root, nil
}

// marshalYAMLDocument renders an edited document at two-space indentation.
//
// yaml.Marshal defaults to four, which would silently re-indent every nested block in the
// operator's file -- their extensions map, their permissions -- on an edit that touched two
// top-level keys. Two spaces is what goose's own writer emits and what the documented examples use.
//
// What cannot be preserved, and is stated here rather than discovered from a diff: yaml.v3 does not
// retain blank lines between top-level keys, so a config written with paragraph breaks comes back
// without them. Comments, key order, values and nesting all survive; the blank lines do not. That
// is the reason ConfigureGoose backs the file up before writing.
func marshalYAMLDocument(document *yaml.Node) ([]byte, error) {
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// setYAMLMappingValue sets a key in a mapping node, replacing the value in place when the key is
// already present so that its position and any comment attached to it survive.
func setYAMLMappingValue(mapping *yaml.Node, key, value string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value != key {
			continue
		}
		// Style is cleared along with the value: a key that was previously written as a quoted
		// string or a block scalar would otherwise keep that style and render the new value in it.
		mapping.Content[i+1].Kind = yaml.ScalarNode
		mapping.Content[i+1].Tag = ""
		mapping.Content[i+1].Style = 0
		mapping.Content[i+1].Value = value
		mapping.Content[i+1].Content = nil
		return
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Value: value})
}

// gooseStatus reports whether goose is configured to export to a local collector.
//
// It reads the environment as well as the file, which none of the other harness status functions
// do, and there are two distinct reasons -- both cases where the file says telemetry is on and
// goose is exporting nothing:
//
//   - promote_config_to_env copies the file's endpoint into the environment only when the variable
//     is not already set, so OTEL_EXPORTER_OTLP_ENDPOINT in goose's launch environment silently
//     overrides the file. An operator who pointed another tool at a remote collector has
//     redirected goose too.
//   - OTEL_SDK_DISABLED and a gRPC OTEL_EXPORTER_OTLP_PROTOCOL each turn export off entirely,
//     the second because goose is built without the gRPC transport and disables the signal rather
//     than panicking its exporter threads.
//
// Reporting "enabled" from the file alone would be wrong in all three cases, and wrong in the
// direction that matters: it would tell an operator a runtime is covered when it is silent.
func gooseStatus(path string) (TelemetryStatus, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TelemetryMissing, err.Error()
	}
	var root map[string]interface{}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return TelemetryMisconfigured, "goose config YAML is invalid"
	}
	endpoint := strings.TrimSpace(fmt.Sprint(root[gooseOTLPEndpointKey]))
	if endpoint == "" || endpoint == "<nil>" {
		return TelemetryDisabled, "goose OTLP endpoint is not configured"
	}
	if override := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")); override != "" && override != endpoint {
		return TelemetryMisconfigured, fmt.Sprintf(
			"OTEL_EXPORTER_OTLP_ENDPOINT=%s in the environment overrides the configured %s; goose "+
				"promotes the config file only when the variable is unset", override, endpoint)
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("OTEL_SDK_DISABLED")), "true") {
		return TelemetryMisconfigured, "OTEL_SDK_DISABLED=true in the environment turns goose telemetry off"
	}
	if protocol := gooseConfiguredProtocol(); protocol != "" && !gooseProtocolIsHTTP(protocol) {
		return TelemetryMisconfigured, fmt.Sprintf(
			"OTEL_EXPORTER_OTLP_PROTOCOL=%s selects gRPC, which this goose build does not include; "+
				"goose disables OTLP export rather than exporting. %s", protocol, gooseOTLPProtocolNote)
	}
	if !strings.Contains(endpoint, "127.0.0.1") && !strings.Contains(endpoint, "localhost") {
		return TelemetryMisconfigured, fmt.Sprintf("goose OTLP endpoint %s is not local", endpoint)
	}
	return TelemetryEnabled, "goose exports OTLP to the local collector"
}

// gooseConfiguredProtocol reads the OTLP protocol the environment selects, preferring the
// signal-specific variable over the shared one, as the OTel specification requires and as goose's
// signal_protocol_is_http does.
//
// Traces is the signal asked about because it is the one that carries goose's GenAI spans; a build
// that exported metrics and not traces would still be reported here as configured, which is the
// honest answer for a check about whether the endpoint is pointed at the right place.
func gooseConfiguredProtocol() string {
	if value := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL")); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"))
}

// gooseProtocolIsHTTP mirrors goose's own signal_protocol_is_http: the spec default when unset is
// http/protobuf, and anything that is not one of the two HTTP spellings is a gRPC variant goose
// will refuse to use.
func gooseProtocolIsHTTP(protocol string) bool {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "", "http/protobuf", "http/json":
		return true
	default:
		return false
	}
}

// gooseConfigPath resolves goose's config.yaml, mirroring how goose resolves it.
//
// GOOSE_PATH_ROOT relocates it wholesale, and goose ignores a relative value -- so honoring one
// would write a file nothing reads, under the working directory of all places.
//
// Otherwise goose calls etcetera's choose_app_strategy, which is the Windows strategy on Windows
// and XDG everywhere else -- macOS included. That last part is the one worth stating, because it is
// the opposite of what a reader would assume and getting it wrong fails silently: on macOS goose
// reads ~/.config/goose/config.yaml, not ~/Library/Application Support. (goose's own paths.rs
// mentions the Application Support directory, but as a legacy location kept for compatibility, and
// choose_native_strategy -- the variant that would resolve there -- is not the one it calls.)
//
// On Windows the same call gives %APPDATA%\Block\goose\config, from the author and app name
// goose passes: "Block" is deliberate there and is not the product's name, which is why it cannot
// be derived from anything else in this file.
func gooseConfigPath() (string, error) {
	if root := strings.TrimSpace(os.Getenv(goosePathRootEnv)); filepath.IsAbs(root) {
		return filepath.Join(root, "config", "config.yaml"), nil
	}
	if runtime.GOOS == "windows" {
		appData, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(appData, "Block", "goose", "config", "config.yaml"), nil
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "goose", "config.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "goose", "config.yaml"), nil
}
