package cmd

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/account"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/onboarding"
)

// stubDashboardOpener records the URLs the install would open, on a machine that has a display
// and is not reached over SSH, without --no-browser.
func stubDashboardOpener(t *testing.T) *[]string {
	t.Helper()
	prevOpen, prevDisplay, prevNoBrowser := onboardingOpenDashboard, onboardingDisplayCheck, endpointOpts.noBrowser
	t.Cleanup(func() {
		onboardingOpenDashboard, onboardingDisplayCheck, endpointOpts.noBrowser = prevOpen, prevDisplay, prevNoBrowser
	})
	opened := &[]string{}
	onboardingOpenDashboard = func(url string) error {
		*opened = append(*opened, url)
		return nil
	}
	onboardingDisplayCheck = func() error { return nil }
	endpointOpts.noBrowser = false
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_TTY", "")
	return opened
}

// stubCloudConnect makes connectEndpointFor enroll as person@example.com without a network,
// returning a connect that staged events of session history, or err.
func stubCloudConnect(t *testing.T, events int, err error) *asymptote.ConnectOptions {
	t.Helper()
	prevLoad, prevConnect, prevConnectOpts := connectAccountLoad, connectManagedEndpoint, connectOpts
	t.Cleanup(func() {
		connectAccountLoad, connectManagedEndpoint, connectOpts = prevLoad, prevConnect, prevConnectOpts
	})
	connectOpts.privacyMode = ""
	connectAccountLoad = func() (*account.Session, error) {
		return &account.Session{
			BaseURL: "https://beacon.sh", AccessToken: "bcn_cli_secret", ExpiresAt: time.Now().Add(time.Hour),
			Scopes: []string{account.ScopeProfileRead, account.ScopeDeviceEnroll},
			User:   account.User{ID: "usr_1", Email: "person@example.com"},
		}, nil
	}
	captured := &asymptote.ConnectOptions{}
	connectManagedEndpoint = func(_ context.Context, options asymptote.ConnectOptions) (*asymptote.ConnectResult, error) {
		*captured = options
		if err != nil {
			return nil, err
		}
		return &asymptote.ConnectResult{
			Enrollment:     asymptote.Enrollment{DeviceID: "dev_7f3a", DashboardURL: "https://beacon.sh", PrivacyMode: options.PrivacyMode},
			Forwarder:      "test",
			ForwarderState: service.Status{Loaded: true, Running: true},
			VectorConfig:   "/tmp/vector.toml",
			SecretsFile:    "/tmp/secrets.json",
			Backfill:       &asymptote.BackfillResult{Events: events},
		}, nil
	}
	return captured
}

// Finishing the wizard with Beacon Cloud used to end on some forty lines of paths and per-harness
// steps, and nothing sent the person to the dashboard. The connect is now quiet and opens it.
func TestWizardInstallClosesOnAShortSummaryAndOpensTheDashboard(t *testing.T) {
	recordEndpointLifecycle(t)
	h := newOnboardingHarness(t)
	fx := newMCPFixture(t, "claude_code", "cursor", "vscode")
	mcpIngestURL = func() string { return strings.TrimSuffix(fx.url, "/mcp") }
	opened := stubDashboardOpener(t)
	captured := stubCloudConnect(t, 12, nil)
	// Claude Code has more history than its share of the budget; Codex fits.
	stubBackfill(t, 1<<20, 4000,
		endpointBackfillRunner{Name: "Claude Code", Sync: "beacon endpoint claude sync", Run: writingRunner("claude", 100, nil).Run},
		writingRunner("Codex", 2, nil))
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	endpointLifecycleInstall = func(lifecycle.InstallOptions) (lifecycle.InstallResult, error) {
		return lifecycle.InstallResult{LogPath: logPath, ConfigPath: "/tmp/config.json"}, nil
	}
	endpointOpts.harnesses = ""
	onboardingRunWizard = func(io.Reader, io.Writer, onboarding.WizardOptions) (onboarding.WizardResult, error) {
		return onboarding.WizardResult{
			Completed: true, Destination: onboarding.DestinationAsymptote, PrivacyMode: "standard",
			ConnectCloudMCP: true, InstallSkills: true,
		}, nil
	}

	if err := runEndpointInstall(h.cmd, nil); err != nil {
		t.Fatal(err)
	}
	if h.stderr.Len() != 0 {
		t.Fatalf("stderr: %s", h.stderr)
	}
	if !captured.Quiet {
		t.Fatal("the wizard's connect printed its progress lines")
	}
	if !strings.Contains(h.stdout.String(), "https://beacon.sh/dashboard") {
		t.Fatalf("final screen does not show the dashboard URL:\n%s", h.stdout)
	}
	if len(*opened) != 1 || (*opened)[0] != "https://beacon.sh/dashboard" {
		t.Fatalf("dashboard opened %v, want https://beacon.sh/dashboard once", *opened)
	}
	if strings.Contains(h.stdout.String(), "bcn_cli_secret") {
		t.Fatal("the account token reached the output")
	}
}

func TestWizardCloudConnectOpensTheDashboardOnlyWhereThePersonCanSeeIt(t *testing.T) {
	for _, tc := range []struct {
		name       string
		connectErr error
		setup      func(t *testing.T)
		wantOpen   bool
		wantLine   bool
	}{
		{name: "connected", wantOpen: true, wantLine: true},
		{name: "connect failed", connectErr: errors.New("enroll failed")},
		{name: "--no-browser", setup: func(*testing.T) { endpointOpts.noBrowser = true }, wantLine: true},
		{name: "SSH_CONNECTION", setup: func(t *testing.T) { t.Setenv("SSH_CONNECTION", "10.0.0.2 51234 10.0.0.1 22") }, wantLine: true},
		{name: "SSH_TTY", setup: func(t *testing.T) { t.Setenv("SSH_TTY", "/dev/ttys004") }, wantLine: true},
		{name: "no display", setup: func(*testing.T) {
			onboardingDisplayCheck = func() error { return errors.New("DISPLAY and WAYLAND_DISPLAY are not set") }
		}, wantLine: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newOnboardingHarness(t)
			opened := stubDashboardOpener(t)
			stubCloudConnect(t, 0, tc.connectErr)
			if tc.setup != nil {
				tc.setup(t)
			}
			summary := &cloudSummary{}
			summary.backfill.WriteString("Session backfill: no new agent session activity from the last 30 days.\n")
			finishCloudSetup(h.cmd, onboardingOutcome{Connect: true}, true, summary, func() error {
				return connectEndpointFor(h.cmd, true, filepath.Join(t.TempDir(), "runtime.jsonl"), true, summary)
			})
			if got := len(*opened) == 1; got != tc.wantOpen || len(*opened) > 1 {
				t.Fatalf("opened %v, want opened = %t", *opened, tc.wantOpen)
			}
			if tc.wantOpen && (*opened)[0] != "https://beacon.sh/dashboard" {
				t.Fatalf("opened %q", (*opened)[0])
			}
			text := h.stdout.String()
			if got := strings.Contains(text, "Dashboard: https://beacon.sh/dashboard"); got != tc.wantLine {
				t.Fatalf("Dashboard line shown = %t, want %t:\n%s", got, tc.wantLine, text)
			}
			// The backfill report is said either way: a failed connect still leaves that history
			// in the local log.
			if !strings.Contains(text, "Session backfill: no new agent session activity") {
				t.Fatalf("backfill report missing:\n%s", text)
			}
			if tc.connectErr != nil && !strings.Contains(h.stderr.String(), "beacon endpoint connect") {
				t.Fatalf("a failed connect must name the retry: %q", h.stderr)
			}
		})
	}
}

// A browser that fails to open is no reason to say anything: the URL is already on screen.
func TestWizardDashboardOpenFailureIsSilent(t *testing.T) {
	h := newOnboardingHarness(t)
	stubDashboardOpener(t)
	onboardingOpenDashboard = func(string) error { return errors.New("no browser") }
	stubCloudConnect(t, 3, nil)
	summary := &cloudSummary{}
	finishCloudSetup(h.cmd, onboardingOutcome{Connect: true}, true, summary, func() error {
		return connectEndpointFor(h.cmd, true, filepath.Join(t.TempDir(), "runtime.jsonl"), true, summary)
	})
	if h.stderr.Len() != 0 {
		t.Fatalf("stderr: %s", h.stderr)
	}
	if !strings.Contains(h.stdout.String(), "https://beacon.sh/dashboard") {
		t.Fatalf("stdout:\n%s", h.stdout)
	}
}

// `beacon endpoint connect`, and `install --connect` without the wizard, keep the full report and
// open nothing.
func TestExplicitConnectKeepsTheFullReport(t *testing.T) {
	h := newOnboardingHarness(t)
	opened := stubDashboardOpener(t)
	captured := stubCloudConnect(t, 0, nil)
	finishCloudSetup(h.cmd, onboardingOutcome{}, true, nil, func() error {
		return connectEndpoint(h.cmd, true, filepath.Join(t.TempDir(), "runtime.jsonl"), true)
	})
	if captured.Quiet {
		t.Fatal("an explicit connect was asked to be quiet")
	}
	if len(*opened) != 0 {
		t.Fatalf("opened %v", *opened)
	}
	if !strings.Contains(h.stdout.String(), "dev_7f3a") {
		t.Fatalf("the full report does not name the device:\n%s", h.stdout)
	}
	if strings.Contains(h.stdout.String(), "Dashboard: ") {
		t.Fatalf("the explicit connect gained the wizard's closing:\n%s", h.stdout)
	}
}

func TestCancelledWizardInstallsNothing(t *testing.T) {
	calls := recordEndpointLifecycle(t)
	h := newOnboardingHarness(t)
	endpointOpts.harnesses = ""
	h.askErr = onboarding.ErrWizardCancelled
	if err := runEndpointInstall(h.cmd, nil); err == nil {
		t.Fatal("a cancelled wizard returned no error")
	}
	if len(*calls) != 0 {
		t.Fatalf("a cancelled wizard still installed: %+v", *calls)
	}
}
