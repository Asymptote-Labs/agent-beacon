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
)

type ContentRetention string

const (
	ContentRetentionMetadata ContentRetention = "metadata"
	ContentRetentionRedacted ContentRetention = "redacted"
	ContentRetentionFull     ContentRetention = "full"
)

type Config struct {
	UserMode         bool             `json:"user_mode"`
	LogPath          string           `json:"log_path"`
	Collector        Collector        `json:"collector"`
	Harnesses        []string         `json:"harnesses"`
	EventCategories  []string         `json:"event_categories,omitempty"`
	ContentRetention ContentRetention `json:"content_retention"`
	Destinations     *Destinations    `json:"destinations,omitempty"`
}

type Collector struct {
	BinaryPath string `json:"binary_path,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`
	GRPCPort   int    `json:"grpc_port"`
	HTTPPort   int    `json:"http_port"`
	SpoolPath  string `json:"spool_path,omitempty"`
}

type Destinations struct {
	SplunkHEC *SplunkHEC `json:"splunk_hec,omitempty"`
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

func Default(userMode bool, logPath string) Config {
	base := BaseDir(userMode)
	return Config{
		UserMode:         userMode,
		LogPath:          logPath,
		Harnesses:        []string{"claude", "codex"},
		ContentRetention: ContentRetentionFull,
		Collector: Collector{
			ConfigPath: filepath.Join(base, "otelcol.yaml"),
			GRPCPort:   DefaultGRPCPort,
			HTTPPort:   DefaultHTTPPort,
			SpoolPath:  filepath.Join(base, "spool", "otlp.jsonl"),
		},
	}
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
	if cfg.ContentRetention == "" {
		cfg.ContentRetention = ContentRetentionFull
	}
	if err := ValidateContentRetention(cfg.ContentRetention); err != nil {
		return Config{}, err
	}
	NormalizeDestinations(&cfg)
	if err := ValidateDestinations(cfg.Destinations); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Save(cfg Config) (string, error) {
	if cfg.ContentRetention == "" {
		cfg.ContentRetention = ContentRetentionFull
	}
	if err := ValidateContentRetention(cfg.ContentRetention); err != nil {
		return "", err
	}
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

func ValidateContentRetention(mode ContentRetention) error {
	switch mode {
	case "", ContentRetentionMetadata, ContentRetentionRedacted, ContentRetentionFull:
		return nil
	default:
		return fmt.Errorf("content retention must be metadata, redacted, or full")
	}
}

func NormalizeDestinations(cfg *Config) {
	if cfg == nil || cfg.Destinations == nil || cfg.Destinations.SplunkHEC == nil {
		return
	}
	splunk := cfg.Destinations.SplunkHEC
	if splunk.Endpoint != "" || splunk.Token != "" {
		splunk.Enabled = true
	}
	if !splunk.Enabled {
		return
	}
	if splunk.Source == "" {
		splunk.Source = DefaultSplunkSource
	}
	if splunk.Sourcetype == "" {
		splunk.Sourcetype = DefaultSplunkSourcetype
	}
}

func ValidateDestinations(destinations *Destinations) error {
	if destinations == nil || destinations.SplunkHEC == nil {
		return nil
	}
	splunk := destinations.SplunkHEC
	configured := splunk.Enabled ||
		splunk.Endpoint != "" ||
		splunk.Token != "" ||
		splunk.Index != "" ||
		splunk.Source != "" ||
		splunk.Sourcetype != "" ||
		splunk.InsecureSkipVerify ||
		splunk.CAFile != ""
	if !configured {
		return nil
	}
	if splunk.Endpoint == "" {
		return fmt.Errorf("splunk HEC endpoint is required when Splunk forwarding is configured")
	}
	if splunk.Token == "" {
		return fmt.Errorf("splunk HEC token is required when Splunk forwarding is configured")
	}
	return nil
}

func HasSecretDestinations(cfg Config) bool {
	return cfg.Destinations != nil &&
		cfg.Destinations.SplunkHEC != nil &&
		cfg.Destinations.SplunkHEC.Token != ""
}
