package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultUserConfigUsesHomeScopedPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := Default(true, filepath.Join(home, "runtime.jsonl"))

	if !cfg.UserMode {
		t.Fatal("expected user mode config")
	}
	if cfg.Collector.GRPCPort != DefaultGRPCPort || cfg.Collector.HTTPPort != DefaultHTTPPort {
		t.Fatalf("unexpected ports: grpc=%d http=%d", cfg.Collector.GRPCPort, cfg.Collector.HTTPPort)
	}
	if got, want := cfg.Collector.ConfigPath, filepath.Join(home, ".beacon", "endpoint", "otelcol.yaml"); got != want {
		t.Fatalf("ConfigPath = %q, want %q", got, want)
	}
	if got, want := cfg.Collector.SpoolPath, filepath.Join(home, ".beacon", "endpoint", "spool", "otlp.jsonl"); got != want {
		t.Fatalf("SpoolPath = %q, want %q", got, want)
	}
	if len(cfg.Harnesses) != 2 || cfg.Harnesses[0] != "claude" || cfg.Harnesses[1] != "codex" {
		t.Fatalf("unexpected default harnesses: %#v", cfg.Harnesses)
	}
	if cfg.ContentRetention != ContentRetentionFull {
		t.Fatalf("ContentRetention = %q, want %q", cfg.ContentRetention, ContentRetentionFull)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	logPath := filepath.Join(home, "logs", "runtime.jsonl")

	cfg := Default(true, logPath)
	cfg.Collector.BinaryPath = filepath.Join(home, "bin", "otelcol")
	cfg.Collector.IncludeRuntimeMetrics = true
	cfg.EventCategories = []string{"tool", "session"}
	cfg.ContentRetention = ContentRetentionRedacted

	path, err := Save(cfg)
	if err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	if got, want := path, filepath.Join(home, UserConfigPath); got != want {
		t.Fatalf("Save path = %q, want %q", got, want)
	}

	loaded, err := Load(true)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.LogPath != logPath {
		t.Fatalf("LogPath = %q, want %q", loaded.LogPath, logPath)
	}
	if loaded.Collector.BinaryPath != cfg.Collector.BinaryPath {
		t.Fatalf("BinaryPath = %q, want %q", loaded.Collector.BinaryPath, cfg.Collector.BinaryPath)
	}
	if !loaded.Collector.IncludeRuntimeMetrics {
		t.Fatal("IncludeRuntimeMetrics = false, want true")
	}
	if len(loaded.EventCategories) != 2 || loaded.EventCategories[1] != "session" {
		t.Fatalf("EventCategories did not round-trip: %#v", loaded.EventCategories)
	}
	if loaded.ContentRetention != ContentRetentionRedacted {
		t.Fatalf("ContentRetention = %q, want %q", loaded.ContentRetention, ContentRetentionRedacted)
	}
}

func TestSaveLoadSplunkHECRoundTripAndPrivatePermissions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := Default(true, filepath.Join(home, "logs", "runtime.jsonl"))
	cfg.Destinations = &Destinations{SplunkHEC: &SplunkHEC{
		Endpoint: "https://splunk.example:8088/services/collector",
		Token:    "hec-token",
		Index:    "beacon",
	}}

	path, err := Save(cfg)
	if err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0600); got != want {
		t.Fatalf("config permissions = %o, want %o", got, want)
	}

	loaded, err := Load(true)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	splunk := loaded.Destinations.SplunkHEC
	if !splunk.Enabled {
		t.Fatal("SplunkHEC.Enabled = false, want true")
	}
	if splunk.Source != DefaultSplunkSource || splunk.Sourcetype != DefaultSplunkSourcetype {
		t.Fatalf("Splunk defaults = source %q sourcetype %q", splunk.Source, splunk.Sourcetype)
	}
	if splunk.Token != "hec-token" || splunk.Index != "beacon" {
		t.Fatalf("Splunk config did not round-trip: %#v", splunk)
	}
}

func TestSaveRejectsIncompleteSplunkHEC(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := Default(true, filepath.Join(home, "runtime.jsonl"))
	cfg.Destinations = &Destinations{SplunkHEC: &SplunkHEC{Endpoint: "https://splunk.example:8088/services/collector"}}

	if _, err := Save(cfg); err == nil {
		t.Fatal("expected missing Splunk token error")
	}
}

func TestLoadDefaultsMissingContentRetentionToFull(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, UserConfigPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"user_mode":true}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	loaded, err := Load(true)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.ContentRetention != ContentRetentionFull {
		t.Fatalf("ContentRetention = %q, want %q", loaded.ContentRetention, ContentRetentionFull)
	}
}

func TestLoadRejectsCorruptJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, UserConfigPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0644); err != nil {
		t.Fatalf("write corrupt config: %v", err)
	}

	if _, err := Load(true); err == nil {
		t.Fatal("expected corrupt JSON error")
	}
}
