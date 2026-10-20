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
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/mcpconnect"
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
// steps, and nothing sent the person to the dashboard. It now closes on four lines and opens it.
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
	t.Logf("final screen:\n%s", h.stdout)
	lines := strings.Split(strings.TrimSpace(h.stdout.String()), "\n")
	want := []string{
		"Installing Beacon and connecting to Beacon Cloud...",
		"",
		"Beacon is installed and connected to Beacon Cloud as person@example.com (device dev_7f3a, Standard privacy).",
		"Session backfill: ",
		"  Claude Code: stopped at the backfill size limit; `beacon endpoint claude sync` reads the rest.",
		"Dashboard: https://beacon.sh/dashboard (your recent sessions appear within a minute)",
		"Beacon Cloud MCP added to Claude Code, Cursor, VS Code; sign in to beacon-managed once in each app (Claude Code: /mcp in a new session).",
	}
	if len(lines) != len(want) {
		t.Fatalf("final screen has %d lines, want %d:\n%s", len(lines), len(want), h.stdout)
	}
	for i, prefix := range want {
		if !strings.HasPrefix(lines[i], prefix) || (prefix == "" && lines[i] != "") {
			t.Fatalf("line %d = %q, want %q:\n%s", i+1, lines[i], prefix, h.stdout)
		}
	}
	if !strings.Contains(lines[3], "(Claude Code ") || !strings.Contains(lines[3], "Codex 2") {
		t.Fatalf("backfill line = %q", lines[3])
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
			if got := strings.Contains(text, "Dashboard: https://beacon.sh/dashboard (sessions appear within about 15 seconds of agent activity)"); got != tc.wantLine {
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
	if !strings.Contains(h.stdout.String(), "Dashboard: https://beacon.sh/dashboard (your recent sessions appear within a minute)") {
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
	for _, want := range []string{"Connected to Beacon Cloud as device dev_7f3a", "Managed privacy: Standard", "Next: run any supported agent"} {
		if !strings.Contains(h.stdout.String(), want) {
			t.Fatalf("missing %q:\n%s", want, h.stdout)
		}
	}
	if strings.Contains(h.stdout.String(), "Dashboard: ") {
		t.Fatalf("the explicit connect gained the wizard's closing:\n%s", h.stdout)
	}
}

func TestCancelledWizardSaysNothingWasInstalled(t *testing.T) {
	calls := recordEndpointLifecycle(t)
	h := newOnboardingHarness(t)
	endpointOpts.harnesses = ""
	h.askErr = onboarding.ErrWizardCancelled
	err := runEndpointInstall(h.cmd, nil)
	want := "setup cancelled; nothing was installed. Run `beacon endpoint install` again when you are ready, or set BEACON_ONBOARDING=0 to install without the guided setup"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
	if len(*calls) != 0 {
		t.Fatalf("a cancelled wizard still installed: %+v", *calls)
	}
}

func TestConnectedSummaryLine(t *testing.T) {
	for _, tc := range []struct {
		enrollment asymptote.Enrollment
		signedInAs string
		want       string
	}{
		{asymptote.Enrollment{DeviceID: "dev_1", Email: "a@example.com", PrivacyMode: "metadata_only"}, "b@example.com",
			"Beacon is installed and connected to Beacon Cloud as a@example.com (device dev_1, Metadata only privacy)."},
		{asymptote.Enrollment{DeviceID: "dev_1"}, "b@example.com",
			"Beacon is installed and connected to Beacon Cloud as b@example.com (device dev_1, Standard privacy)."},
		{asymptote.Enrollment{DeviceID: "dev_1", OrganizationName: "Acme"}, "",
			"Beacon is installed and connected to Beacon Cloud as Acme (device dev_1, Standard privacy)."},
		{asymptote.Enrollment{DeviceID: "dev_1"}, "",
			"Beacon is installed and connected to Beacon Cloud (device dev_1, Standard privacy)."},
	} {
		if got := connectedSummaryLine(tc.enrollment, tc.signedInAs); got != tc.want {
			t.Errorf("connectedSummaryLine(%+v, %q) = %q, want %q", tc.enrollment, tc.signedInAs, got, tc.want)
		}
	}
}

// The wizard's MCP line: where it was added and the one thing left to do. A harness that takes the
// server only by hand gets a pointer to the steps instead of the steps.
func TestBriefCloudMCPSaysItInOneLine(t *testing.T) {
	fx := newMCPFixture(t, "claude_code", "cursor", "copilot_cli")
	mcpIngestURL = func() string { return strings.TrimSuffix(fx.url, "/mcp") }
	var out strings.Builder
	if err := connectCloudMCP(context.Background(), &out, cloudMCPRequest{yes: true, brief: true}); err != nil {
		t.Fatal(err)
	}
	want := "Beacon Cloud MCP added to Claude Code, Cursor; sign in to beacon-managed once in each app (Claude Code: /mcp in a new session).\n" +
		"To use Beacon Cloud MCP in GitHub Copilot CLI, add it by hand: `beacon mcp connect --dry-run` prints the steps.\n"
	if out.String() != want {
		t.Fatalf("brief output:\n%s\nwant:\n%s", out.String(), want)
	}

	out.Reset()
	if err := connectCloudMCP(context.Background(), &out, cloudMCPRequest{yes: true, brief: true, harnesses: []string{"cursor"}}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "Beacon Cloud MCP is already set up in Cursor; sign in to beacon-managed once in each app.\n" {
		t.Fatalf("second run: %q", got)
	}
}

func TestPrintMCPSummaryWithNothingToAdd(t *testing.T) {
	var out strings.Builder
	printMCPSummary(&out, nil, mcpconnect.Options{})
	if got := out.String(); !strings.Contains(got, "not added to any app") || !strings.Contains(got, "beacon mcp connect") {
		t.Fatalf("output = %q", got)
	}
}
