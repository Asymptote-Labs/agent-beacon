package cmd

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/edition"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/selfupdate"
)

// enforcementFakes replaces every seam the public enable reaches with a fake that records what
// ran. The defaults describe a connected Mac in system mode with the community edition installed.
type enforcementFakes struct {
	supported, root, connected bool
	userMode                   []string
	installedLine              string
	locateErr, installErr      error
	rolledBack                 bool

	located, installed []edition.Release
	locatedWith        []string
	handedOff          []string
}

func stubEnforcement(t *testing.T) *enforcementFakes {
	t.Helper()
	f := &enforcementFakes{
		supported: true, root: true, connected: true,
		installedLine: "beacon version 1.3.40 (abc) built on today\n",
	}
	rel := edition.Release{Version: "1.3.41", URL: "https://x.example/p", SHA256: strings.Repeat("a", 64), Size: 74_700_000}

	saved := []func(){
		swapEnforcementSeam(&enforcementPlatformSupported, func() bool { return f.supported }),
		swapEnforcementSeam(&enforcementHasRoot, func() bool { return f.root }),
		swapEnforcementSeam(&enforcementConnected, func() bool { return f.connected }),
		swapEnforcementSeam(&enforcementCredentials, func() (string, string, error) {
			return "https://platform.example", "bcn_device_test", nil
		}),
		swapEnforcementSeam(&enforcementUserModeProbe, func() (string, []string, error) { return "dev", f.userMode, nil }),
		swapEnforcementSeam(&installedBeaconVersionLine, func(context.Context) string { return f.installedLine }),
		swapEnforcementSeam(&locateEnterpriseRelease, func(_ context.Context, _ *http.Client, dashboard, key, version string) (edition.Release, error) {
			f.locatedWith = append(f.locatedWith, dashboard+" "+key+" "+version)
			if f.locateErr != nil {
				return edition.Release{}, f.locateErr
			}
			f.located = append(f.located, rel)
			return rel, nil
		}),
		swapEnforcementSeam(&installEnterpriseRelease, func(_ context.Context, r edition.Release, _, _ string) (selfupdate.ApplyResult, error) {
			f.installed = append(f.installed, r)
			if f.installErr != nil {
				return selfupdate.ApplyResult{RolledBack: f.rolledBack}, f.installErr
			}
			return selfupdate.ApplyResult{Applied: true, Verification: "sha256+notarization"}, nil
		}),
		swapEnforcementSeam(&runInstalledEnforcementEnable, func(_ context.Context, dashboard string) error {
			f.handedOff = append(f.handedOff, dashboard)
			return nil
		}),
		swapEnforcementSeam(&activateEnforcement, nil),
	}
	t.Cleanup(func() {
		for _, restore := range saved {
			restore()
		}
		endpointEnforcementOpts.dashboardURL = ""
	})
	t.Setenv(enforcementHandoffEnv, "")
	return f
}

func swapEnforcementSeam[T any](target *T, value T) func() {
	old := *target
	*target = value
	return func() { *target = old }
}

func runEnforcementEnableForTest(t *testing.T) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetContext(context.Background())
	err := runEndpointEnforcementEnable(cmd, nil)
	return out.String(), err
}

func TestEnforcementEnableInstallsTheEnterpriseEditionThenHandsOff(t *testing.T) {
	f := stubEnforcement(t)
	out, err := runEnforcementEnableForTest(t)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if len(f.installed) != 1 || f.installed[0].Version != "1.3.41" {
		t.Fatalf("installed = %+v", f.installed)
	}
	if strings.Join(f.locatedWith, ",") != "https://platform.example bcn_device_test " {
		t.Fatalf("located with = %q", f.locatedWith)
	}
	if len(f.handedOff) != 1 || f.handedOff[0] != "" {
		t.Fatalf("handed off = %q", f.handedOff)
	}
	if strings.Contains(out, "bcn_device_test") {
		t.Fatal("the device key reached the output")
	}
}

func TestEnforcementEnableSkipsTheInstallWhenTheEnterpriseEditionIsInstalled(t *testing.T) {
	f := stubEnforcement(t)
	f.installedLine = "beacon version 1.3.41 (abc) built on today, enterprise edition\n"
	endpointEnforcementOpts.dashboardURL = "https://other.example"
	out, err := runEnforcementEnableForTest(t)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if len(f.locatedWith) != 0 || len(f.installed) != 0 {
		t.Fatalf("located %v, installed %v", f.locatedWith, f.installed)
	}
	if len(f.handedOff) != 1 || f.handedOff[0] != "https://other.example" {
		t.Fatalf("out %q, handed off %q", out, f.handedOff)
	}
}

func TestEnforcementEnableUsesTheDashboardFlag(t *testing.T) {
	f := stubEnforcement(t)
	endpointEnforcementOpts.dashboardURL = "https://other.example"
	if _, err := runEnforcementEnableForTest(t); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !strings.HasPrefix(f.locatedWith[0], "https://other.example ") {
		t.Fatalf("located with %q", f.locatedWith)
	}
}

func TestEnforcementEnableRefusesBeforeAskingThePlatform(t *testing.T) {
	cases := map[string]struct {
		set  func(*enforcementFakes)
		want string
	}{
		"not a Mac":     {func(f *enforcementFakes) { f.supported = false }, "macOS on Apple Silicon only"},
		"not root":      {func(f *enforcementFakes) { f.root = false }, "needs root"},
		"not connected": {func(f *enforcementFakes) { f.connected = false }, "endpoint connect --system"},
		"user mode": {func(f *enforcementFakes) {
			f.userMode = []string{"com.beacon.endpoint.collector.user is loaded in dev's session"}
		}, "running in user mode for dev"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := stubEnforcement(t)
			tc.set(f)
			_, err := runEnforcementEnableForTest(t)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if len(f.locatedWith)+len(f.installed)+len(f.handedOff) != 0 {
				t.Fatalf("located %v, installed %v, handed off %v", f.locatedWith, f.installed, f.handedOff)
			}
		})
	}
}

// A platform refusal stops before anything is installed or handed off, and passes the platform's
// reason through.
func TestEnforcementEnableStopsWhenThePlatformRefuses(t *testing.T) {
	f := stubEnforcement(t)
	f.locateErr = &edition.HTTPError{Status: 403, Detail: "policy enforcement is not enabled for this organization"}
	_, err := runEnforcementEnableForTest(t)
	if err == nil || !strings.Contains(err.Error(), "policy enforcement is not enabled for this organization") {
		t.Fatalf("err = %v, want the platform's reason", err)
	}
	if len(f.installed)+len(f.handedOff) != 0 {
		t.Fatalf("installed %v, handed off %v", f.installed, f.handedOff)
	}
}

func TestEnforcementEnableStopsWhenTheInstallFails(t *testing.T) {
	f := stubEnforcement(t)
	f.installErr, f.rolledBack = errors.New("verify checksum: sha256 mismatch"), true
	_, err := runEnforcementEnableForTest(t)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("err = %v", err)
	}
	if len(f.handedOff) != 0 {
		t.Fatalf("handed off %v", f.handedOff)
	}
}

func TestEnforcementEnableRunsTheBuildsOwnActivation(t *testing.T) {
	f := stubEnforcement(t)
	var activated []string
	activateEnforcement = func(_ *cobra.Command, dashboard string) error {
		activated = append(activated, dashboard)
		return nil
	}
	endpointEnforcementOpts.dashboardURL = "https://other.example"
	f.root = false // the build's own activation does its own checks
	if _, err := runEnforcementEnableForTest(t); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if strings.Join(activated, ",") != "https://other.example" || len(f.locatedWith)+len(f.handedOff) != 0 {
		t.Fatalf("activated %v, located %v, handed off %v", activated, f.locatedWith, f.handedOff)
	}
}

func TestEnforcementEnableRefusesAHandoffToABuildWithoutEnforcement(t *testing.T) {
	f := stubEnforcement(t)
	t.Setenv(enforcementHandoffEnv, "1")
	if _, err := runEnforcementEnableForTest(t); err == nil {
		t.Fatal("a handed-off enable on a build without enforcement succeeded")
	}
	if len(f.locatedWith)+len(f.installed)+len(f.handedOff) != 0 {
		t.Fatal("a handed-off enable must never install again")
	}
}
