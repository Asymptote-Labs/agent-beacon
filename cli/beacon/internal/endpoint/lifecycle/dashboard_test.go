package lifecycle

import (
	"errors"
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
