package collector

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

func testConfig(t *testing.T) endpointconfig.Config {
	t.Helper()
	dir := t.TempDir()
	return endpointconfig.Config{
		UserMode: true,
		LogPath:  filepath.Join(dir, "logs", "runtime.jsonl"),
		Collector: endpointconfig.Collector{
			ConfigPath: filepath.Join(dir, "otelcol.yaml"),
			GRPCPort:   14317,
			HTTPPort:   14318,
			SpoolPath:  filepath.Join(dir, "spool", "otlp.jsonl"),
		},
	}
}

func TestConfigYAMLIncludesReleaseContractFields(t *testing.T) {
	cfg := testConfig(t)

	yaml := ConfigYAML(cfg)

	for _, want := range []string{
		"endpoint: 127.0.0.1:14317",
		"endpoint: 127.0.0.1:14318",
		"beaconjson:",
		"path: " + `"` + cfg.LogPath + `"`,
		"max_event_bytes: 65536",
		"rotate_bytes: 10485760",
		"redact_secrets: true",
		"level: none",
		"receivers: [otlp]",
		"exporters: [beaconjson]",
	} {
		if !strings.Contains(yaml, want) {
			t.Fatalf("ConfigYAML missing %q:\n%s", want, yaml)
		}
	}
}

func TestWriteConfigCreatesConfigAndSpoolDirectory(t *testing.T) {
	cfg := testConfig(t)

	if err := WriteConfig(cfg); err != nil {
		t.Fatalf("WriteConfig returned error: %v", err)
	}
	if _, err := os.Stat(cfg.Collector.ConfigPath); err != nil {
		t.Fatalf("collector config not written: %v", err)
	}
	if info, err := os.Stat(filepath.Dir(cfg.Collector.SpoolPath)); err != nil || !info.IsDir() {
		t.Fatalf("spool dir not created: info=%v err=%v", info, err)
	}
}

func TestDiscoverBinaryPrefersConfiguredExistingPath(t *testing.T) {
	bin := filepath.Join(t.TempDir(), BinaryName)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("write fake collector: %v", err)
	}

	if got := DiscoverBinary(bin); got != bin {
		t.Fatalf("DiscoverBinary = %q, want configured path %q", got, bin)
	}
}

func TestResolveBinaryRejectsMissingConfiguredPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), BinaryName)

	_, err := ResolveBinary(missing)
	if err == nil || !strings.Contains(err.Error(), "not usable") || !strings.Contains(err.Error(), missing) {
		t.Fatalf("ResolveBinary error = %v, want configured path error", err)
	}
}

func TestDiscoverBinaryFindsBeaconOtelcolOnPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, BinaryName)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("write fake collector: %v", err)
	}
	t.Setenv("PATH", dir)
	withCollectorDiscovery(t, nil, nil)

	if got := DiscoverBinary(""); got != bin {
		t.Fatalf("DiscoverBinary = %q, want PATH binary %q", got, bin)
	}
}

func TestDiscoverBinaryIgnoresGenericOtelcol(t *testing.T) {
	dir := t.TempDir()
	generic := filepath.Join(dir, "otelcol-contrib")
	if err := os.WriteFile(generic, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("write generic collector: %v", err)
	}
	t.Setenv("PATH", dir)
	withCollectorDiscovery(t, nil, nil)

	if got := DiscoverBinary(""); got != "" {
		t.Fatalf("DiscoverBinary = %q, want empty for generic collector", got)
	}
}

func TestDiscoverBinaryFindsAdjacentBeaconOtelcol(t *testing.T) {
	dir := t.TempDir()
	beacon := filepath.Join(dir, "beacon")
	collector := filepath.Join(dir, BinaryName)
	if err := os.WriteFile(collector, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("write adjacent collector: %v", err)
	}
	withCollectorDiscovery(t, func(file string) (string, error) {
		if file == BinaryName {
			return "", errors.New("not found")
		}
		return "", errors.New("unexpected lookup")
	}, func() []string {
		return []string{filepath.Join(filepath.Dir(beacon), BinaryName)}
	})

	if got := DiscoverBinary(""); got != collector {
		t.Fatalf("DiscoverBinary = %q, want adjacent collector %q", got, collector)
	}
}

func TestPortAvailabilityAndOpenChecks(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	if PortAvailable(port) {
		t.Fatalf("PortAvailable(%d) = true while listener is active", port)
	}
	if !portOpen(port) {
		t.Fatalf("portOpen(%d) = false while listener is active", port)
	}
}

func TestLaunchAgentPlistUsesFallbackBinaryAndUserLabel(t *testing.T) {
	cfg := testConfig(t)
	cfg.UserMode = true
	cfg.Collector.BinaryPath = filepath.Join(t.TempDir(), "missing-otelcol")
	withCollectorDiscovery(t, func(file string) (string, error) {
		return "", errors.New("not found")
	}, nil)

	plist := LaunchAgentPlist(cfg)

	for _, want := range []string{
		"<string>com.beacon.endpoint.collector.user</string>",
		"<string>beacon-otelcol</string>",
		"<string>--config</string>",
		"<string>" + cfg.Collector.ConfigPath + "</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("LaunchAgentPlist missing %q:\n%s", want, plist)
		}
	}
}

func TestWriteLaunchPlistUserMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("launch plist paths are POSIX-specific")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := testConfig(t)
	cfg.UserMode = true

	path, err := WriteLaunchPlist(cfg)
	if err != nil {
		t.Fatalf("WriteLaunchPlist returned error: %v", err)
	}
	if got, want := path, filepath.Join(home, "Library", "LaunchAgents", "com.beacon.endpoint.collector.plist"); got != want {
		t.Fatalf("plist path = %q, want %q", got, want)
	}
}

func withCollectorDiscovery(t *testing.T, lookup func(string) (string, error), candidates func() []string) {
	t.Helper()
	oldLookPath := lookPath
	oldCandidates := discoverDefaultBinaryCandidates
	if lookup == nil {
		lookup = execLookPath
	}
	if candidates == nil {
		candidates = func() []string { return nil }
	}
	lookPath = lookup
	discoverDefaultBinaryCandidates = candidates
	t.Cleanup(func() {
		lookPath = oldLookPath
		discoverDefaultBinaryCandidates = oldCandidates
	})
}

func execLookPath(file string) (string, error) {
	return exec.LookPath(file)
}
