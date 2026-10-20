package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks/dshtest"
)

const testDshVersion = "0.2.1-rc.2"

// dshBridgeHome isolates HOME, DSH_HOME and PATH so the real dsh on a developer machine is never
// seen, and returns the Harness home and Beacon's hooks file path in it.
func dshBridgeHome(t *testing.T) (dshHome, hooksPath string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dshHome = filepath.Join(home, "dsh-home")
	t.Setenv("DSH_HOME", dshHome)
	t.Setenv("PATH", t.TempDir())
	return dshHome, filepath.Join(dshHome, dshHookFileName)
}

func installDshForTest(t *testing.T) (DshStatus, error) {
	t.Helper()
	return InstallDsh(DshOptions{Level: LevelUser, LogPath: filepath.Join(t.TempDir(), "runtime.jsonl"), UserMode: true})
}

func TestInstallDshRefusesWhenNothingCanLoadTheBridge(t *testing.T) {
	dshHome, hooksPath := dshBridgeHome(t)
	dshtest.InstallCLI(t, testDshVersion)
	dshtest.Profile(t, dshHome, "web")

	_, err := installDshForTest(t)
	var unavailable *DshBridgeUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err = %v, want DshBridgeUnavailableError", err)
	}
	msg := err.Error()
	for _, want := range []string{
		"dsh plugin --profile web add @deepseek-ai/dsh-hooks-claude-code@" + testDshVersion + " @deepseek-ai/dsh-hook-protocol@" + testDshVersion,
		"Beacon does not install packages",
		"beacon endpoint dsh sync",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q does not contain %q", msg, want)
		}
	}
	for _, path := range []string{hooksPath, dshPatchPathFor(hooksPath)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("refused install wrote %s", path)
		}
	}
}

func TestInstallDshMountsWhenAProfileHasAMatchingBridge(t *testing.T) {
	dshHome, hooksPath := dshBridgeHome(t)
	dshtest.InstallCLI(t, testDshVersion)
	dshtest.InstallBridge(t, dshHome, "web", testDshVersion, testDshVersion)
	dshtest.Profile(t, dshHome, "headless")

	status, err := installDshForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Active || status.PathMounted || !dshPatchHasEntry(dshPatchPathFor(hooksPath)) {
		t.Fatalf("status = %+v, want an active install with Beacon's row", status)
	}
	if got := status.Bridge.ProfilesWithoutBridge(); len(got) != 1 || got[0] != "headless" {
		t.Fatalf("profiles without bridge = %v, want [headless]", got)
	}
	after := DshHookStatus(DshOptions{Level: LevelUser})
	if !after.Installed || !after.Active || after.Bridge.DshVersion != testDshVersion {
		t.Fatalf("status after install = %+v", after)
	}
}

func TestInstallDshNamesBothVersionsOnAMismatch(t *testing.T) {
	dshHome, _ := dshBridgeHome(t)
	dshtest.InstallCLI(t, testDshVersion)
	dshtest.InstallBridge(t, dshHome, "web", "0.2.1-alpha.2", "0.2.1-alpha.2")

	_, err := installDshForTest(t)
	if err == nil || !strings.Contains(err.Error(), "dsh is "+testDshVersion) || !strings.Contains(err.Error(), "is 0.2.1-alpha.2") {
		t.Fatalf("err = %v, want both versions named", err)
	}
	report := InspectDshBridge(dshHome, filepath.Join(dshHome, dshHookFileName))
	if report.Ready || report.Profiles[0].Status != DshBridgeVersionMismatch {
		t.Fatalf("report = %+v", report)
	}
}

func TestInspectDshBridgeNeedsTheProtocolPeer(t *testing.T) {
	dshHome, hooksPath := dshBridgeHome(t)
	dshtest.InstallCLI(t, testDshVersion)
	dshtest.InstallBridge(t, dshHome, "web", testDshVersion, "")
	report := InspectDshBridge(dshHome, hooksPath)
	if report.Ready || report.Profiles[0].Status != DshBridgeProtocolMissing {
		t.Fatalf("report = %+v, want protocol_missing", report)
	}
}

// A bridge mounted by absolute path at Beacon's hooks file, in a profile layer, is how a
// declarative setup installs it. Beacon accepts it, writes only the hooks file, and removes a row
// of its own so the bridge is not mounted twice.
func TestInstallDshAcceptsAPathMountAndWritesNoSecondMount(t *testing.T) {
	dshHome, hooksPath := dshBridgeHome(t)
	cli := dshtest.InstallCLI(t, testDshVersion)
	store := t.TempDir()
	bridge := filepath.Join(store, "bridge")
	dshtest.WriteBridge(t, bridge, testDshVersion)
	dshtest.WritePackage(t, filepath.Join(bridge, "node_modules", "@deepseek-ai", "dsh-hook-protocol"), dshtest.ProtocolPackage, testDshVersion)
	profile := dshtest.Profile(t, dshHome, "web")
	layer := filepath.Join(profile, dshPatchFileName)
	writeFile(t, layer, "# managed elsewhere\n- insert:\n    - id: hooks-bridge\n      name: "+filepath.Join(bridge, "lib", "index.js")+"\n      config:\n        configPath: "+hooksPath+"\n")

	// dsh does not hand a bridge mounted by path the packages it ships, so with its dsh peers
	// unresolvable from where it sits, the mount is skipped at boot and is not a route.
	report := InspectDshBridge(dshHome, hooksPath)
	if report.Ready || len(report.PathMounts) != 1 || report.PathMounts[0].Status != DshBridgePeersMissing ||
		!strings.Contains(report.PathMounts[0].Detail, "@deepseek-ai/dsh-llm") {
		t.Fatalf("path mount without its peers = %+v", report.PathMounts)
	}
	// The declarative package links them into the dsh installation.
	for _, peer := range dshtest.HostPeers {
		link := filepath.Join(bridge, "node_modules", filepath.FromSlash(peer))
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(cli, "node_modules", filepath.FromSlash(peer)), link); err != nil {
			t.Fatal(err)
		}
	}
	// A row left by an earlier Beacon install.
	if err := addDshPatchEntry(dshPatchPathFor(hooksPath), hooksPath); err != nil {
		t.Fatal(err)
	}

	status, err := installDshForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	if !status.PathMounted || !status.Active {
		t.Fatalf("status = %+v, want an active path mount", status)
	}
	if dshPatchHasEntry(dshPatchPathFor(hooksPath)) {
		t.Fatal("Beacon's own row is still mounted beside the path mount; every hook would run twice")
	}
	if data, _ := os.ReadFile(layer); !strings.Contains(string(data), "# managed elsewhere") {
		t.Fatal("the profile layer was rewritten")
	}
	after := DshHookStatus(DshOptions{Level: LevelUser})
	if !after.Installed || !after.Active || !after.PathMounted {
		t.Fatalf("status after install = %+v, want installed, active and path-mounted", after)
	}

	// The same mount one version behind is reported, not accepted.
	dshtest.WritePackage(t, bridge, dshtest.BridgePackage, "0.2.1-alpha.2")
	stale := DshHookStatus(DshOptions{Level: LevelUser})
	if !stale.Installed || stale.Active || stale.Bridge.PathMounted() || stale.Bridge.PathMounts[0].Status != DshBridgeVersionMismatch {
		t.Fatalf("stale mount status = %+v", stale)
	}
}

func TestInspectDshBridgeFindsABridgeShippedWithDsh(t *testing.T) {
	dshHome, hooksPath := dshBridgeHome(t)
	pkg := dshtest.InstallCLI(t, testDshVersion)
	dshtest.WritePackage(t, filepath.Join(pkg, "node_modules", "@deepseek-ai", "dsh-hooks-claude-code"), dshtest.BridgePackage, testDshVersion)
	dshtest.WritePackage(t, filepath.Join(pkg, "node_modules", "@deepseek-ai", "dsh-hook-protocol"), dshtest.ProtocolPackage, testDshVersion)
	dshtest.Profile(t, dshHome, "web")
	report := InspectDshBridge(dshHome, hooksPath)
	if !report.Ready || report.Install == nil || report.Install.Status != DshBridgeOK || len(report.ProfilesWithoutBridge()) != 0 {
		t.Fatalf("report = %+v", report)
	}
}

// Package managers such as Nix install dsh behind wrapper scripts that exec another launcher,
// which execs node on the CLI's lib/bin.js. The version is read from that package, not by running dsh.
func TestDshInstallationIsFoundThroughWrapperLaunchers(t *testing.T) {
	_, _ = dshBridgeHome(t)
	root := t.TempDir()
	pkg := filepath.Join(root, "store", "lib", "node_modules", "@deepseek-ai", "dsh")
	dshtest.WritePackage(t, pkg, "@deepseek-ai/dsh", testDshVersion)
	inner := filepath.Join(root, "store", "bin", "dsh")
	writeFile(t, inner, "#!/bin/bash -e\nexec -a \"dsh\" \"/opt/node/bin/node\"  "+filepath.Join(pkg, "lib", "bin.js")+" \"$@\"\n")
	bin := filepath.Join(root, "bin")
	writeFile(t, filepath.Join(bin, "dsh"), "#!/bin/bash\nexport DSH_TELEMETRY_DISABLED=1\nexec "+inner+" \"$@\"\n")
	for _, p := range []string{inner, filepath.Join(bin, "dsh")} {
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	inst := defaultProbeDshInstallation()
	if inst.version != testDshVersion || inst.modules != filepath.Join(pkg, "node_modules") {
		t.Fatalf("installation = %+v", inst)
	}
}

func TestInspectDshBridgeWithoutDshIsUnverified(t *testing.T) {
	dshHome, hooksPath := dshBridgeHome(t)
	dshtest.InstallBridge(t, dshHome, "web", testDshVersion, testDshVersion)
	report := InspectDshBridge(dshHome, hooksPath)
	if !report.Ready || report.DshVersion != "" || report.Profiles[0].Status != DshBridgeUnverified || report.DshVersionError == "" {
		t.Fatalf("report = %+v", report)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
