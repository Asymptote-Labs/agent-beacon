package beaconingestextension

import (
	"errors"
	"fmt"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
)

const (
	defaultRotateBytes    = 10 * 1024 * 1024
	defaultRotateArchives = 5
)

// Config is the beacon_ingest extension's configuration. Install renders it only for a system-mode
// endpoint in the restricted log access mode, with the same log path and rotation the beaconjson
// exporter uses.
type Config struct {
	// SocketPath is where hooks connect. Empty means localingest.DefaultSocketPath.
	SocketPath string `mapstructure:"socket_path"`
	// LogPath is the runtime log the events are appended to.
	LogPath        string `mapstructure:"log_path"`
	RotateBytes    int64  `mapstructure:"rotate_bytes"`
	RotateArchives int    `mapstructure:"rotate_archives"`
	// MaxConnections bounds concurrent connections; zero is the localingest default.
	MaxConnections int `mapstructure:"max_connections"`
	// MaxConnectionsPerUID bounds one account's concurrent connections; zero is the localingest
	// default.
	MaxConnectionsPerUID int `mapstructure:"max_connections_per_uid"`
	// RateBytesPerSecond and RateBurstBytes bound the bytes each account can write; zero is the
	// localingest default and a negative rate turns the bound off.
	RateBytesPerSecond float64 `mapstructure:"rate_bytes_per_second"`
	RateBurstBytes     float64 `mapstructure:"rate_burst_bytes"`
}

func createDefaultConfig() *Config {
	return &Config{
		SocketPath:     localingest.DefaultSocketPath,
		RotateBytes:    defaultRotateBytes,
		RotateArchives: defaultRotateArchives,
	}
}

// Validate checks the configuration.
func (c *Config) Validate() error {
	if c.LogPath == "" {
		return errors.New("log_path is required")
	}
	if c.RotateBytes < 0 || c.RotateArchives < 0 || c.MaxConnections < 0 || c.MaxConnectionsPerUID < 0 || c.RateBurstBytes < 0 {
		return errors.New("rotate_bytes, rotate_archives, max_connections, max_connections_per_uid and rate_burst_bytes must not be negative")
	}
	// Each line costs its size plus one against the burst, which is also the most the bucket ever
	// holds: a burst below that could never let a maximum-size event through, and a spool whose
	// oldest line is one would never drain.
	if c.RateBurstBytes > 0 && c.RateBurstBytes < localingest.MaxLineBytes+1 {
		return fmt.Errorf("rate_burst_bytes must be 0 (the default) or at least %d, the cost of one maximum-size event", localingest.MaxLineBytes+1)
	}
	return nil
}
