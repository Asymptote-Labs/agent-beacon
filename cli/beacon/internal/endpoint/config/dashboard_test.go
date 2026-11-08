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
		`{"user_mode":false}`:        false,
		`{"dashboard":"enabled"}`:    false,
		`{"dashboard":"disabled"}`:   true,
		`{"dashboard":" Disabled "}`: true,
		// A value --dashboard would refuse fails closed.
		`{"dashboard":"other-value"}`: true,
		`{"dashboard":"off"}`:         true,
		`{"dashboard":""}`:            false,
	} {
		if disabled, err := DashboardDisabledAt(write("config.json", body)); err != nil || disabled != want {
			t.Errorf("%s = %v, %v; want %v", body, disabled, err, want)
		}
	}
	if _, err := DashboardDisabledAt(write("broken.json", `{"dashboard":`)); err == nil {
		t.Error("a config that does not parse read as enabled")
	}
}

// A system config that holds a HEC token is root-only, and the accounts that cannot read it are the
// ones the switch is for: the marker beside it answers for them.
func TestDashboardDisabledAtAnUnreadableConfigUsesTheMarker(t *testing.T) {
	if !testenv.HasPOSIXFileModes() || os.Geteuid() == 0 {
		t.Skip("needs POSIX file modes and an account that root-only files refuse")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"dashboard":"disabled","destinations":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	if disabled, err := DashboardDisabledAt(path); err != nil || disabled {
		t.Fatalf("unreadable config, no marker = %v, %v; want enabled with no error", disabled, err)
	}
	if err := syncDashboardMarker(path, true); err != nil {
		t.Fatal(err)
	}
	if disabled, err := DashboardDisabledAt(path); err != nil || !disabled {
		t.Fatalf("unreadable config with the marker = %v, %v; want disabled", disabled, err)
	}
}

func TestSyncDashboardMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	marker := DashboardMarkerPath(path)
	if err := syncDashboardMarker(path, false); err != nil {
		t.Fatalf("removing a marker that is not there = %v", err)
	}
	if err := syncDashboardMarker(path, true); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(marker)
	if err != nil {
		t.Fatalf("no marker after disabling: %v", err)
	}
	if testenv.HasPOSIXFileModes() && info.Mode().Perm() != 0o644 {
		t.Errorf("marker mode = %v, want 0644 so every account can read it", info.Mode().Perm())
	}
	if err := syncDashboardMarker(path, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("marker still there after enabling: %v", err)
	}
}
