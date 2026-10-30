//go:build windows

package config

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sysacl"
)

// The system config.json holding a HEC token is readable only by SYSTEM and Administrators on
// Windows, the counterpart of the root-owned 0600 file on POSIX; without a token it is readable by
// everyone again, as the CLI expects when it runs unelevated.
func TestSystemConfigWithAHECTokenIsPrivateOnWindows(t *testing.T) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Skipf("open the process token: %v", err)
	}
	elevated := token.IsElevated()
	token.Close()
	if !elevated {
		t.Skip("writing a file owned by Administrators needs an elevated shell")
	}
	programData := t.TempDir()
	t.Setenv("ProgramData", programData)
	if err := sysacl.SecureSystemTree(filepath.Join(programData, "Beacon")); err != nil {
		t.Fatal(err)
	}

	cfg := Default(false, filepath.Join(programData, "Beacon", "Endpoint", "logs", "runtime.jsonl"))
	cfg.Destinations = &Destinations{FalconHEC: &FalconHEC{
		Endpoint: "https://cloud.us.humio.com/api/v1/ingest/hec",
		Token:    "ingest-token",
	}}
	path, err := Save(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if problems, err := sysacl.InspectPrivateFile(path); err != nil || len(problems) > 0 {
		t.Fatalf("config.json with a HEC token: %v, %v", problems, err)
	}
	if loaded, err := Load(false); err != nil || loaded.Destinations.FalconHEC.Token != "ingest-token" {
		t.Fatalf("Load after a private save: %+v, %v", loaded.Destinations, err)
	}

	cfg.Destinations = nil
	if _, err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	if sysacl.IsPrivateFile(path) {
		t.Fatal("config.json stayed private after the token was removed")
	}
}
