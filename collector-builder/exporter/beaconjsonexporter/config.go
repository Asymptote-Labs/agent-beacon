package beaconjsonexporter

import "fmt"

const (
	defaultMaxEventBytes = 64 * 1024
	defaultRotateBytes   = 10 * 1024 * 1024
)

// Config controls local JSONL export for Beacon endpoint events.
type Config struct {
	Path                  string `mapstructure:"path"`
	MaxEventBytes         int    `mapstructure:"max_event_bytes"`
	RotateBytes           int64  `mapstructure:"rotate_bytes"`
	RedactSecrets         bool   `mapstructure:"redact_secrets"`
	ContentRetention      string `mapstructure:"content_retention"`
	IncludeRuntimeMetrics bool   `mapstructure:"include_runtime_metrics"`
}

func createDefaultConfig() *Config {
	return &Config{
		MaxEventBytes:    defaultMaxEventBytes,
		RotateBytes:      defaultRotateBytes,
		RedactSecrets:    true,
		ContentRetention: "full",
	}
}

func (c *Config) Validate() error {
	if c.Path == "" {
		return fmt.Errorf("path is required")
	}
	if c.MaxEventBytes <= 0 {
		return fmt.Errorf("max_event_bytes must be positive")
	}
	if c.RotateBytes < 0 {
		return fmt.Errorf("rotate_bytes cannot be negative")
	}
	switch c.ContentRetention {
	case "", "metadata", "redacted", "full":
		return nil
	default:
		return fmt.Errorf("content_retention must be metadata, redacted, or full")
	}
}
