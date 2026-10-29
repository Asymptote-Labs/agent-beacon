package beaconjsonexporter

import (
	"fmt"
	"path/filepath"
)

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
	ContentRetention      string `mapstructure:"content_retention"` // Deprecated no-op; retained for older collector configs.
	IncludeRuntimeMetrics bool   `mapstructure:"include_runtime_metrics"`
	IncludeCodexSpans     bool   `mapstructure:"include_codex_spans"`
	// CaptureModelContext is the --claude-capture-model-context opt-in, the same setting the
	// claude_api_body processor reads; see beaconevent.Options.
	CaptureModelContext bool `mapstructure:"capture_model_context"`
	// HookSocket, when set, is the Unix socket on which this exporter accepts finished lines from
	// hooks and appends them to Path: restricted log mode, in which hooks cannot write the log
	// themselves. See hooksocket.go.
	HookSocket string `mapstructure:"hook_socket"`
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
	if c.HookSocket != "" && !filepath.IsAbs(c.HookSocket) {
		return fmt.Errorf("hook_socket must be an absolute path")
	}
	return nil
}
