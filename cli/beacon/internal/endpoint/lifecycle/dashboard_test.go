package lifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// The dashboard switch is an administrator's choice: a repair or package upgrade that rebuilt
// config.json without it would quietly turn the dashboard back on.
func TestBuildConfigKeepsTheDashboardSwitch(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	writeInventoryConfig(t, true, `{"user_mode":true,"log_path":"/tmp/runtime.jsonl","dashboard":"disabled"}`)
	if cfg := buildConfig(InstallOptions{UserMode: true}); cfg.Dashboard != endpointconfig.DashboardDisabled {
		t.Fatalf("Dashboard = %q, want the existing setting carried over", cfg.Dashboard)
	}
	if cfg := buildConfig(InstallOptions{UserMode: true, Dashboard: "enabled"}); cfg.Dashboard != endpointconfig.DashboardEnabled {
		t.Fatalf("Dashboard = %q, want the explicit option to win", cfg.Dashboard)
	}
}

func TestBuildConfigWithoutAnExistingConfigHasTheDashboardOn(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	cfg := buildConfig(InstallOptions{UserMode: true})
	if cfg.Dashboard != "" || cfg.DashboardMode() != endpointconfig.DashboardEnabled {
		t.Fatalf("a fresh install has dashboard %q", cfg.Dashboard)
	}
}

// A hand-written value --dashboard refuses is carried as disabled, how the dashboard reads it,
// rather than raw into a preflight that would fail every repair and package upgrade.
func TestCarriedDashboard(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "enabled": "enabled", " Disabled ": "disabled", "off": "disabled", "no": "disabled",
	} {
		if got := carriedDashboard(in); got != want {
			t.Errorf("carriedDashboard(%q) = %q, want %q", in, got, want)
		}
		if err := validateDashboard(endpointconfig.Config{Dashboard: carriedDashboard(in)}); err != nil {
			t.Errorf("carried %q fails preflight: %v", in, err)
		}
	}
	testenv.SetHome(t, t.TempDir())
	writeInventoryConfig(t, true, `{"user_mode":true,"log_path":"/tmp/runtime.jsonl","dashboard":"off"}`)
	if cfg := buildConfig(InstallOptions{UserMode: true}); cfg.Dashboard != endpointconfig.DashboardDisabled {
		t.Fatalf("Dashboard = %q, want a hand-written \"off\" carried as disabled", cfg.Dashboard)
	}
}

// Repairing a system config that no longer loads must not turn the dashboard back on: the marker
// beside it still records the switch.
func TestBuildConfigKeepsTheDashboardOffWhenTheSystemConfigDoesNotLoad(t *testing.T) {
	prevMarked, prevRestricted := systemDashboardMarked, logIsRestricted
	t.Cleanup(func() { systemDashboardMarked, logIsRestricted = prevMarked, prevRestricted })
	logIsRestricted = func(string) bool { return false }
	if _, err := endpointconfig.Load(false); err == nil {
		t.Skip("this machine has a loadable system config")
	}
	systemDashboardMarked = func() bool { return true }
	if cfg := buildConfig(InstallOptions{}); cfg.Dashboard != endpointconfig.DashboardDisabled {
		t.Fatalf("Dashboard = %q, want disabled from the marker", cfg.Dashboard)
	}
	if cfg := buildConfig(InstallOptions{Dashboard: endpointconfig.DashboardEnabled}); cfg.Dashboard != endpointconfig.DashboardEnabled {
		t.Fatalf("Dashboard = %q, want the explicit --dashboard enabled to win", cfg.Dashboard)
	}
	systemDashboardMarked = func() bool { return false }
	if cfg := buildConfig(InstallOptions{}); cfg.Dashboard != "" {
		t.Fatalf("Dashboard = %q, want unset without a marker", cfg.Dashboard)
	}
}

func TestValidateDashboard(t *testing.T) {
	for _, mode := range []string{"", "enabled", "disabled", "Disabled"} {
		if err := validateDashboard(endpointconfig.Config{Dashboard: mode}); err != nil {
			t.Errorf("system %q = %v", mode, err)
		}
	}
	if err := validateDashboard(endpointconfig.Config{Dashboard: "off"}); err == nil {
		t.Error("an unknown mode was accepted")
	}
	if err := validateDashboard(endpointconfig.Config{UserMode: true, Dashboard: "disabled"}); err == nil || !strings.Contains(err.Error(), "system") {
		t.Errorf("user-mode switch = %v", err)
	}
	if err := validateDashboard(endpointconfig.Config{UserMode: true}); err != nil {
		t.Errorf("user mode without a switch = %v", err)
	}
}

func TestStatusReportsTheDashboardSwitch(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	cfg := endpointconfig.Default(true, t.TempDir()+"/runtime.jsonl")
	runtimeLog := RuntimeLogSource{EffectiveUserMode: true, EffectiveLogPath: cfg.LogPath, RequestedUserMode: true, RequestedLogPath: cfg.LogPath}
	prev := systemDashboardDisabled
	t.Cleanup(func() { systemDashboardDisabled = prev })

	cases := []struct {
		name      string
		disabled  bool
		err       error
		wantMode  string
		wantError bool
	}{
		{"enabled", false, nil, "enabled", false},
		{"disabled", true, nil, "disabled", false},
		{"unreadable", false, errors.New("permission denied"), "disabled", true},
	}
	for _, tc := range cases {
		systemDashboardDisabled = func() (bool, error) { return tc.disabled, tc.err }
		status := buildStatus(true, cfg, runtimeLog, asymptote.StatusOptions{SkipCredentialCheck: true})
		if status.Dashboard != tc.wantMode || (status.DashboardError != "") != tc.wantError {
			t.Errorf("%s: dashboard = %q, error %q", tc.name, status.Dashboard, status.DashboardError)
		}
	}
}

// The dashboard marker goes wherever config.json goes. A normal uninstall removes config.json
// through the manifest; a marker left behind would bring a later clean install back disabled.
// Repair keeps both.
func TestUninstallRemovesTheDashboardMarkerWithTheConfig(t *testing.T) {
	for _, withManifest := range []bool{false, true} {
		for _, repair := range []bool{false, true} {
			testenv.SetHome(t, t.TempDir())
			cfg := endpointconfig.Default(true, filepath.Join(t.TempDir(), "runtime.jsonl"))
			configPath, err := endpointconfig.Save(cfg)
			if err != nil {
				t.Fatal(err)
			}
			marker := endpointconfig.DashboardMarkerPath(configPath)
			if err := os.WriteFile(marker, []byte("disabled\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if withManifest {
				if _, err := writeManifest(true, Manifest{Files: []string{configPath}}); err != nil {
					t.Fatal(err)
				}
			}
			_ = Uninstall(UninstallOptions{UserMode: true, KeepManagedIngest: repair})
			_, err = os.Stat(marker)
			if repair && err != nil {
				t.Errorf("manifest=%v: repair removed the dashboard marker: %v", withManifest, err)
			}
			if !repair && !os.IsNotExist(err) {
				t.Errorf("manifest=%v: uninstall left the dashboard marker behind: %v", withManifest, err)
			}
		}
	}
}
