package service

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// scriptForwarderFixture points the forwarder paths at a temp dir, installs the named forwarders'
// plists and a config dir holding a credential file, and records every launchctl call.
func scriptForwarderFixture(t *testing.T, installed ...string) (configDir string, calls *[]string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("package-helper forwarders are macOS only")
	}
	shrinkLaunchdWaits(t)
	root := t.TempDir()
	oldDaemons, oldConfig := scriptForwarderDaemonsDir, scriptForwarderConfigDir
	scriptForwarderDaemonsDir = filepath.Join(root, "LaunchDaemons")
	scriptForwarderConfigDir = filepath.Join(root, "Forwarders")
	t.Cleanup(func() { scriptForwarderDaemonsDir, scriptForwarderConfigDir = oldDaemons, oldConfig })

	for _, dir := range []string{scriptForwarderDaemonsDir, scriptForwarderConfigDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, label := range installed {
		if err := os.WriteFile(scriptForwarderPlistPath(label), []byte("<plist/>"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(scriptForwarderConfigDir, "s3-vector.env"), []byte("AWS_SECRET_ACCESS_KEY=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	recorded := []string{}
	oldRun := runLaunchctlCommand
	runLaunchctlCommand = func(args ...string) (string, error) {
		recorded = append(recorded, strings.Join(args, " "))
		if args[0] == "print" {
			return "Could not find service", errors.New("exit status 113")
		}
		return "", nil
	}
	t.Cleanup(func() { runLaunchctlCommand = oldRun })
	return scriptForwarderConfigDir, &recorded
}

func TestRemoveScriptForwardersRemovesDaemonsAndCredentials(t *testing.T) {
	configDir, calls := scriptForwarderFixture(t, "com.beacon.endpoint.s3-forwarder")

	if err := RemoveScriptForwarders(false); err != nil {
		t.Fatal(err)
	}
	if want := []string{"bootout system/com.beacon.endpoint.s3-forwarder"}; strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Errorf("launchctl calls = %v, want %v: only the installed forwarder should be booted out", *calls, want)
	}
	if _, err := os.Stat(scriptForwarderPlistPath("com.beacon.endpoint.s3-forwarder")); !os.IsNotExist(err) {
		t.Errorf("the S3 forwarder LaunchDaemon should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(configDir); !os.IsNotExist(err) {
		t.Errorf("the forwarder config dir holds AWS keys and should be removed, stat err = %v", err)
	}
}

func TestRemoveScriptForwardersKeepConfigKeepsCredentials(t *testing.T) {
	configDir, _ := scriptForwarderFixture(t, "com.beacon.endpoint.falcon-forwarder", "com.beacon.endpoint.gcs-forwarder")

	if err := RemoveScriptForwarders(true); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"com.beacon.endpoint.falcon-forwarder", "com.beacon.endpoint.gcs-forwarder"} {
		if _, err := os.Stat(scriptForwarderPlistPath(label)); !os.IsNotExist(err) {
			t.Errorf("%s LaunchDaemon should be removed even when config is kept, stat err = %v", label, err)
		}
	}
	if _, err := os.Stat(filepath.Join(configDir, "s3-vector.env")); err != nil {
		t.Errorf("keepConfig should keep the forwarder config dir: %v", err)
	}
}

func TestRestoreScriptForwardersLoadsOnlyInstalledOnes(t *testing.T) {
	_, calls := scriptForwarderFixture(t, "com.beacon.endpoint.s3-forwarder", "com.beacon.endpoint.gcs-forwarder")

	if err := RestoreScriptForwarders(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"bootstrap system " + scriptForwarderPlistPath("com.beacon.endpoint.s3-forwarder"),
		"bootstrap system " + scriptForwarderPlistPath("com.beacon.endpoint.gcs-forwarder"),
	}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Errorf("launchctl calls = %v, want %v", *calls, want)
	}
}
