package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/account"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/managedprivacy"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/onboarding"
)

func TestEndpointConnectAndDisconnectCommandsRegistered(t *testing.T) {
	for _, name := range []string{"connect", "disconnect"} {
		cmd, _, err := endpointCmd.Find([]string{name})
		if err != nil || cmd == nil || cmd.Use != name {
			t.Fatalf("endpoint %s not registered: %v %#v", name, err, cmd)
		}
		for _, flag := range []string{"user", "system", "log-path", "json"} {
			if cmd.Flags().Lookup(flag) == nil {
				t.Fatalf("endpoint %s missing --%s", name, flag)
			}
		}
	}
	connect, _, _ := endpointCmd.Find([]string{"connect"})
	for _, flag := range []string{"dashboard-url", "no-browser", "vector-bin", "privacy-mode"} {
		if connect.Flags().Lookup(flag) == nil {
			t.Fatalf("connect missing --%s", flag)
		}
	}
	disconnect, _, _ := endpointCmd.Find([]string{"disconnect"})
	if disconnect.Flags().Lookup("keep-credentials") == nil {
		t.Fatal("disconnect missing --keep-credentials")
	}
	for _, removed := range []string{"ingest"} {
		if cmd, _, err := rootCmd.Find([]string{removed}); err == nil && cmd != nil && cmd.Use == removed {
			t.Fatalf("removed command %q is still registered", removed)
		}
	}
}

func TestManagedIngestStatusLine(t *testing.T) {
	notConnected := managedIngestStatusLine(asymptote.ManagedIngestStatus{})
	if !strings.Contains(notConnected, "not connected") || !strings.Contains(notConnected, "beacon endpoint connect") {
		t.Fatalf("not connected line = %q", notConnected)
	}
	connected := managedIngestStatusLine(asymptote.ManagedIngestStatus{
		Enabled:          true,
		DeviceID:         "dev-1",
		OrganizationName: "Asymptote Test",
		Forwarder:        service.Status{Loaded: true, Running: true},
		Credential:       "valid",
		BufferBytes:      3 * 1024 * 1024,
		PrivacyMode:      managedprivacy.MetadataOnly,
	})
	for _, want := range []string{"connected to Asymptote Test as device dev-1", "forwarder loaded=true running=true", "credential valid", "buffer 3.0 MiB", "privacy Metadata only"} {
		if !strings.Contains(connected, want) {
			t.Fatalf("connected line missing %q: %s", want, connected)
		}
	}
	revoked := managedIngestStatusLine(asymptote.ManagedIngestStatus{Enabled: true, DeviceID: "dev-1", Credential: "revoked"})
	if !strings.Contains(revoked, "credential revoked") || !strings.Contains(revoked, "re-enroll") {
		t.Fatalf("revoked line = %q", revoked)
	}
	unknown := managedIngestStatusLine(asymptote.ManagedIngestStatus{Enabled: true, DeviceID: "dev-1", Credential: "unknown", CredentialMessage: "ingest service unreachable"})
	if !strings.Contains(unknown, "credential unknown (ingest service unreachable)") {
		t.Fatalf("unknown line = %q", unknown)
	}
	if strings.Contains(connected+revoked+unknown, "bcn_device") {
		t.Fatal("status lines must never carry a device key")
	}
}

func TestConnectUsesSignedInAccountAndSelectedPrivacy(t *testing.T) {
	originalLoad, originalConnect := connectAccountLoad, connectManagedEndpoint
	originalConnectOpts, originalEndpointOpts := connectOpts, endpointOpts
	t.Cleanup(func() {
		connectAccountLoad, connectManagedEndpoint = originalLoad, originalConnect
		connectOpts, endpointOpts = originalConnectOpts, originalEndpointOpts
	})
	connectOpts.privacyMode = "metadata-only"
	connectAccountLoad = func() (*account.Session, error) {
		return &account.Session{
			BaseURL:     "https://beacon.sh",
			AccessToken: "bcn_cli_secret",
			ExpiresAt:   time.Now().Add(time.Hour),
			Scopes:      []string{account.ScopeProfileRead, account.ScopeDeviceEnroll},
			User:        account.User{ID: "usr_1", Email: "person@example.com"},
		}, nil
	}
	var captured asymptote.ConnectOptions
	connectManagedEndpoint = func(_ context.Context, options asymptote.ConnectOptions) (*asymptote.ConnectResult, error) {
		captured = options
		return &asymptote.ConnectResult{
			Enrollment: asymptote.Enrollment{
				DeviceID: "dev-1", DashboardURL: "https://beacon.sh", PrivacyMode: options.PrivacyMode,
			},
			Forwarder:      "test",
			ForwarderState: service.Status{Loaded: true, Running: true},
			VectorConfig:   "/tmp/vector.toml",
			SecretsFile:    "/tmp/secrets.json",
		}, nil
	}
	var out bytes.Buffer
	command := &cobra.Command{}
	command.SetOut(&out)
	command.SetErr(&out)
	if err := connectEndpoint(command, true, "/tmp/runtime.jsonl"); err != nil {
		t.Fatalf("connectEndpoint returned error: %v", err)
	}
	if captured.AccountEnroll == nil || captured.AccountEnroll.AccessToken != "bcn_cli_secret" {
		t.Fatalf("account enrollment = %#v", captured.AccountEnroll)
	}
	if captured.PrivacyMode != managedprivacy.MetadataOnly {
		t.Fatalf("privacy mode = %q", captured.PrivacyMode)
	}
	if strings.Contains(out.String(), "bcn_cli_secret") || !strings.Contains(out.String(), "Managed privacy: Metadata only") {
		t.Fatalf("output = %q", out.String())
	}

	// Setup ends on what to do next, not on more state. This is the moment the user
	// has the most intent and the least idea what happens now, and nothing is
	// forwarded yet: the runtime source starts at the connection point, so the
	// dashboard stays empty until an agent actually runs.
	text := out.String()
	for _, want := range []string{
		"Next: run any supported agent",
		// The recorded service URL is the marketing root; the app is at /dashboard.
		"sessions appear at https://beacon.sh/dashboard",
		"within a minute",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("connect should close on next steps, missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "/dashboard/endpoints") || strings.Contains(text, "Revoke this device") {
		t.Fatalf("the closing lines should be next steps, not device administration:\n%s", text)
	}
	if last := strings.TrimSpace(text); !strings.HasSuffix(last, "within a minute of the activity.") {
		t.Fatalf("the dashboard should be the last thing said:\n%s", text)
	}
}

// Every URL Beacon prints has to be one a browser can actually open. The dashboard
// is a single page at /dashboard whose views are tabs, so the service root is the
// marketing site and deep links like /dashboard/telemetry have no route at all.
func TestDashboardHomeURL(t *testing.T) {
	for base, want := range map[string]string{
		"https://beacon.sh":           "https://beacon.sh/dashboard",
		"https://beacon.sh/":          "https://beacon.sh/dashboard",
		"  https://beacon.sh  ":       "https://beacon.sh/dashboard",
		"https://beacon.sh/dashboard": "https://beacon.sh/dashboard",
		"http://127.0.0.1:8971":       "http://127.0.0.1:8971/dashboard",
		"":                            "https://beacon.sh/dashboard",
	} {
		if got := dashboardHomeURL(base); got != want {
			t.Fatalf("dashboardHomeURL(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestConnectRequiresSignedInAccountInUserMode(t *testing.T) {
	originalLoad, originalConnect := connectAccountLoad, connectManagedEndpoint
	t.Cleanup(func() {
		connectAccountLoad, connectManagedEndpoint = originalLoad, originalConnect
	})
	connectAccountLoad = func() (*account.Session, error) { return nil, account.ErrNotSignedIn }
	connectManagedEndpoint = func(context.Context, asymptote.ConnectOptions) (*asymptote.ConnectResult, error) {
		return nil, errors.New("must not run")
	}
	command := &cobra.Command{}
	err := connectEndpoint(command, true, "/tmp/runtime.jsonl")
	if err == nil || !strings.Contains(err.Error(), "beacon login") {
		t.Fatalf("error = %v", err)
	}
}

func TestSelectedManagedPrivacyModePrefersFlagThenActiveEnrollment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	originalOpts := connectOpts
	t.Cleanup(func() { connectOpts = originalOpts })
	if err := onboarding.Save(onboarding.Profile{Onboarding: onboarding.Onboarding{
		CompletedAt: "2026-09-21T08:00:00Z",
		Destination: onboarding.DestinationAsymptote,
		PrivacyMode: managedprivacy.Standard,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := asymptote.SaveEnrollment(true, asymptote.Enrollment{
		InstallID:   "install-1",
		IngestURL:   "https://ingest.beacon.sh",
		DeviceID:    "dev-1",
		PrivacyMode: managedprivacy.MetadataOnly,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := selectedManagedPrivacyMode(true)
	if err != nil || got != managedprivacy.MetadataOnly {
		t.Fatalf("active enrollment mode = %q, %v", got, err)
	}
	connectOpts.privacyMode = managedprivacy.Standard
	got, err = selectedManagedPrivacyMode(true)
	if err != nil || got != managedprivacy.Standard {
		t.Fatalf("flag mode = %q, %v", got, err)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{0: "0 B", 512: "512 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 536870912: "512.0 MiB"}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
