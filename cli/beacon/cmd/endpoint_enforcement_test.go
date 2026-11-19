package cmd

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/edition"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/selfupdate"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
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
	// How many of those asked for the package a user-mode install takes its beacon from.
	userLocated int
	handedOff   []string
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

	locate := func(_ context.Context, _ *http.Client, dashboard, key, version string) (edition.Release, error) {
		f.locatedWith = append(f.locatedWith, dashboard+" "+key+" "+version)
		if f.locateErr != nil {
			return edition.Release{}, f.locateErr
		}
		f.located = append(f.located, rel)
		return rel, nil
	}
	saved := []func(){
		swapEnforcementSeam(&enforcementPlatformSupported, func() bool { return f.supported }),
		swapEnforcementSeam(&enforcementHasRoot, func() bool { return f.root }),
		swapEnforcementSeam(&enforcementConnected, func() bool { return f.connected }),
		swapEnforcementSeam(&enforcementCredentials, func() (string, string, error) {
			return "https://platform.example", "bcn_device_test", nil
		}),
		swapEnforcementSeam(&enforcementUserModeProbe, func() (string, []string, error) { return "dev", f.userMode, nil }),
		swapEnforcementSeam(&installedBeaconVersionLine, func(context.Context) string { return f.installedLine }),
		swapEnforcementSeam(&locateEnterpriseRelease, locate),
		swapEnforcementSeam(&locateUserEnterpriseRelease, func(ctx context.Context, c *http.Client, dashboard, key, version string) (edition.Release, error) {
			f.userLocated++
			return locate(ctx, c, dashboard, key, version)
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
	system := t.TempDir()
	orig := enforcementSystemBaseDir
	enforcementSystemBaseDir = func() string { return system }
	t.Cleanup(func() { enforcementSystemBaseDir = orig })
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
	if strings.Join(f.locatedWith, ",") != "https://platform.example bcn_device_test " || f.userLocated != 0 {
		t.Fatalf("located with = %q (%d for user mode)", f.locatedWith, f.userLocated)
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
		"unsupported":   {func(f *enforcementFakes) { f.supported = false }, "macOS on Apple Silicon and for Linux on amd64 and arm64 only"},
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
		if err := os.WriteFile(filepath.Join(userEnforcementDir(), enforcementConfigName), []byte("{}\n"), 0o644); err != nil {
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
	if strings.Join(f.locatedWith, ",") != "https://cloud.example bcn_device_user " || f.userLocated != 1 {
		t.Fatalf("located with = %q (%d for user mode)", f.locatedWith, f.userLocated)
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
	if _, ok := enforcedHookBeacon(context.Background(), &bytes.Buffer{}, user); ok {
		t.Fatal("went through the enterprise binary with enforcement off")
	}
	bin := writeUserEnforcementFiles(t, true)
	if _, ok := enforcedHookBeacon(context.Background(), &bytes.Buffer{}, endpointconfig.Config{LogPath: "/var/log/beacon-agent/runtime.jsonl"}); ok {
		t.Fatal("went through the enterprise binary for a system-mode install")
	}
	if got, ok := enforcedHookBeacon(context.Background(), &bytes.Buffer{}, user); !ok || got != bin {
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

// Without the enterprise binary, enforcement status still says whether enforcement is on (for the
// user or the machine), and endpoint status says so on a Mac that can have it.
func TestEnforcementStatusWithoutTheEnterpriseBinarySaysWhetherItIsOn(t *testing.T) {
	f := stubEnforcement(t)
	f.root = false
	status := func(args ...string) string {
		var out bytes.Buffer
		cmd := &cobra.Command{Use: "enforcement"}
		cmd.SetOut(&out)
		cmd.SetContext(context.Background())
		if err := runEndpointEnforcement(cmd, append([]string{"status"}, args...)); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if got := status(); got != "Enforcement:   off\n" {
		t.Fatalf("status = %q", got)
	}
	if got := enforcementStatusLine(); got != "Enforcement: off" {
		t.Fatalf("endpoint status line = %q", got)
	}
	f.supported = false
	if got := enforcementStatusLine(); got != "" {
		t.Fatalf("endpoint status line where enforcement cannot run = %q", got)
	}

	systemDir := filepath.Join(enforcementSystemBaseDir(), enforcementDirName)
	if err := os.MkdirAll(systemDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(systemDir, enforcementConfigName), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "Enforcement:   on\n" {
		t.Fatalf("status with system enforcement = %q", got)
	}
	if got := status("--json"); got != "{\"enabled\":true}\n" {
		t.Fatalf("status --json = %q", got)
	}
	if got := enforcementStatusLine(); got != "Enforcement: on" {
		t.Fatalf("endpoint status line = %q", got)
	}
	if len(f.userForwarded) != 0 {
		t.Fatalf("forwarded %v", f.userForwarded)
	}
}

// After a community upgrade, the kept enterprise binary is brought up to this build's release
// before it is handed a hook install; never down to it, and when the release cannot be had the
// install goes ahead with the kept binary and a warning.
func TestHookInstallsUpdateAnOlderKeptEnterpriseBinaryFirst(t *testing.T) {
	f := stubEnforcement(t)
	f.root = false
	bin := writeUserEnforcementFiles(t, true)
	user := endpointconfig.Config{UserMode: true, LogPath: "/u/runtime.jsonl"}
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })
	version.Version = "1.3.70"

	for line, wantInstall := range map[string]bool{
		"beacon version 1.3.69 (abc) built on today, enterprise edition\n": true,
		"beacon version 1.3.70 (abc) built on today, enterprise edition\n": false,
		"beacon version 1.3.71 (abc) built on today, enterprise edition\n": false,
	} {
		f.userLine, f.userInstalled, f.locatedWith = line, nil, nil
		var warnings bytes.Buffer
		if got, ok := enforcedHookBeacon(context.Background(), &warnings, user); !ok || got != bin {
			t.Fatalf("with %q: %q, %v", line, got, ok)
		}
		if got := len(f.userInstalled) == 1; got != wantInstall {
			t.Fatalf("with %q installed %v, want install %v", line, f.userInstalled, wantInstall)
		}
		if wantInstall && !strings.HasSuffix(f.locatedWith[0], " 1.3.70") {
			t.Fatalf("located %q, want this build's release", f.locatedWith)
		}
		if warnings.Len() != 0 {
			t.Fatalf("with %q warned %q", line, warnings.String())
		}
	}

	f.userLine, f.userInstalled = "beacon version 1.3.69 (abc) built on today, enterprise edition\n", nil
	f.locateErr = &edition.HTTPError{Status: http.StatusNotFound}
	var warnings bytes.Buffer
	if _, ok := enforcedHookBeacon(context.Background(), &warnings, user); !ok {
		t.Fatal("no hand-off when the update failed")
	}
	if len(f.userInstalled) != 0 || !strings.Contains(warnings.String(), "Retry later with: beacon endpoint enforcement enable") {
		t.Fatalf("installed %v, warnings %q", f.userInstalled, warnings.String())
	}
}

func TestEnforcementPlatformsAreAppleSiliconAndLinux(t *testing.T) {
	want := (runtime.GOOS == "darwin" && runtime.GOARCH == "arm64") ||
		(runtime.GOOS == "linux" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"))
	if got := defaultEnforcementPlatformSupported(); got != want {
		t.Fatalf("supported on %s/%s = %v, want %v", runtime.GOOS, runtime.GOARCH, got, want)
	}
}

// The way out of user mode names this platform's service manager.
func TestUserModeActiveErrorNamesThisPlatformsServices(t *testing.T) {
	msg := userModeActiveError("dev", []string{"something"}).Error()
	if runtime.GOOS == "linux" {
		if !strings.Contains(msg, "systemctl --user disable --now beacon-collector.service beacon-asymptote-forwarder.service beacon-inventory.timer beacon-session-sync.timer") || strings.Contains(msg, "launchctl") {
			t.Fatalf("message = %q", msg)
		}
		return
	}
	if !strings.Contains(msg, "launchctl bootout gui/$(id -u)/com.beacon.endpoint.collector.user") || strings.Contains(msg, "systemctl") {
		t.Fatalf("message = %q", msg)
	}
}

// A unit counts only where Beacon installed it for the user; one whose state systemd cannot report
// (older than 248 cannot reach another user's manager) is not taken for stopped.
func TestLinuxUserModeUnitsAreReportedUnlessSystemdSaysTheyAreStopped(t *testing.T) {
	home := t.TempDir()
	info := consoleUserInfo{Username: "alice", HomeDir: home}
	units := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := linuxUserModeUnits(info, func(string, string) string { return "active" }); len(got) != 0 {
		t.Fatalf("without unit files: %v", got)
	}
	wants := filepath.Join(units, "default.target.wants")
	if err := os.MkdirAll(wants, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, unit := range []string{"beacon-collector.service", "beacon-asymptote-forwarder.service"} {
		if err := os.WriteFile(filepath.Join(units, unit), []byte("[Unit]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wants, unit), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for state, want := range map[string]string{
		"active":   "beacon-collector.service is running in alice's session",
		"inactive": "",
		"failed":   "",
		"":         "beacon-collector.service is enabled for alice (" + filepath.Join(units, "beacon-collector.service") + "), and systemd could not say whether it is running",
	} {
		var asked []string
		got := linuxUserModeUnits(info, func(username, unit string) string {
			asked = append(asked, username+" "+unit)
			return state
		})
		if strings.Join(asked, ",") != "alice beacon-collector.service,alice beacon-asymptote-forwarder.service" {
			t.Fatalf("%q: asked %v", state, asked)
		}
		if want == "" {
			if len(got) != 0 {
				t.Fatalf("%q: %v", state, got)
			}
			continue
		}
		if len(got) != 2 || got[0] != want {
			t.Fatalf("%q: %v, want %q first", state, got, want)
		}
	}
}

// Where systemd cannot report a state, a unit stopped and disabled as userModeActiveError says
// (`systemctl --user disable --now`, which leaves the unit file) no longer keeps enable refused.
func TestLinuxUserModeUnitsLetADisabledUnitGo(t *testing.T) {
	home := t.TempDir()
	units := filepath.Join(home, ".config", "systemd", "user")
	wants := filepath.Join(units, "default.target.wants")
	if err := os.MkdirAll(wants, 0o755); err != nil {
		t.Fatal(err)
	}
	unit := "beacon-collector.service"
	if err := os.WriteFile(filepath.Join(units, unit), []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(units, unit), filepath.Join(wants, unit)); err != nil {
		t.Fatal(err)
	}
	info := consoleUserInfo{Username: "alice", HomeDir: home}
	unknown := func(string, string) string { return "" }
	if got := linuxUserModeUnits(info, unknown); len(got) != 1 || !strings.Contains(got[0], "is enabled for alice") {
		t.Fatalf("enabled: %v", got)
	}
	if err := os.Remove(filepath.Join(wants, unit)); err != nil { // what `disable` does
		t.Fatal(err)
	}
	if got := linuxUserModeUnits(info, unknown); len(got) != 0 {
		t.Fatalf("after disable: %v", got)
	}
	if msg := userModeActiveError("alice", []string{"x"}).Error(); runtime.GOOS == "linux" && !strings.Contains(msg, "systemctl --user disable --now "+unit) {
		t.Fatalf("the fix it prints = %q", msg)
	}
}

// Every user the system install configures is asked about, not only the console's.
func TestUserModeProbeAsksEveryConsoleUser(t *testing.T) {
	defer swapEnforcementSeam(&activeConsoleUsers, func() ([]consoleUserInfo, error) {
		return []consoleUserInfo{{Username: "bob"}, {Username: "alice"}, {Username: "carol"}}, nil
	})()
	defer swapEnforcementSeam(&consoleUserModeFindings, func(info consoleUserInfo) []string {
		if info.Username == "bob" {
			return nil
		}
		return []string{"user mode for " + info.Username}
	})()
	who, found, err := probeConsoleUserMode()
	if err != nil || who != "alice, carol" || strings.Join(found, ",") != "user mode for alice,user mode for carol" {
		t.Fatalf("probe = %q %v %v", who, found, err)
	}
	if msg := userModeActiveError(who, found).Error(); !strings.Contains(msg, "As each of alice, carol:") {
		t.Fatalf("message = %q", msg)
	}
}
