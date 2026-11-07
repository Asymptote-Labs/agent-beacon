package cmd

import (
	"fmt"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/spf13/cobra"
)

// dashboardStatusLine is the dashboard switch as `beacon endpoint status` prints it.
func dashboardStatusLine(status lifecycle.Status) string {
	switch {
	case status.DashboardError != "":
		return fmt.Sprintf("Dashboard: unavailable (cannot read the administrator's setting: %s)", status.DashboardError)
	case status.Dashboard == endpointconfig.DashboardDisabled:
		return "Dashboard: disabled by the administrator (system config)"
	}
	return "Dashboard: enabled"
}

const dashboardSwitchUsage = "Local dashboard for every account on this machine: enabled (the default) or disabled. " +
	"Applies to a system install (--system); empty keeps the recorded setting"

// dashboardSystemDisabled reads the administrator's switch from the system config. A seam for tests,
// which must not depend on the machine they run on.
var dashboardSystemDisabled = endpointconfig.SystemDashboardDisabled

func registerDashboardSwitchFlag(c *cobra.Command) {
	c.Flags().StringVar(&endpointOpts.dashboardSwitch, "dashboard", "", dashboardSwitchUsage)
}

// dashboardSwitchOption turns --dashboard into the install option: "" when not given, so repair and
// package upgrades keep the recorded setting.
func dashboardSwitchOption() (string, error) {
	mode, err := endpointconfig.ParseDashboardMode(endpointOpts.dashboardSwitch)
	if err != nil {
		return "", err
	}
	if mode != "" && endpointUserMode() {
		return "", fmt.Errorf("--dashboard %s applies to a system install; add --system (it is the administrator's switch for every account)", mode)
	}
	return mode, nil
}

// refuseDisabledDashboard stops `beacon endpoint dashboard` and `beacon lenses preview` when the
// system config turns the dashboard off. It applies to every account, root included, and to a
// user-mode run on a machine that also has a system install. A system config that exists but
// cannot be read or parsed refuses too, so a damaged file never turns the switch back on.
func refuseDisabledDashboard() error {
	disabled, err := dashboardSystemDisabled()
	if err != nil {
		return fmt.Errorf("the local dashboard is not available: cannot read the administrator's setting in %s: %w", endpointconfig.SystemConfigPath(), err)
	}
	if disabled {
		return fmt.Errorf("the local dashboard is disabled on this machine by its administrator (%q is \"disabled\" in %s; an administrator can turn it back on with `beacon endpoint repair --system --dashboard enabled`)", "dashboard", endpointconfig.SystemConfigPath())
	}
	return nil
}
