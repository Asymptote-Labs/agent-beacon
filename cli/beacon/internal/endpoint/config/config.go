package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/atomicfile"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sysacl"
)

const (
	// DarwinSystemBaseDir is the macOS system-mode state directory.
	DarwinSystemBaseDir = "/Library/Application Support/Beacon/Endpoint"
	// LinuxSystemBaseDir follows the FHS: configuration under /etc, which is where a
	// package-managed install and an admin both expect to find it.
	LinuxSystemBaseDir = "/etc/beacon/endpoint"
	// WindowsSystemBaseFallback is used when %ProgramData% is unset. Windows always defines it in
	// practice, but a service running under an unusual profile can arrive without it, and writing
	// to a relative path in that case would scatter machine state into whatever directory the
	// process happened to start in.
	WindowsSystemBaseFallback = `C:\ProgramData`

	UserConfigPath  = ".beacon/endpoint/config.json"
	DefaultGRPCPort = 4317
	DefaultHTTPPort = 4318
	// DefaultHealthCheckPort is where the collector's health_check extension listens on an
	// install with the default OTLP ports, and on any configuration written before the port was
	// recorded (see HealthCheckPort).
	DefaultHealthCheckPort = 13133
)

// SystemBaseDir is the single source of truth for the system-mode state directory.
//
// Everything else derives from this, replacing what used to be several independently
// hardcoded copies of the macOS path in the service, diagnostics, collector, and selfupdate
// packages.
func SystemBaseDir() string {
	switch runtime.GOOS {
	case "linux":
		return LinuxSystemBaseDir
	case "windows":
		// %ProgramData% is the documented home for machine-wide application state that outlives
		// any one user, and is the closest equivalent to both /Library/Application Support and
		// /etc. Read from the environment rather than hardcoded because a redirected or localized
		// install puts it elsewhere, and writing to the wrong root would be silent.
		//
		// Windows is a named case rather than sharing the default: before it was, an endpoint
		// there resolved its state directory to the *macOS* path, so the log directory derived
		// from it came out as "\Library\Application Support\...". Nothing would have failed
		// loudly; it would simply have written machine state to a directory no Windows tool,
		// installer or admin looks in.
		if programData := os.Getenv("ProgramData"); programData != "" {
			return filepath.Join(programData, "Beacon", "Endpoint")
		}
		return filepath.Join(WindowsSystemBaseFallback, "Beacon", "Endpoint")
	default:
		return DarwinSystemBaseDir
	}
}

// SystemConfigPath is the system-mode config file location.
func SystemConfigPath() string {
	return filepath.Join(SystemBaseDir(), "config.json")
}

// SystemLogDir is the single source of truth for where a system-mode endpoint writes its logs.
//
// The runtime log path was previously a literal repeated in fourteen places -- every destination
// pack, the writer, the hook adapter's default, the inventory heartbeat and the self-updater --
// which is exactly the shape SystemBaseDir was introduced to remove for the config directory. One
// function is what makes a second platform possible at all: fourteen literals cannot disagree with
// each other on Linux, but they cannot be given a Windows value either.
//
// The Linux and macOS value is unchanged, and deliberately so. /var/log/beacon-agent is where
// installed endpoints already write, where the packaging scripts create directories, and what the
// forwarder packs tail; moving it would be a migration, not a refactor.
func SystemLogDir() string {
	switch runtime.GOOS {
	case "windows":
		// Alongside the config rather than under a separate root. Windows has no /var/log
		// equivalent, and %ProgramData% is the documented home for machine-wide application state
		// that outlives any one user.
		return filepath.Join(SystemBaseDir(), "logs")
	default:
		return "/var/log/beacon-agent"
	}
}

// SystemLogPath is the system-mode runtime log.
func SystemLogPath() string {
	return filepath.Join(SystemLogDir(), "runtime.jsonl")
}

const (
	DefaultSplunkSource     = "beacon-endpoint-agent"
	DefaultSplunkSourcetype = "beacon:endpoint"
	DefaultFalconSource     = "beacon-endpoint-agent"
	DefaultFalconSourcetype = "json"
)

type Config struct {
	UserMode        bool      `json:"user_mode"`
	LogPath         string    `json:"log_path"`
	Collector       Collector `json:"collector"`
	Harnesses       []string  `json:"harnesses"`
	EventCategories []string  `json:"event_categories,omitempty"`
	// ClaudeCaptureModelContext is the --claude-capture-model-context opt-in, recorded so the per-user
	// config repair writes Claude Code's settings the way the install did.
	ClaudeCaptureModelContext bool       `json:"claude_capture_model_context,omitempty"`
	Inventory                 *Inventory `json:"inventory_heartbeat,omitempty"`
	// SessionSync turns on the scheduled job that reads the agent runtimes' own session stores
	// into the runtime log. Off unless set: see SessionSync.
	SessionSync   *SessionSync   `json:"session_sync,omitempty"`
	Destinations  *Destinations  `json:"destinations,omitempty"`
	ManagedIngest *ManagedIngest `json:"managed_ingest,omitempty"`
	AutoUpdate    *AutoUpdate    `json:"auto_update,omitempty"`
	// LogAccess is the system log's access mode: "shared" (empty, the default) or "restricted".
	// Read it through RestrictedLogAccess, which also accounts for user mode.
	LogAccess string `json:"log_access,omitempty"`
}

// RestrictedLogAccess reports whether this endpoint's system log is in the restricted access mode:
// written only by the privileged collector, readable by root (SYSTEM and Administrators on
// Windows) and the beacon-log group, with hooks sending their events over the local ingest socket
// or pipe. It is never true in user mode, whose log is already private.
func (c Config) RestrictedLogAccess() bool {
	return !c.UserMode && strings.EqualFold(strings.TrimSpace(c.LogAccess), "restricted")
}

// ManagedIngest records that this endpoint forwards to Asymptote managed ingest. It holds
// only non-secret identity so status and diagnostics never need the device key, which lives
// in the 0600 secrets file under the asymptote/ directory and is read only by Vector.
type ManagedIngest struct {
	Enabled        bool   `json:"enabled"`
	IngestURL      string `json:"ingest_url,omitempty"`
	DeviceID       string `json:"device_id,omitempty"`
	OrganizationID string `json:"organization_id,omitempty"`
	PrivacyMode    string `json:"privacy_mode,omitempty"`
}

// AutoUpdate controls Beacon's endpoint update checker. Phase 1 supports
// check-only/off behavior; an empty mode uses the built-in default of off.
type AutoUpdate struct {
	Mode string `json:"mode,omitempty"`
}

type Collector struct {
	BinaryPath string `json:"binary_path,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`
	GRPCPort   int    `json:"grpc_port"`
	HTTPPort   int    `json:"http_port"`
	// HealthPort is the collector health_check extension's loopback port. Zero, which is what a
	// config.json written before the field existed decodes to, means DefaultHealthCheckPort:
	// that is the port such an install rendered into its collector config. Read it through
	// HealthCheckPort rather than directly.
	HealthPort            int    `json:"health_port,omitempty"`
	SpoolPath             string `json:"spool_path,omitempty"`
	IncludeRuntimeMetrics bool   `json:"include_runtime_metrics,omitempty"`
	IncludeCodexSpans     bool   `json:"include_codex_spans,omitempty"`
}

// SessionSync is the `session_sync` block. Unlike the inventory heartbeat it is opt-in: the job
// re-reads session stores that hooks and OTLP already capture live, so turning it on adds poll
// events (`harness.collection_method=poll`) alongside the live ones, which a deployment that
// forwards the runtime log elsewhere chooses deliberately.
type SessionSync struct {
	Enabled bool `json:"enabled"`
}

// SessionSyncEnabled reports whether cfg turns the scheduled session sync on.
func SessionSyncEnabled(cfg Config) bool {
	return cfg.SessionSync != nil && cfg.SessionSync.Enabled
}

// Inventory is the `inventory_heartbeat` block. `enabled` nil means enabled; it governs both the
// scheduled job and the writer. A `ttl_seconds` key written by older versions is ignored: the
// heartbeat runs on the scheduler's interval, not a TTL.
type Inventory struct {
	Enabled         *bool    `json:"enabled,omitempty"`
	Runtimes        []string `json:"runtimes,omitempty"`
	IncludeContents *bool    `json:"include_contents,omitempty"`
	MaxContentBytes int      `json:"max_content_bytes,omitempty"`
}

type InventorySettings struct {
	Enabled         bool
	Runtimes        []string
	IncludeContents bool
	MaxContentBytes int
}

type Destinations struct {
	SplunkHEC *SplunkHEC `json:"splunk_hec,omitempty"`
	FalconHEC *FalconHEC `json:"falcon_hec,omitempty"`
	// OTLPForward sends the OTLP the local collector receives on to a second OTLP endpoint,
	// unchanged, alongside Beacon's own pipeline. It exists for machines whose runtimes already
	// exported to another OTLP backend before Beacon took over their export settings.
	OTLPForward *OTLPForward `json:"otlp_forward,omitempty"`
}

// OTLP forwarding protocols.
const (
	OTLPForwardProtocolGRPC = "grpc"
	OTLPForwardProtocolHTTP = "http/protobuf"
)

type OTLPForward struct {
	Enabled bool `json:"enabled,omitempty"`
	// Endpoint is the receiving collector: host:port or a URL for gRPC, the base URL for HTTP
	// (the exporter appends /v1/logs, /v1/traces and /v1/metrics).
	Endpoint string `json:"endpoint,omitempty"`
	// Protocol is "grpc" (the default) or "http/protobuf".
	Protocol string `json:"protocol,omitempty"`
	// Harnesses are the canonical harness names whose OTLP is forwarded; empty means Claude Code
	// and Codex (DefaultOTLPForwardHarnesses). Every other source the collector receives, such as
	// the browser extension's chats, stays local.
	Harnesses []string `json:"harnesses,omitempty"`
	// Headers are sent with every export, typically an authorization header the backend issued.
	// Their values are credentials, so they are kept out of config.json (which ordinary accounts
	// read to find the system log) and stored in the root-only OTLPForwardSecretsPath; config.json
	// carries only HeaderNames.
	Headers     map[string]string `json:"-"`
	HeaderNames []string          `json:"header_names,omitempty"`
	// HeadersMissing is set by Load when config.json names headers but their file is gone (a
	// package removal or a manual cleanup took it). The forward would then send no credentials,
	// so status reports it and install and repair warn.
	HeadersMissing     bool   `json:"-"`
	Insecure           bool   `json:"insecure,omitempty"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify,omitempty"`
	CAFile             string `json:"ca_file,omitempty"`
}

// DefaultOTLPForwardHarnesses are the runtimes a forward carries when none are named: the two
// whose OTLP export Beacon takes over.
var DefaultOTLPForwardHarnesses = []string{"claude_code", "codex_cli", "codex_desktop"}

// OTLPForwardSecretsPath holds the forward's header values, beside config.json and 0600.
func OTLPForwardSecretsPath(userMode bool) string {
	return filepath.Join(filepath.Dir(ConfigPath(userMode)), "otlp-forward-headers.json")
}

type otlpForwardSecrets struct {
	Headers map[string]string `json:"headers"`
}

// loadOTLPForwardSecrets fills in the forward's header values. An account that cannot read the
// secrets file (anyone but root, for a system install) gets the forward without them, which is
// all status and the runtime-log lookup need; only root renders the collector config.
func loadOTLPForwardSecrets(cfg *Config) {
	if cfg.Destinations == nil || cfg.Destinations.OTLPForward == nil || len(cfg.Destinations.OTLPForward.HeaderNames) == 0 {
		return
	}
	data, err := os.ReadFile(OTLPForwardSecretsPath(cfg.UserMode))
	if os.IsNotExist(err) {
		cfg.Destinations.OTLPForward.HeadersMissing = true
		return
	}
	if err != nil {
		return
	}
	var secrets otlpForwardSecrets
	if json.Unmarshal(data, &secrets) == nil && len(secrets.Headers) > 0 {
		cfg.Destinations.OTLPForward.Headers = secrets.Headers
	}
}

// saveOTLPForwardSecrets writes the forward's header values to their root-only file, or removes
// the file when the forward has none.
func saveOTLPForwardSecrets(cfg Config) error {
	path := OTLPForwardSecretsPath(cfg.UserMode)
	if cfg.Destinations == nil || cfg.Destinations.OTLPForward == nil || !cfg.Destinations.OTLPForward.Enabled || len(cfg.Destinations.OTLPForward.Headers) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(otlpForwardSecrets{Headers: cfg.Destinations.OTLPForward.Headers}, "", "  ")
	if err != nil {
		return err
	}
	return writeSecretFile(cfg.UserMode, path, data)
}

// RewriteSystemConfig replaces the system config.json with data, for the callers that edit its raw
// JSON in place (the auto-update mode) rather than going through Save. It is written the way Save
// would write it: SYSTEM and Administrators only on Windows, 0600 on POSIX, when it holds a
// credential or was already private, and otherwise readable like any config.json. Writing it with
// atomicfile.Write alone gave a Windows file the tree's DACL, so every user could read a HEC token.
func RewriteSystemConfig(path string, data []byte, wasPrivate bool) error {
	return RewriteConfig(false, path, data, wasPrivate)
}

// RewriteConfig is RewriteSystemConfig for either mode: a private user config is written 0600.
func RewriteConfig(userMode bool, path string, data []byte, wasPrivate bool) error {
	if wasPrivate || rawConfigHoldsCredential(data) {
		return writeSecretFile(userMode, path, data)
	}
	return atomicfile.Write(path, data, 0644)
}

// rawConfigHoldsCredential reports whether config.json text holds a HEC token. It reads only the
// token fields, so a field elsewhere of an unexpected type (a hand-edited "grpc_port": "4317")
// cannot hide one, and text it cannot parse at all counts as holding one: the private write is the
// safe one to fall back to.
func rawConfigHoldsCredential(data []byte) bool {
	var partial struct {
		Destinations struct {
			SplunkHEC struct {
				Token json.RawMessage `json:"token"`
			} `json:"splunk_hec"`
			FalconHEC struct {
				Token json.RawMessage `json:"token"`
			} `json:"falcon_hec"`
		} `json:"destinations"`
	}
	if err := json.Unmarshal(data, &partial); err != nil {
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) {
			return true
		}
	}
	present := func(raw json.RawMessage) bool {
		text := strings.TrimSpace(string(raw))
		return text != "" && text != "null" && text != `""`
	}
	return present(partial.Destinations.SplunkHEC.Token) || present(partial.Destinations.FalconHEC.Token)
}

// writeSecretFile writes a file holding a credential. A system install's files sit in the
// machine-wide tree, whose Windows DACL lets every user read what is below it and ignores the
// mode, so they are created with SYSTEM and Administrators only (sysacl.WriteSecretFile); per-user
// files are in the person's own profile and keep the mode.
func writeSecretFile(userMode bool, path string, data []byte) error {
	if userMode {
		return atomicfile.Write(path, data, 0600)
	}
	return sysacl.WriteSecretFile(path, data)
}

type SplunkHEC struct {
	Enabled            bool   `json:"enabled,omitempty"`
	Endpoint           string `json:"endpoint,omitempty"`
	Token              string `json:"token,omitempty"`
	Index              string `json:"index,omitempty"`
	Source             string `json:"source,omitempty"`
	Sourcetype         string `json:"sourcetype,omitempty"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify,omitempty"`
	CAFile             string `json:"ca_file,omitempty"`
}

type FalconHEC struct {
	Enabled            bool   `json:"enabled,omitempty"`
	Endpoint           string `json:"endpoint,omitempty"`
	Token              string `json:"token,omitempty"`
	Index              string `json:"index,omitempty"`
	Source             string `json:"source,omitempty"`
	Sourcetype         string `json:"sourcetype,omitempty"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify,omitempty"`
	CAFile             string `json:"ca_file,omitempty"`
}

func Default(userMode bool, logPath string) Config {
	base := BaseDir(userMode)
	return Config{
		UserMode:  userMode,
		LogPath:   logPath,
		Harnesses: []string{"claude", "codex"},
		Collector: Collector{
			ConfigPath: filepath.Join(base, "otelcol.yaml"),
			GRPCPort:   DefaultGRPCPort,
			HTTPPort:   DefaultHTTPPort,
			SpoolPath:  filepath.Join(base, "spool", "otlp.jsonl"),
		},
	}
}

// HealthCheckPort is the port the collector's health_check extension listens on for this config.
//
// A config with no recorded port predates the field, and every such install rendered the fixed
// DefaultHealthCheckPort into its collector config, so that is the answer for it; deriving one
// here instead would point status at a port the running collector never bound.
func HealthCheckPort(collector Collector) int {
	if collector.HealthPort > 0 {
		return collector.HealthPort
	}
	return DefaultHealthCheckPort
}

// DeriveHealthCheckPort picks the health-check port for an install that did not name one.
//
// The port moves with the OTLP HTTP port: it is DefaultHealthCheckPort shifted by however far the
// HTTP port was moved from DefaultHTTPPort. The default install therefore keeps 13133, and a second
// collector installed on alternate OTLP ports -- a user-mode endpoint beside the system one, which
// is the case that needs different ports at all -- gets a different health port without being
// told (4328 gives 13143). The fixed port was why those two could not run together (#447).
//
// When that shift leaves the valid range or lands on an OTLP port, the next free-looking neighbour
// of the HTTP port is used instead. The result is a pure function of the two ports, so install,
// repair and status agree without consulting anything else.
func DeriveHealthCheckPort(grpcPort, httpPort int) int {
	if grpcPort <= 0 {
		grpcPort = DefaultGRPCPort
	}
	if httpPort <= 0 {
		httpPort = DefaultHTTPPort
	}
	candidates := []int{
		DefaultHealthCheckPort + (httpPort - DefaultHTTPPort),
		httpPort + 1,
		httpPort - 1,
		httpPort + 2,
		httpPort - 2,
	}
	for _, port := range candidates {
		if validPort(port) && port != grpcPort && port != httpPort {
			return port
		}
	}
	return DefaultHealthCheckPort
}

// ValidateCollectorPorts rejects out-of-range or shared collector endpoints before install writes
// a configuration the collector cannot bind. A zero health port keeps the legacy default.
func ValidateCollectorPorts(collector Collector) error {
	if !validPort(collector.GRPCPort) {
		return fmt.Errorf("collector OTLP gRPC port %d is out of range; choose a port between 1 and 65535 with --otlp-grpc-port", collector.GRPCPort)
	}
	if !validPort(collector.HTTPPort) {
		return fmt.Errorf("collector OTLP HTTP port %d is out of range; choose a port between 1 and 65535 with --otlp-http-port", collector.HTTPPort)
	}
	if collector.GRPCPort == collector.HTTPPort {
		return fmt.Errorf("collector OTLP gRPC and HTTP ports are both %d; choose different --otlp-grpc-port and --otlp-http-port values", collector.GRPCPort)
	}
	health := HealthCheckPort(collector)
	if collector.HealthPort < 0 || !validPort(health) {
		return fmt.Errorf("collector health-check port %d is out of range; choose a port between 1 and 65535 with --health-port", collector.HealthPort)
	}
	if health == collector.GRPCPort || health == collector.HTTPPort {
		return fmt.Errorf("collector health-check port %d is also an OTLP receiver port; choose a different --health-port", health)
	}
	return nil
}

func validPort(port int) bool {
	return port > 0 && port <= 65535
}

func InventoryDefaults() InventorySettings {
	return InventorySettings{
		Enabled:  true,
		Runtimes: []string{},
	}
}

func InventoryConfig(cfg Config) InventorySettings {
	settings := InventoryDefaults()
	if cfg.Inventory == nil {
		return settings
	}
	if cfg.Inventory.Enabled != nil {
		settings.Enabled = *cfg.Inventory.Enabled
	}
	if len(cfg.Inventory.Runtimes) > 0 {
		settings.Runtimes = append([]string(nil), cfg.Inventory.Runtimes...)
	}
	if cfg.Inventory.IncludeContents != nil {
		settings.IncludeContents = *cfg.Inventory.IncludeContents
	}
	if cfg.Inventory.MaxContentBytes > 0 {
		settings.MaxContentBytes = cfg.Inventory.MaxContentBytes
	}
	return settings
}

func BaseDir(userMode bool) string {
	if userMode {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".", ".beacon", "endpoint")
		}
		return filepath.Join(home, ".beacon", "endpoint")
	}
	return SystemBaseDir()
}

// HistoryStoreFile is the durable local history `beacon endpoint traces` reads once the user
// opts in to it.
const HistoryStoreFile = "history.db"

// HistoryStoreEnv overrides where the local history lives.
const HistoryStoreEnv = "BEACON_HISTORY_DB"

// HistoryStorePath is where the local history lives: BEACON_HISTORY_DB when set, otherwise the
// user's own endpoint directory, whichever log it indexes. The system log's directory is root's,
// and the history holds the same prompts and commands the log does, so it belongs to the person
// reading it.
func HistoryStorePath() string {
	if path := strings.TrimSpace(os.Getenv(HistoryStoreEnv)); path != "" {
		return path
	}
	return filepath.Join(BaseDir(true), HistoryStoreFile)
}

func ConfigPath(userMode bool) string {
	if userMode {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".", UserConfigPath)
		}
		return filepath.Join(home, UserConfigPath)
	}
	return SystemConfigPath()
}

func Load(userMode bool) (Config, error) {
	path := ConfigPath(userMode)
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	loadOTLPForwardSecrets(&cfg)
	NormalizeDestinations(&cfg)
	if err := ValidateDestinations(cfg.Destinations); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Save(cfg Config) (string, error) {
	NormalizeDestinations(&cfg)
	if err := ValidateDestinations(cfg.Destinations); err != nil {
		return "", err
	}
	path := ConfigPath(cfg.UserMode)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	// The forward's header values live in their own root-only file, so a forward alone leaves
	// config.json readable to the accounts that look up the system log through it.
	if err := saveOTLPForwardSecrets(cfg); err != nil {
		return "", err
	}
	if hasSecretConfigFields(cfg) {
		if err := writeSecretFile(cfg.UserMode, path, data); err != nil {
			return "", err
		}
		return path, nil
	}
	if err := atomicfile.Write(path, data, 0644); err != nil {
		return "", err
	}
	return path, nil
}

func NormalizeDestinations(cfg *Config) {
	if cfg == nil || cfg.Destinations == nil {
		return
	}
	if splunk := cfg.Destinations.SplunkHEC; splunk != nil {
		if splunk.Endpoint != "" || splunk.Token != "" {
			splunk.Enabled = true
		}
		if splunk.Enabled {
			if splunk.Source == "" {
				splunk.Source = DefaultSplunkSource
			}
			if splunk.Sourcetype == "" {
				splunk.Sourcetype = DefaultSplunkSourcetype
			}
		}
	}
	if forward := cfg.Destinations.OTLPForward; forward != nil {
		forward.Endpoint = strings.TrimSpace(forward.Endpoint)
		forward.Protocol = strings.ToLower(strings.TrimSpace(forward.Protocol))
		switch forward.Protocol {
		case "http", "http/protobuf", "otlphttp":
			forward.Protocol = OTLPForwardProtocolHTTP
		}
		if forward.Endpoint != "" {
			forward.Enabled = true
		}
		if forward.Enabled && forward.Protocol == "" {
			forward.Protocol = OTLPForwardProtocolGRPC
		}
		if forward.Enabled && len(forward.Harnesses) == 0 {
			forward.Harnesses = append([]string(nil), DefaultOTLPForwardHarnesses...)
		}
		if forward.Headers != nil {
			forward.HeaderNames = forward.HeaderNames[:0:0]
			for name := range forward.Headers {
				forward.HeaderNames = append(forward.HeaderNames, name)
			}
			sort.Strings(forward.HeaderNames)
		}
	}
	if falcon := cfg.Destinations.FalconHEC; falcon != nil {
		if falcon.Endpoint != "" || falcon.Token != "" {
			falcon.Enabled = true
		}
		if falcon.Enabled {
			if falcon.Source == "" {
				falcon.Source = DefaultFalconSource
			}
			if falcon.Sourcetype == "" {
				falcon.Sourcetype = DefaultFalconSourcetype
			}
		}
	}
}

func ValidateDestinations(destinations *Destinations) error {
	if destinations == nil {
		return nil
	}
	if forward := destinations.OTLPForward; forward != nil {
		configured := forward.Enabled ||
			forward.Endpoint != "" ||
			len(forward.Headers) > 0 ||
			forward.Insecure ||
			forward.InsecureSkipVerify ||
			forward.CAFile != ""
		if configured {
			if strings.TrimSpace(forward.Endpoint) == "" {
				return fmt.Errorf("OTLP forward endpoint is required when OTLP forwarding is configured")
			}
			switch forward.Protocol {
			case "", OTLPForwardProtocolGRPC, OTLPForwardProtocolHTTP:
			default:
				return fmt.Errorf("unsupported OTLP forward protocol %q: use %q or %q", forward.Protocol, OTLPForwardProtocolGRPC, OTLPForwardProtocolHTTP)
			}
			if forward.Protocol == OTLPForwardProtocolHTTP && !strings.HasPrefix(forward.Endpoint, "https://") && !strings.HasPrefix(forward.Endpoint, "http://") {
				return fmt.Errorf("OTLP forward endpoint %q must be an http:// or https:// URL for the %s protocol", forward.Endpoint, OTLPForwardProtocolHTTP)
			}
			for name, value := range forward.Headers {
				if strings.TrimSpace(name) == "" || strings.ContainsAny(name, ":\r\n") {
					return fmt.Errorf("invalid OTLP forward header name %q", name)
				}
				if strings.ContainsAny(value, "\r\n") {
					return fmt.Errorf("OTLP forward header %q has a line break in its value", name)
				}
			}
		}
	}
	if destinations.SplunkHEC != nil {
		splunk := destinations.SplunkHEC
		configured := splunk.Enabled ||
			splunk.Endpoint != "" ||
			splunk.Token != "" ||
			splunk.Index != "" ||
			splunk.Source != "" ||
			splunk.Sourcetype != "" ||
			splunk.InsecureSkipVerify ||
			splunk.CAFile != ""
		if configured {
			if splunk.Endpoint == "" {
				return fmt.Errorf("splunk HEC endpoint is required when Splunk forwarding is configured")
			}
			if splunk.Token == "" {
				return fmt.Errorf("splunk HEC token is required when Splunk forwarding is configured")
			}
		}
	}
	if destinations.FalconHEC != nil {
		falcon := destinations.FalconHEC
		configured := falcon.Enabled ||
			falcon.Endpoint != "" ||
			falcon.Token != "" ||
			falcon.Index != "" ||
			falcon.Source != "" ||
			falcon.Sourcetype != "" ||
			falcon.InsecureSkipVerify ||
			falcon.CAFile != ""
		if configured {
			if falcon.Endpoint == "" {
				return fmt.Errorf("falcon HEC endpoint is required when Falcon forwarding is configured")
			}
			if falcon.Token == "" {
				return fmt.Errorf("falcon HEC token is required when Falcon forwarding is configured")
			}
		}
	}
	return nil
}

func HasSecretDestinations(cfg Config) bool {
	return cfg.Destinations != nil &&
		((cfg.Destinations.SplunkHEC != nil && cfg.Destinations.SplunkHEC.Token != "") ||
			(cfg.Destinations.FalconHEC != nil && cfg.Destinations.FalconHEC.Token != "") ||
			(cfg.Destinations.OTLPForward != nil && len(cfg.Destinations.OTLPForward.Headers) > 0))
}

// hasSecretConfigFields reports whether config.json itself holds a credential. The forward's
// header values are not in it (see OTLPForwardSecretsPath), so they do not count here; they do
// for the collector config, which renders them (HasSecretDestinations).
func hasSecretConfigFields(cfg Config) bool {
	return cfg.Destinations != nil &&
		((cfg.Destinations.SplunkHEC != nil && cfg.Destinations.SplunkHEC.Token != "") ||
			(cfg.Destinations.FalconHEC != nil && cfg.Destinations.FalconHEC.Token != ""))
}
