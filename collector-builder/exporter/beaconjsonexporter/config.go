package beaconjsonexporter

import "fmt"

const (
	defaultMaxEventBytes  = 64 * 1024
	defaultRotateBytes    = 10 * 1024 * 1024
	defaultRotateArchives = 5
)

// Config controls local JSONL export for Beacon endpoint events.
type Config struct {
	Path                  string `mapstructure:"path"`
	MaxEventBytes         int    `mapstructure:"max_event_bytes"`
	RotateBytes           int64  `mapstructure:"rotate_bytes"`
	RotateArchives        int    `mapstructure:"rotate_archives"`
	RedactSecrets         bool   `mapstructure:"redact_secrets"`
	IncludeRuntimeMetrics bool   `mapstructure:"include_runtime_metrics"`
	IncludeCodexSpans     bool   `mapstructure:"include_codex_spans"`
}

func createDefaultConfig() *Config {
	return &Config{
		MaxEventBytes:  defaultMaxEventBytes,
		RotateBytes:    defaultRotateBytes,
		RotateArchives: defaultRotateArchives,
		RedactSecrets:  true,
	}
}

func (c *Config) Validate() error {
	if c.Path == "" {
		return fmt.Errorf("path is required")
	}
	if c.MaxEventBytes <= 0 {
		return fmt.Errorf("max_event_bytes must be positive")
	}
	return nil
}
