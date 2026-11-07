package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestParseDashboardMode(t *testing.T) {
	for in, want := range map[string]string{"": "", " Enabled ": "enabled", "DISABLED": "disabled"} {
		if got, err := ParseDashboardMode(in); err != nil || got != want {
			t.Errorf("ParseDashboardMode(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseDashboardMode("off"); err == nil {
		t.Error("an unknown mode was accepted")
	}
}

func TestDashboardDisabledAt(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if disabled, err := DashboardDisabledAt(filepath.Join(dir, "missing.json")); disabled || err != nil {
		t.Fatalf("missing config = %v, %v; want enabled", disabled, err)
	}
	for body, want := range map[string]bool{
		`{"user_mode":false}`:         false,
		`{"dashboard":"enabled"}`:     false,
		`{"dashboard":"disabled"}`:    true,
		`{"dashboard":" Disabled "}`:  true,
		`{"dashboard":"other-value"}`: false,
	} {
		if disabled, err := DashboardDisabledAt(write("config.json", body)); err != nil || disabled != want {
			t.Errorf("%s = %v, %v; want %v", body, disabled, err, want)
		}
	}
	if _, err := DashboardDisabledAt(write("broken.json", `{"dashboard":`)); err == nil {
		t.Error("a config that does not parse read as enabled")
	}
	if testenv.HasPOSIXFileModes() && os.Geteuid() != 0 {
		path := write("private.json", `{"dashboard":"enabled"}`)
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := DashboardDisabledAt(path); err == nil {
			t.Error("an unreadable config read as enabled")
		}
	}
}
