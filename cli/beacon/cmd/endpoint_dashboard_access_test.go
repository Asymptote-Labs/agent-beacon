package cmd

import (
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func useDashboardTestDefaults(t *testing.T) *dashboard.Options {
	t.Helper()
	oldOpts := endpointOpts
	oldListen := dashboardListenAndServe
	oldSwitch := dashboardSystemDisabled
	oldOpen := openDashboardBrowser
	t.Cleanup(func() {
		endpointOpts = oldOpts
		dashboardListenAndServe = oldListen
		dashboardSystemDisabled = oldSwitch
		openDashboardBrowser = oldOpen
	})
	home := t.TempDir()
	testenv.SetHome(t, home)
	if _, err := endpointconfig.Save(endpointconfig.Default(true, filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl"))); err != nil {
		t.Fatal(err)
	}
	served := &dashboard.Options{}
	dashboardListenAndServe = func(opts dashboard.Options) error {
		*served = opts
		return acceptDashboardBind(opts)
	}
	endpointOpts.userMode = true
	endpointOpts.systemMode = false
	endpointOpts.logPath = ""
	endpointOpts.dashboardOpen = false
	endpointOpts.dashboardAddr = "127.0.0.1:8765"
	return served
}

// The URL the command prints is the only way in, so it must carry the secret the server checks,
// and the secret must appear nowhere else in the output.
func TestRunEndpointDashboardPrintsTheLaunchURL(t *testing.T) {
	served := useDashboardTestDefaults(t)
	out, err := captureStdout(t, func() error { return runEndpointDashboard(nil, nil) })
	if err != nil {
		t.Fatal(err)
	}
	if len(served.Token) < 43 {
		t.Fatalf("served token = %q", served.Token)
	}
	if !strings.Contains(out, "Beacon endpoint dashboard: http://127.0.0.1:8765/?token="+served.Token+"\n") {
		t.Fatalf("output = %q", out)
	}
	if strings.Count(out, served.Token) != 1 {
		t.Fatalf("the launch secret appears outside the URL: %q", out)
	}
	first := served.Token
	if _, err := captureStdout(t, func() error { return runEndpointDashboard(nil, nil) }); err != nil {
		t.Fatal(err)
	}
	if served.Token == first {
		t.Fatal("two launches shared one secret")
	}
}

func TestRunEndpointDashboardOpensTheBrowserOnlyAfterThePortBinds(t *testing.T) {
	served := useDashboardTestDefaults(t)
	var opened string
	openDashboardBrowser = func(url string) error { opened = url; return nil }
	endpointOpts.dashboardOpen = true
	out, err := captureStdout(t, func() error { return runEndpointDashboard(nil, nil) })
	if err != nil {
		t.Fatal(err)
	}
	want := "http://127.0.0.1:8765/?token=" + served.Token
	if opened != want {
		t.Fatalf("opened %q, want %q", opened, want)
	}
	if strings.Count(out, served.Token) != 1 {
		t.Fatalf("output = %q", out)
	}

	opened = ""
	dashboardListenAndServe = func(dashboard.Options) error {
		return errors.New("listen tcp 127.0.0.1:8765: bind: address already in use")
	}
	out, err = captureStdout(t, func() error { return runEndpointDashboard(nil, nil) })
	if err == nil || !strings.Contains(err.Error(), "bind") {
		t.Fatalf("taken port = %v", err)
	}
	if opened != "" || strings.Contains(out, "token=") || strings.Contains(out, "Beacon endpoint dashboard:") {
		t.Fatalf("a failed bind disclosed the launch: opened %q, output %q", opened, out)
	}
}

func acceptDashboardBind(opts dashboard.Options) error {
	if opts.Ready == nil {
		return nil
	}
	addr, err := net.ResolveTCPAddr("tcp", opts.Addr)
	if err != nil {
		return err
	}
	return opts.Ready(addr)
}

func TestRunEndpointDashboardRefusesWhenTheAdministratorDisabledIt(t *testing.T) {
	served := useDashboardTestDefaults(t)
	dashboardSystemDisabled = func() (bool, error) { return true, nil }
	_, err := captureStdout(t, func() error { return runEndpointDashboard(nil, nil) })
	if err == nil || !strings.Contains(err.Error(), "disabled on this machine by its administrator") {
		t.Fatalf("disabled dashboard = %v", err)
	}
	if served.Token != "" {
		t.Fatal("a disabled dashboard was served")
	}

	dashboardSystemDisabled = func() (bool, error) { return false, errors.New("permission denied") }
	if _, err := captureStdout(t, func() error { return runEndpointDashboard(nil, nil) }); err == nil || !strings.Contains(err.Error(), "cannot read the administrator's setting") {
		t.Fatalf("unreadable setting = %v", err)
	}
	if served.Token != "" {
		t.Fatal("a dashboard was served without reading the setting")
	}
}

func TestLensesPreviewRefusesWhenTheAdministratorDisabledTheDashboard(t *testing.T) {
	useLensToolsDefaults(t)
	lensToolsOpts.logPath = writeLensLog(t)
	served := false
	prev, prevSwitch := dashboardListenAndServe, dashboardSystemDisabled
	dashboardListenAndServe = func(dashboard.Options) error { served = true; return nil }
	dashboardSystemDisabled = func() (bool, error) { return true, nil }
	t.Cleanup(func() { dashboardListenAndServe, dashboardSystemDisabled = prev, prevSwitch })
	cmd, _ := newCmd()
	if err := lensesPreviewCmd.RunE(cmd, []string{writeTestLens(t, t.TempDir(), "my-lens", "1")}); err == nil || !strings.Contains(err.Error(), "administrator") || served {
		t.Fatalf("preview with the dashboard disabled = %v, served %v", err, served)
	}
}

func TestDashboardSwitchOption(t *testing.T) {
	oldOpts := endpointOpts
	t.Cleanup(func() { endpointOpts = oldOpts })

	endpointOpts.userMode, endpointOpts.systemMode = true, true
	for in, want := range map[string]string{"": "", "enabled": "enabled", " Disabled": "disabled"} {
		endpointOpts.dashboardSwitch = in
		if got, err := dashboardSwitchOption(); err != nil || got != want {
			t.Errorf("--system --dashboard %q = %q, %v", in, got, err)
		}
	}
	endpointOpts.dashboardSwitch = "off"
	if _, err := dashboardSwitchOption(); err == nil {
		t.Error("--dashboard off was accepted")
	}
	endpointOpts.systemMode = false
	endpointOpts.dashboardSwitch = "disabled"
	if _, err := dashboardSwitchOption(); err == nil || !strings.Contains(err.Error(), "--system") {
		t.Errorf("user-mode --dashboard = %v", err)
	}
	endpointOpts.dashboardSwitch = ""
	if got, err := dashboardSwitchOption(); err != nil || got != "" {
		t.Errorf("user mode without --dashboard = %q, %v", got, err)
	}

	endpointOpts.systemMode = true
	endpointOpts.dashboardSwitch = "disabled"
	if opts := endpointInstallOptions(endpointTargetSelection{}, ""); opts.Dashboard != "disabled" {
		t.Errorf("install options carry dashboard %q", opts.Dashboard)
	}
}

func TestDashboardStatusLine(t *testing.T) {
	for _, tc := range []struct {
		status lifecycle.Status
		want   string
	}{
		{lifecycle.Status{Dashboard: "enabled"}, "Dashboard: enabled"},
		{lifecycle.Status{Dashboard: "disabled"}, "Dashboard: disabled by the administrator"},
		{lifecycle.Status{Dashboard: "disabled", DashboardError: "permission denied"}, "Dashboard: unavailable"},
	} {
		if got := dashboardStatusLine(tc.status); !strings.HasPrefix(got, tc.want) {
			t.Errorf("dashboardStatusLine(%+v) = %q", tc.status, got)
		}
	}
}
