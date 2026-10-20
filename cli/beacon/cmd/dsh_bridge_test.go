package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/dshsession"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks/dshtest"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
)

// dshBridgeTestHome isolates the Harness home and hides any real dsh on PATH.
func dshBridgeTestHome(t *testing.T) (home, dshHome string, cfg endpointconfig.Config) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dshHome = filepath.Join(home, ".dsh")
	t.Setenv("DSH_HOME", dshHome)
	t.Setenv("PATH", t.TempDir())
	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })
	endpointOpts.hookLevel = "user"
	return home, dshHome, endpointconfig.Config{LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}
}

// Named explicitly, a DeepSeek Harness that cannot load the bridge is an error; swept up with
// other targets it is skipped with the reason, and the targets after it still install.
func TestHooksInstallRefusesDshExplicitlyAndSkipsItInASweep(t *testing.T) {
	home, dshHome, cfg := dshBridgeTestHome(t)
	dshtest.InstallCLI(t, "0.2.1-rc.2")
	dshtest.Profile(t, dshHome, "web")

	var warnings bytes.Buffer
	err := installEndpointHookTargets(&warnings, []string{"dsh"}, cfg, false)
	var unavailable *endpointhooks.DshBridgeUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("explicit install err = %v, want DshBridgeUnavailableError", err)
	}

	err = installEndpointHookTargets(&warnings, []string{"dsh", "kiro"}, cfg, true)
	if err != nil {
		t.Fatalf("sweep err = %v, want dsh skipped", err)
	}
	out := warnings.String()
	if !strings.Contains(out, "Warning: skipped DeepSeek Harness") || !strings.Contains(out, "dsh plugin --profile") {
		t.Fatalf("warning = %q", out)
	}
	if strings.Contains(strings.ToLower(out), "beacon will install") || strings.Contains(out, "npm install") {
		t.Fatalf("warning suggests Beacon installs packages: %q", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".kiro", "hooks", "beacon-endpoint.json")); err != nil {
		t.Fatalf("the target after dsh was not installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dshHome, "cordis.patch.yml")); !os.IsNotExist(err) {
		t.Fatal("the skipped dsh install wrote a mount")
	}
}

// Endpoint install sweeps detected runtimes and turns a target error into a warning; the dsh
// refusal must come through that path as a warning that leaves the install successful.
func TestEndpointInstallSkipsDshWithoutABridge(t *testing.T) {
	_, dshHome, _ := dshBridgeTestHome(t)
	dshtest.InstallCLI(t, "0.2.1-rc.2")
	dshtest.Profile(t, dshHome, "web")
	var report, out bytes.Buffer
	installHookTargetsFromEndpointInstall(&report, &out, []string{"dsh"})
	if !strings.Contains(out.String(), "Warning: dsh hooks were not installed") || !strings.Contains(out.String(), "Beacon does not install packages") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestDshHookBridgeDoctorCheck(t *testing.T) {
	_, dshHome, cfg := dshBridgeTestHome(t)
	dshtest.InstallCLI(t, "0.2.1-rc.2")
	dshtest.InstallBridge(t, dshHome, "web", "0.2.1-rc.2", "0.2.1-rc.2")
	dshtest.Profile(t, dshHome, "headless")
	if err := installEndpointHookTarget("dsh", cfg); err != nil {
		t.Fatal(err)
	}

	check, ok := dshHookBridgeCheck()
	if !ok || check.Name != "dsh_hook_bridge" || check.Status != diagnostics.StatusWarn || check.Evidence != "dsh_hook_bridge_partial" ||
		!strings.Contains(check.Action, "dsh plugin --profile headless add") {
		t.Fatalf("partial check = %+v", check)
	}

	dshtest.InstallBridge(t, dshHome, "headless", "0.2.1-rc.2", "0.2.1-rc.2")
	if check, _ := dshHookBridgeCheck(); check.Status != diagnostics.StatusOK {
		t.Fatalf("complete check = %+v", check)
	}

	// dsh upgraded under an installed bridge: both versions are named.
	dshtest.InstallCLI(t, "0.2.2")
	check, _ = dshHookBridgeCheck()
	if check.Status != diagnostics.StatusWarn || check.Evidence != "dsh_hook_bridge_unavailable" ||
		!strings.Contains(check.Message, "dsh is 0.2.2") || !strings.Contains(check.Message, "0.2.1-rc.2") ||
		!strings.Contains(check.Action, "Beacon does not install packages") {
		t.Fatalf("mismatch check = %+v", check)
	}
}

func TestDshHookBridgeDoctorCheckSkipsWithoutHooks(t *testing.T) {
	_, dshHome, _ := dshBridgeTestHome(t)
	dshtest.InstallCLI(t, "0.2.1-rc.2")
	dshtest.Profile(t, dshHome, "web")
	if _, ok := dshHookBridgeCheck(); ok {
		t.Fatal("bridge check ran with no dsh hooks installed")
	}
}

// When dsh cannot load the bridge, every session runs without hook events, and dsh_hook_capture
// would blame the sandbox for it. Doctor reports the bridge and leaves the capture check out; with
// a working bridge both run.
func TestDoctorReportsTheBridgeInsteadOfBlamingTheSandbox(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	origBridge, origCapture := doctorDshHookBridgeCheck, doctorDshHookCaptureCheck
	t.Cleanup(func() { doctorDshHookBridgeCheck, doctorDshHookCaptureCheck = origBridge, origCapture })
	captureCalls := 0
	doctorDshHookCaptureCheck = func(string) (diagnostics.Check, bool) {
		captureCalls++
		return diagnostics.Check{Name: "dsh_hook_capture", Target: dshsession.Harness, Status: diagnostics.StatusWarn}, true
	}
	bridge := diagnostics.Check{Name: "dsh_hook_bridge", Target: dshsession.Harness, Status: diagnostics.StatusWarn, Evidence: "dsh_hook_bridge_unavailable"}
	doctorDshHookBridgeCheck = func() (diagnostics.Check, bool) { return bridge, true }

	status := lifecycle.Status{
		LogPath:    filepath.Join(home, "runtime.jsonl"),
		RuntimeLog: lifecycle.RuntimeLogSource{EffectiveUserMode: true},
		Harnesses:  []harness.Harness{{Name: dshsession.Harness, Detected: true, TelemetryStatus: harness.TelemetryEnabled}},
	}
	names := func() []string {
		var out []string
		for _, c := range buildDoctorResult(status, time.Now()).Checks {
			if strings.HasPrefix(c.Name, "dsh_") {
				out = append(out, c.Name)
			}
		}
		return out
	}
	if got := names(); len(got) != 1 || got[0] != "dsh_hook_bridge" || captureCalls != 0 {
		t.Fatalf("checks = %v (capture calls %d), want only dsh_hook_bridge", got, captureCalls)
	}
	bridge.Evidence, bridge.Status = "dsh_hook_bridge_loads", diagnostics.StatusOK
	if got := names(); len(got) != 2 || captureCalls != 1 {
		t.Fatalf("checks = %v with a loadable bridge, want both", got)
	}
}
