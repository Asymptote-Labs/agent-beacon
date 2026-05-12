package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const (
	SystemConfigPath = "/Library/Application Support/Beacon/Endpoint/config.json"
	UserConfigPath   = ".beacon/endpoint/config.json"
	DefaultGRPCPort  = 4317
	DefaultHTTPPort  = 4318
)

type Config struct {
	UserMode        bool      `json:"user_mode"`
	LogPath         string    `json:"log_path"`
	Collector       Collector `json:"collector"`
	Harnesses       []string  `json:"harnesses"`
	EventCategories []string  `json:"event_categories,omitempty"`
}

type Collector struct {
	BinaryPath string `json:"binary_path,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`
	GRPCPort   int    `json:"grpc_port"`
	HTTPPort   int    `json:"http_port"`
	SpoolPath  string `json:"spool_path,omitempty"`
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
	return cfg, nil
}

func Save(cfg Config) (string, error) {
	path := ConfigPath(cfg.UserMode)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data, 0644)
}
