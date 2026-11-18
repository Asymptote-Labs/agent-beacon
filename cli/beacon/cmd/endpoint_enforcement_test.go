package cmd

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/edition"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/selfupdate"
)

// enforcementFakes replaces every seam the public enable reaches with a fake that records what
// ran. The defaults describe a connected Mac in system mode with the community edition installed.
type enforcementFakes struct {
	supported, root, connected bool
	userConnected              bool
	userMode                   []string
	installedLine, userLine    string
	locateErr, installErr      error
	rolledBack                 bool

	located, installed []edition.Release
	locatedWith        []string
	handedOff          []string
	// User mode: what was installed where, which binary's enable ran, and what was forwarded.
	userInstalled []string
	userHandedOff []string
	userForwarded [][]string
	// A forward whose arguments include this one fails, as the enterprise binary's exit would.
	userForwardFailsOn string
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
		swapEnforcementSeam(&enforcementUserConnected, func() bool { return f.userConnected }),
		swapEnforcementSeam(&enforcementUserCredentials, func() (string, string, error) {
			return "https://cloud.example", "bcn_device_user", nil
		}),
		swapEnforcementSeam(&userEnterpriseVersionLine, func(context.Context) string { return f.userLine }),
		swapEnforcementSeam(&installUserEnterpriseRelease, func(_ context.Context, r edition.Release, dest string) error {
			f.userInstalled = append(f.userInstalled, r.Version+" "+dest)
			return f.installErr
		}),
		swapEnforcementSeam(&runUserEnforcementEnable, func(_ context.Context, bin, dashboard string) error {
			f.userHandedOff = append(f.userHandedOff, bin+" "+dashboard)
			return nil
		}),
		swapEnforcementSeam(&runUserEnterpriseBeacon, func(_ context.Context, bin string, args ...string) error {
			f.userForwarded = append(f.userForwarded, append([]string{bin}, args...))
			if f.userForwardFailsOn != "" && slices.Contains(args, f.userForwardFailsOn) {
				return errors.New("exit status 1")
			}
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
	// The user endpoint directory, where user-mode enforcement keeps its files.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
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

// writeUserEnforcementFiles puts the kept enterprise binary (and, with on, enforcement's config)
// where user-mode enforcement keeps them, under the test's HOME.
func writeUserEnforcementFiles(t *testing.T, on bool) string {
	t.Helper()
	bin := userEnterpriseBeaconPath()
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if on {
		if err := os.MkdirAll(userEnforcementDir(), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(userEnforcementDir(), userEnforcementConfigName), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return bin
}

func TestEnforcementEnableInUserModeKeepsTheEditionBesideThisOneThenHandsOff(t *testing.T) {
	f := stubEnforcement(t)
	f.root, f.connected, f.userConnected = false, false, true
	out, err := runEnforcementEnableForTest(t)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	bin := userEnterpriseBeaconPath()
	if strings.Join(f.locatedWith, ",") != "https://cloud.example bcn_device_user " {
		t.Fatalf("located with = %q", f.locatedWith)
	}
	if strings.Join(f.userInstalled, ",") != "1.3.41 "+bin {
		t.Fatalf("user installed = %q, want 1.3.41 at %s", f.userInstalled, bin)
	}
	if strings.Join(f.userHandedOff, ",") != bin+" " {
		t.Fatalf("user handed off = %q", f.userHandedOff)
	}
	if len(f.installed)+len(f.handedOff) != 0 {
		t.Fatalf("the system flow ran: installed %v, handed off %v", f.installed, f.handedOff)
	}
	if strings.Contains(out, "bcn_device_user") {
		t.Fatal("the device key reached the output")
	}
}

func TestEnforcementEnableInUserModeSkipsTheDownloadWhenTheKeptEditionIsCurrent(t *testing.T) {
	for line, wantInstall := range map[string]bool{
		"beacon version 1.3.41 (abc) built on today, enterprise edition\n": false,
		"beacon version 1.3.40 (abc) built on today, enterprise edition\n": true,
		"beacon version 1.3.41 (abc) built on today\n":                     true,
		"": true,
	} {
		f := stubEnforcement(t)
		f.root, f.connected, f.userConnected = false, false, true
		f.userLine = line
		if _, err := runEnforcementEnableForTest(t); err != nil {
			t.Fatalf("enable with %q: %v", line, err)
		}
		if got := len(f.userInstalled) == 1; got != wantInstall {
			t.Fatalf("with %q installed %v, want install %v", line, f.userInstalled, wantInstall)
		}
		if len(f.userHandedOff) != 1 {
			t.Fatalf("with %q handed off %v", line, f.userHandedOff)
		}
	}
}

func TestEnforcementEnableSaysWhatToRunForEachMode(t *testing.T) {
	cases := map[string]struct {
		root, connected, userConnected bool
		want, notWant                  string
	}{
		"sudo on a user-mode Mac":  {true, false, true, "run this without sudo", ""},
		"system mode without sudo": {false, true, false, "needs root", ""},
		"connected nowhere":        {false, false, false, "beacon login && beacon endpoint connect", "--system"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := stubEnforcement(t)
			f.root, f.connected, f.userConnected = tc.root, tc.connected, tc.userConnected
			_, err := runEnforcementEnableForTest(t)
			if err == nil || !strings.Contains(err.Error(), tc.want) || (tc.notWant != "" && strings.Contains(err.Error(), tc.notWant)) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if len(f.locatedWith)+len(f.userInstalled)+len(f.userHandedOff) != 0 {
				t.Fatalf("located %v, installed %v, handed off %v", f.locatedWith, f.userInstalled, f.userHandedOff)
			}
		})
	}
}

func TestEnforcementEnableInUserModeSaysHowToReconnectARejectedKey(t *testing.T) {
	f := stubEnforcement(t)
	f.root, f.connected, f.userConnected = false, false, true
	f.locateErr = &edition.HTTPError{Status: http.StatusUnauthorized}
	_, err := runEnforcementEnableForTest(t)
	if err == nil || !strings.Contains(err.Error(), "beacon endpoint connect") || strings.Contains(err.Error(), "sudo") {
		t.Fatalf("err = %v, want a user-mode reconnect", err)
	}
	if len(f.userInstalled)+len(f.userHandedOff) != 0 {
		t.Fatalf("installed %v, handed off %v", f.userInstalled, f.userHandedOff)
	}
}

func TestEnforcementSubcommandsGoToTheKeptEnterpriseBinary(t *testing.T) {
	f := stubEnforcement(t)
	f.root = false
	var out bytes.Buffer
	cmd := &cobra.Command{Use: "enforcement"}
	cmd.SetOut(&out)
	cmd.SetContext(context.Background())

	// Nothing kept: help, nothing run.
	if err := runEndpointEnforcement(cmd, []string{"status"}); err != nil || len(f.userForwarded) != 0 {
		t.Fatalf("err %v, forwarded %v", err, f.userForwarded)
	}

	bin := writeUserEnforcementFiles(t, true)
	if err := runEndpointEnforcement(cmd, []string{"status", "--json"}); err != nil {
		t.Fatalf("forward: %v", err)
	}
	want := []string{bin, "endpoint", "enforcement", "status", "--json"}
	if len(f.userForwarded) != 1 || strings.Join(f.userForwarded[0], " ") != strings.Join(want, " ") {
		t.Fatalf("forwarded %q, want %q", f.userForwarded, want)
	}

	// Never as root, never from a build that has enforcement itself, never when handed work.
	f.root = true
	_ = runEndpointEnforcement(cmd, []string{"status"})
	f.root = false
	activateEnforcement = func(*cobra.Command, string) error { return nil }
	_ = runEndpointEnforcement(cmd, []string{"status"})
	activateEnforcement = nil
	t.Setenv(enforcementHandoffEnv, "1")
	_ = runEndpointEnforcement(cmd, []string{"status"})
	if len(f.userForwarded) != 1 {
		t.Fatalf("forwarded %v, want only the first", f.userForwarded)
	}
}

func TestHookInstallsGoThroughTheKeptEnterpriseBinaryOnlyWhileUserEnforcementIsOn(t *testing.T) {
	f := stubEnforcement(t)
	f.root = false
	user := endpointconfig.Config{UserMode: true, LogPath: "/u/runtime.jsonl"}

	writeUserEnforcementFiles(t, false)
	if _, ok := enforcedHookBeacon(user); ok {
		t.Fatal("went through the enterprise binary with enforcement off")
	}
	bin := writeUserEnforcementFiles(t, true)
	if _, ok := enforcedHookBeacon(endpointconfig.Config{LogPath: "/var/log/beacon-agent/runtime.jsonl"}); ok {
		t.Fatal("went through the enterprise binary for a system-mode install")
	}
	if got, ok := enforcedHookBeacon(user); !ok || got != bin {
		t.Fatalf("enforcedHookBeacon = %q, %v; want %s", got, ok, bin)
	}
}

// hooks install hands the enterprise binary the selection as given: a sweep stays a sweep (so a
// DeepSeek Harness without its bridge is skipped, not fatal), named targets stay named.
func TestHookInstallHandsTheEnterpriseBinaryTheSelectionAsGiven(t *testing.T) {
	f := stubEnforcement(t)
	f.root = false
	bin := writeUserEnforcementFiles(t, true)
	orig := endpointOpts
	t.Cleanup(func() { endpointOpts = orig })
	endpointOpts.userMode, endpointOpts.systemMode, endpointOpts.dryRun = true, false, false
	endpointOpts.hookLevel, endpointOpts.logPath = "user", "/u/runtime.jsonl"
	cmd := &cobra.Command{Use: "install"}
	cmd.SetContext(context.Background())

	endpointOpts.allTargets, endpointOpts.hookHarnesses = true, ""
	if err := runEndpointHooksInstall(cmd, nil); err != nil {
		t.Fatalf("--all: %v", err)
	}
	endpointOpts.allTargets, endpointOpts.hookHarnesses = false, "claude,codex"
	if err := runEndpointHooksInstall(cmd, nil); err != nil {
		t.Fatalf("--harness: %v", err)
	}
	want := []string{
		bin + " endpoint hooks install --all --level user --log-path /u/runtime.jsonl",
		bin + " endpoint hooks install --harness claude,codex --level user --log-path /u/runtime.jsonl",
	}
	if len(f.userForwarded) != len(want) {
		t.Fatalf("forwarded %q, want %q", f.userForwarded, want)
	}
	for i, args := range f.userForwarded {
		if got := strings.Join(args, " "); got != want[i] {
			t.Fatalf("forward %d = %q, want %q", i, got, want[i])
		}
	}
}

// endpoint install still tries every target when they go through the enterprise binary, so one
// runtime's settings file cannot stop the others' hooks.
func TestEndpointInstallHandsOffEachHookTargetOnItsOwn(t *testing.T) {
	f := stubEnforcement(t)
	f.root = false
	bin := writeUserEnforcementFiles(t, true)
	orig := endpointOpts
	t.Cleanup(func() { endpointOpts = orig })
	endpointOpts.userMode, endpointOpts.systemMode = true, false
	endpointOpts.hookLevel, endpointOpts.logPath = "user", "/u/runtime.jsonl"
	f.userForwardFailsOn = "claude"

	var out bytes.Buffer
	installHookTargetsFromEndpointInstall(&bytes.Buffer{}, &out, []string{"claude", "cursor", "codex"})
	var got []string
	for _, args := range f.userForwarded {
		if args[0] != bin {
			t.Fatalf("forwarded to %q, want %s", args[0], bin)
		}
		got = append(got, args[5])
	}
	if strings.Join(got, ",") != "claude,cursor,codex" {
		t.Fatalf("handed off %q, want every target on its own", got)
	}
	if !strings.Contains(out.String(), "Warning: claude hooks were not installed") || !strings.Contains(out.String(), "Retry with: beacon endpoint hooks install --harness claude\n") {
		t.Fatalf("output = %q, want a warning and a retry for claude alone", out.String())
	}
}

func TestRemoveUserEnforcementLeavesTheRestOfTheEndpoint(t *testing.T) {
	stubEnforcement(t)
	writeUserEnforcementFiles(t, true)
	other := filepath.Join(endpointconfig.BaseDir(true), "config.json")
	if err := os.WriteFile(other, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeUserEnforcement(); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{userEnforcementDir(), filepath.Dir(userEnterpriseBeaconPath())} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("%s is still there (%v)", gone, err)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("the endpoint config went too: %v", err)
	}
}
