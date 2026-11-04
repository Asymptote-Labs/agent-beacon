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
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

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
	if err := connectEndpoint(command, true, "/tmp/runtime.jsonl", false); err != nil {
		t.Fatalf("connectEndpoint returned error: %v", err)
	}
	if captured.AccountEnroll == nil || captured.AccountEnroll.AccessToken != "bcn_cli_secret" {
		t.Fatalf("account enrollment = %#v", captured.AccountEnroll)
	}
	if captured.PrivacyMode != managedprivacy.MetadataOnly {
		t.Fatalf("privacy mode = %q", captured.PrivacyMode)
	}
	if strings.Contains(out.String(), "bcn_cli_secret") {
		t.Fatalf("connect output leaks the account token: %q", out.String())
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
	err := connectEndpoint(command, true, "/tmp/runtime.jsonl", false)
	if err == nil || !strings.Contains(err.Error(), "beacon login") {
		t.Fatalf("error = %v", err)
	}
}

func TestSelectedManagedPrivacyModePrefersFlagThenActiveEnrollment(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
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

func TestCloudStatusLineOnlyWhereBeaconCloudApplies(t *testing.T) {
	notConnected := asymptote.ManagedIngestStatus{}
	if line := cloudStatusLine(notConnected, false, false); line != "" {
		t.Errorf("system install, not connected: line = %q, want none (MDM rollouts should not be told to connect)", line)
	}
	if line := cloudStatusLine(notConnected, true, true); line != "" {
		t.Errorf("own forwarder, not connected: line = %q, want none", line)
	}
	if line := cloudStatusLine(notConnected, true, false); !strings.Contains(line, "beacon endpoint connect") {
		t.Errorf("per-user install, not connected: line = %q, want the connect hint", line)
	}
	connected := asymptote.ManagedIngestStatus{Enabled: true, DeviceID: "dev-1"}
	if line := cloudStatusLine(connected, false, true); !strings.Contains(line, "Beacon Cloud: connected") {
		t.Errorf("system install, connected: line = %q, want the connected line", line)
	}
	pending := asymptote.ManagedIngestStatus{Message: "connect did not finish"}
	if line := cloudStatusLine(pending, false, false); !strings.Contains(line, "connect did not finish") {
		t.Errorf("system install, incomplete connect: line = %q, want its message", line)
	}
}
