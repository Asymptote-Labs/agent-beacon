package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sentinel"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
)

func stubSentinelForwarder(t *testing.T, privileged bool) *sentinel.InstallOptions {
	t.Helper()
	var got sentinel.InstallOptions
	oldInstall, oldPriv := installSentinelForwarder, sentinelHasPrivileges
	installSentinelForwarder = func(o sentinel.InstallOptions) (sentinel.InstallResult, error) {
		got = o
		return sentinel.InstallResult{Config: "/x/vector.toml", Vector: "/opt/beacon/bin/vector",
			Service: service.Status{Label: "BeaconSentinelForwarder", Running: true}}, nil
	}
	sentinelHasPrivileges = func() bool { return privileged }
	t.Cleanup(func() { installSentinelForwarder, sentinelHasPrivileges = oldInstall, oldPriv })
	return &got
}

func runSentinelForwarderCmd(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	saved := endpointOpts
	t.Cleanup(func() { endpointOpts = saved })
	sentinelForwarderOpts = struct {
		endpoint, dcr, tenant, client, certificate, authority, readFrom, vectorBin string
		secretStdin, passwordStdin                                                 bool
	}{}
	cmd := newSentinelForwarderCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

var forwarderArgs = []string{"install", "--system", "--endpoint", "https://dcr.example", "--dcr-immutable-id", "dcr-x", "--tenant-id", "t", "--client-id", "c"}

func TestSentinelForwarderInstallTakesTheSecretFromStdinOrTheEnvironment(t *testing.T) {
	got := stubSentinelForwarder(t, true)
	t.Setenv("AZURE_CLIENT_SECRET", "from-env")
	out, err := runSentinelForwarderCmd(t, "", forwarderArgs...)
	if err != nil || got.ClientSecret != "from-env" || !strings.Contains(out, "Sentinel forwarder installed") {
		t.Fatalf("env secret: %v %+v\n%s", err, *got, out)
	}
	if _, err := runSentinelForwarderCmd(t, "from-stdin\n", append(forwarderArgs, "--client-secret-stdin")...); err != nil || got.ClientSecret != "from-stdin" {
		t.Fatalf("stdin secret: %v %q", err, got.ClientSecret)
	}
	// A certificate install ignores a secret left in the environment.
	if _, err := runSentinelForwarderCmd(t, "", append(forwarderArgs, "--client-certificate", "app.pfx")...); err != nil ||
		got.ClientSecret != "" || got.CertificatePath != "app.pfx" {
		t.Fatalf("certificate: %v %+v", err, *got)
	}
}

func TestSentinelForwarderRefusesUserModeAndUnprivilegedRuns(t *testing.T) {
	stubSentinelForwarder(t, true)
	userArgs := []string{"install", "--endpoint", "https://dcr.example", "--dcr-immutable-id", "dcr-x", "--tenant-id", "t", "--client-id", "c"}
	if _, err := runSentinelForwarderCmd(t, "", userArgs...); err == nil || !strings.Contains(err.Error(), "--system") {
		t.Fatalf("user mode: %v", err)
	}
	stubSentinelForwarder(t, false)
	if _, err := runSentinelForwarderCmd(t, "", forwarderArgs...); err == nil || !strings.Contains(err.Error(), "elevated") {
		t.Fatalf("unprivileged: %v", err)
	}
}

func TestSentinelForwarderStatusLine(t *testing.T) {
	line := sentinelForwarderStatusLine(sentinel.Status{
		Installed:   true,
		Service:     service.Status{Loaded: true, Running: true},
		LastProblem: "2026-11-04T11:00:00Z WARN Retrying after response.",
	})
	if line != "Sentinel forwarder: loaded=true running=true; Vector last reported: 2026-11-04T11:00:00Z WARN Retrying after response." {
		t.Fatalf("line = %q", line)
	}
}
