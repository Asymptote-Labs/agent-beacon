package falconhecexporter

import (
	"fmt"
	"net/url"
	"time"

	"go.opentelemetry.io/collector/config/configretry"
	"go.opentelemetry.io/collector/exporter/exporterhelper"
)

const defaultTimeout = 10 * time.Second

type Config struct {
	Endpoint              string                     `mapstructure:"endpoint"`
	Token                 string                     `mapstructure:"token"`
	Source                string                     `mapstructure:"source"`
	Sourcetype            string                     `mapstructure:"sourcetype"`
	Index                 string                     `mapstructure:"index"`
	InsecureSkipVerify    bool                       `mapstructure:"insecure_skip_verify"`
	CAFile                string                     `mapstructure:"ca_file"`
	Timeout               time.Duration              `mapstructure:"timeout"`
	QueueSettings         exporterhelper.QueueConfig `mapstructure:"sending_queue"`
	RetrySettings         configretry.BackOffConfig  `mapstructure:"retry_on_failure"`
	ContentRetention      string                     `mapstructure:"content_retention"`
	IncludeRuntimeMetrics bool                       `mapstructure:"include_runtime_metrics"`
	IncludeCodexSpans     bool                       `mapstructure:"include_codex_spans"`
}

func createDefaultConfig() *Config {
	queue := exporterhelper.NewDefaultQueueConfig()
	queue.QueueSize = 256
	return &Config{
		Timeout:          defaultTimeout,
		QueueSettings:    queue,
		RetrySettings:    configretry.NewDefaultBackOffConfig(),
		ContentRetention: "full",
	}
}

func (c *Config) Validate() error {
	if c.Endpoint == "" {
		return fmt.Errorf("endpoint is required")
	}
	if _, err := url.ParseRequestURI(c.Endpoint); err != nil {
		return fmt.Errorf("endpoint must be a full URL: %w", err)
	}
	if c.Token == "" {
		return fmt.Errorf("token is required")
	}
	if c.Timeout < 0 {
		return fmt.Errorf("timeout must be non-negative")
	}
	if err := c.QueueSettings.Validate(); err != nil {
		return err
	}
	if err := c.RetrySettings.Validate(); err != nil {
		return err
	}
	switch c.ContentRetention {
	case "", "metadata", "redacted", "full":
		return nil
	default:
		return fmt.Errorf("content_retention must be metadata, redacted, or full")
	}
}
