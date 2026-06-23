package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	SystemConfigPath = "/Library/Application Support/Beacon/Endpoint/config.json"
	UserConfigPath   = ".beacon/endpoint/config.json"
	DefaultGRPCPort  = 4317
	DefaultHTTPPort  = 4318
)

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

// AutoUpdate controls the built-in self-updater. Mode is one of "auto"
// (check and apply), "check-only" (surface but do not apply), or "off"
// (no background updater). An empty mode means use the built-in default.
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
	Enabled    *bool    `json:"enabled,omitempty"`
	TTLSeconds int      `json:"ttl_seconds,omitempty"`
	Runtimes   []string `json:"runtimes,omitempty"`
}

type InventorySettings struct {
	Enabled    bool
	TTLSeconds int
	Runtimes   []string
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
		Runtimes:   []string{"cursor", "claude_code"},
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
	return "/Library/Application Support/Beacon/Endpoint"
}

func ConfigPath(userMode bool) string {
	if userMode {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".", UserConfigPath)
		}
		return filepath.Join(home, UserConfigPath)
	}
	return SystemConfigPath
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
