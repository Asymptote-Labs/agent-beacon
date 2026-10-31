//go:build windows

package collector

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sysacl"
)

// On Windows a system collector config holding a HEC token used to inherit the machine-wide tree's
// Users read grant, since 0600 changes nothing there. It is private to SYSTEM and Administrators
// now, including when it replaces a config an older install left readable, and a config without a
// token stays readable.
func TestSystemCollectorConfigWithAHECTokenIsPrivateOnWindows(t *testing.T) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Skipf("open the process token: %v", err)
	}
	elevated := token.IsElevated()
	token.Close()
	if !elevated {
		t.Skip("writing a file owned by Administrators needs an elevated shell")
	}

	root := filepath.Join(t.TempDir(), "Beacon")
	if err := sysacl.SecureSystemTree(root); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t)
	cfg.UserMode = false
	cfg.Collector.ConfigPath = filepath.Join(root, "Endpoint", "otelcol.yaml")
	cfg.Collector.SpoolPath = filepath.Join(root, "Endpoint", "spool", "otlp.jsonl")

	if err := WriteConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if sysacl.IsPrivateFile(cfg.Collector.ConfigPath) {
		t.Fatal("setup: a config without a token is private")
	}

	cfg.Destinations = &endpointconfig.Destinations{SplunkHEC: &endpointconfig.SplunkHEC{
		Endpoint: "https://splunk.example:8088/services/collector",
		Token:    "hec-token",
	}}
	if err := WriteConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if problems, err := sysacl.InspectPrivateFile(cfg.Collector.ConfigPath); err != nil || len(problems) > 0 {
		t.Fatalf("the collector config with a HEC token: %v, %v", problems, err)
	}
	if data, err := os.ReadFile(cfg.Collector.ConfigPath); err != nil || len(data) == 0 {
		t.Fatalf("read the private config back: %v", err)
	}
}
