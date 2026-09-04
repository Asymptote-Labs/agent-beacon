package cmd

import (
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
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
	for _, flag := range []string{"dashboard-url", "no-browser", "vector-bin"} {
		if connect.Flags().Lookup(flag) == nil {
			t.Fatalf("connect missing --%s", flag)
		}
	}
	disconnect, _, _ := endpointCmd.Find([]string{"disconnect"})
	if disconnect.Flags().Lookup("keep-credentials") == nil {
		t.Fatal("disconnect missing --keep-credentials")
	}
	for _, removed := range []string{"login", "ingest"} {
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
	})
	for _, want := range []string{"connected to Asymptote Test as device dev-1", "forwarder loaded=true running=true", "credential valid", "buffer 3.0 MiB"} {
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

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{0: "0 B", 512: "512 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 536870912: "512.0 MiB"}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
