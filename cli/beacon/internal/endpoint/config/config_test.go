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
	if cfg.ContentRetention != ContentRetentionMetadata {
		t.Fatalf("ContentRetention = %q, want %q", cfg.ContentRetention, ContentRetentionMetadata)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	logPath := filepath.Join(home, "logs", "runtime.jsonl")

	cfg := Default(true, logPath)
	cfg.Collector.BinaryPath = filepath.Join(home, "bin", "otelcol")
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
	if len(loaded.EventCategories) != 2 || loaded.EventCategories[1] != "session" {
		t.Fatalf("EventCategories did not round-trip: %#v", loaded.EventCategories)
	}
	if loaded.ContentRetention != ContentRetentionRedacted {
		t.Fatalf("ContentRetention = %q, want %q", loaded.ContentRetention, ContentRetentionRedacted)
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
