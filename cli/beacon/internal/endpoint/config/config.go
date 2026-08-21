package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	UserMode        bool           `json:"user_mode"`
	LogPath         string         `json:"log_path"`
	Collector       Collector      `json:"collector"`
	Harnesses       []string       `json:"harnesses"`
	EventCategories []string       `json:"event_categories,omitempty"`
	Inventory       *Inventory     `json:"inventory_heartbeat,omitempty"`
	Destinations    *Destinations  `json:"destinations,omitempty"`
	ManagedUpload   *ManagedUpload `json:"managed_upload,omitempty"`
	AutoUpdate      *AutoUpdate    `json:"auto_update,omitempty"`
}

// AutoUpdate controls Beacon's endpoint update checker. Phase 1 supports
// check-only/off behavior; an empty mode uses the built-in default of off.
type AutoUpdate struct {
	Mode string `json:"mode,omitempty"`
}

type Collector struct {
	BinaryPath            string `json:"binary_path,omitempty"`
	ConfigPath            string `json:"config_path,omitempty"`
	GRPCPort              int    `json:"grpc_port"`
	HTTPPort              int    `json:"http_port"`
	SpoolPath             string `json:"spool_path,omitempty"`
	IncludeRuntimeMetrics bool   `json:"include_runtime_metrics,omitempty"`
	IncludeCodexSpans     bool   `json:"include_codex_spans,omitempty"`
}

type Inventory struct {
	Enabled         *bool    `json:"enabled,omitempty"`
	TTLSeconds      int      `json:"ttl_seconds,omitempty"`
	Runtimes        []string `json:"runtimes,omitempty"`
	IncludeContents *bool    `json:"include_contents,omitempty"`
	MaxContentBytes int      `json:"max_content_bytes,omitempty"`
}

type InventorySettings struct {
	Enabled         bool
	TTLSeconds      int
	Runtimes        []string
	IncludeContents bool
	MaxContentBytes int
}

type Destinations struct {
	SplunkHEC *SplunkHEC `json:"splunk_hec,omitempty"`
	FalconHEC *FalconHEC `json:"falcon_hec,omitempty"`
}

type ManagedUpload struct {
	Enabled          bool   `json:"enabled,omitempty"`
	Managed          bool   `json:"managed,omitempty"`
	IngestURL        string `json:"ingest_url,omitempty"`
	SourceID         string `json:"source_id,omitempty"`
	ContentRetention string `json:"content_retention,omitempty"`
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

func InventoryDefaults() InventorySettings {
	return InventorySettings{
		Enabled:    true,
		TTLSeconds: 24 * 60 * 60,
		Runtimes:   []string{},
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
	if cfg.Inventory.TTLSeconds > 0 {
		settings.TTLSeconds = cfg.Inventory.TTLSeconds
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
	perm := os.FileMode(0644)
	if HasSecretDestinations(cfg) {
		perm = 0600
	}
	if err := os.WriteFile(path, data, perm); err != nil {
		return "", err
	}
	if HasSecretDestinations(cfg) {
		return path, os.Chmod(path, perm)
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
			(cfg.Destinations.FalconHEC != nil && cfg.Destinations.FalconHEC.Token != ""))
}
