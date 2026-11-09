package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
)

func TestLogAccessFlagReachesTheInstallOptions(t *testing.T) {
	old := endpointOpts
	t.Cleanup(func() { endpointOpts = old })
	for flag, want := range map[string]string{"": "", "restricted": "restricted", " Restricted ": "restricted", "shared": "shared"} {
		endpointOpts.logAccess = flag
		if got := endpointInstallOptions(endpointTargetSelection{}, service.KindAuto).LogAccess; got != want {
			t.Errorf("--log-access %q gave LogAccess %q, want %q", flag, got, want)
		}
	}
}

func TestLogAccessFlagIsOnInstallAndRepair(t *testing.T) {
	if endpointInstallCmd.Flags().Lookup("log-access") == nil {
		t.Fatal("endpoint install has no --log-access")
	}
	if endpointRepairCmd.Flags().Lookup("log-access") == nil {
		t.Fatal("endpoint repair has no --log-access")
	}
}

func TestReportLogAccess(t *testing.T) {
	var out bytes.Buffer
	reportLogAccess(&out, lifecycle.InstallResult{LogAccess: "restricted", LogReadersGroupCreated: true})
	text := out.String()
	for _, want := range []string{"restricted", "beacon-log", "Created the beacon-log group", "<account>"} {
		if !strings.Contains(text, want) {
			t.Errorf("restricted report missing %q:\n%s", want, text)
		}
	}
	out.Reset()
	reportLogAccess(&out, lifecycle.InstallResult{LogAccess: "restricted"})
	if strings.Contains(out.String(), "Created") || !strings.Contains(out.String(), "need to be in the beacon-log group") {
		t.Errorf("existing group report:\n%s", out.String())
	}
	out.Reset()
	reportLogAccess(&out, lifecycle.InstallResult{LogAccess: "shared"})
	if !strings.Contains(out.String(), "shared") || !strings.Contains(out.String(), "--log-access restricted") {
		t.Errorf("shared report:\n%s", out.String())
	}
	out.Reset()
	reportLogAccess(&out, lifecycle.InstallResult{})
	if out.Len() != 0 {
		t.Errorf("a user-mode install reported a log access mode: %q", out.String())
	}
}

func TestLogReadersFlag(t *testing.T) {
	for _, cmd := range []string{"install", "repair"} {
		c := endpointInstallCmd
		if cmd == "repair" {
			c = endpointRepairCmd
		}
		if c.Flags().Lookup("log-readers") == nil {
			t.Fatalf("endpoint %s has no --log-readers", cmd)
		}
	}
	old := endpointOpts
	t.Cleanup(func() { endpointOpts = old })
	endpointOpts.logReaders = []string{"alice", "svc_splunk"}
	if got := endpointInstallOptions(endpointTargetSelection{}, service.KindAuto).LogReaders; strings.Join(got, ",") != "alice,svc_splunk" {
		t.Fatalf("--log-readers gave LogReaders %v", got)
	}
}

func TestReportLogAccessNamesTheReadersAdded(t *testing.T) {
	var out bytes.Buffer
	reportLogAccess(&out, lifecycle.InstallResult{
		LogAccess:              "restricted",
		LogReadersGroupCreated: true,
		LogReadersAdded:        []string{"alice"},
		LogReaderErrors:        []string{"could not add nosuchuser to the beacon-log group: user does not exist"},
	})
	text := out.String()
	for _, want := range []string{"Added to the beacon-log group: alice", "next login", "Warning: could not add nosuchuser"} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Created the beacon-log group. Add each account") {
		t.Errorf("report still says the new group has no members after adding alice:\n%s", text)
	}
}
