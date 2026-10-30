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
