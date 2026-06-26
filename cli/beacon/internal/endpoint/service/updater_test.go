package service

import (
	"errors"
	"runtime"
	"strings"
	"testing"
)

func TestUpdaterPlistContent(t *testing.T) {
	out := updaterPlist(UpdaterLabel, "/opt/beacon/bin/beacon")
	for _, want := range []string{
		"<string>com.beacon.endpoint.updater</string>",
		"<string>/opt/beacon/bin/beacon</string>",
		"<string>--scheduled</string>",
		"<key>StartCalendarInterval</key>",
		"<key>Hour</key>",
		"<integer>14</integer>",
		"<key>Minute</key>",
		"<integer>0</integer>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plist missing %q", want)
		}
	}
	// One-shot scheduled job: must not RunAtLoad or KeepAlive.
	if strings.Contains(out, "<key>KeepAlive</key>") {
		t.Errorf("updater plist should not set KeepAlive")
	}
	if strings.Contains(out, "<string>--check</string>") {
		t.Errorf("updater plist should let scheduled mode resolve check/apply behavior")
	}
	if !strings.Contains(out, "<key>RunAtLoad</key>\n  <false/>") {
		t.Errorf("updater plist should set RunAtLoad false")
	}
}

func TestUpdaterPlistPath(t *testing.T) {
	if got := (UpdaterManager{}).PlistPath(); got != "/Library/LaunchDaemons/com.beacon.endpoint.updater.plist" {
		t.Errorf("PlistPath = %q", got)
	}
}

func TestUpdaterWritePlistNonDarwinContract(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("darwin writes the plist; this asserts the non-darwin contract")
	}
	if _, err := (UpdaterManager{}).WritePlist("/opt/beacon/bin/beacon"); err == nil {
		t.Error("expected error writing updater plist on non-darwin")
	}
}

func TestUpdaterLoadDoesNotUnloadRunningUpdater(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd updater load is macOS-only")
	}
	var calls []string
	oldRun := runLaunchctlCommand
	runLaunchctlCommand = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if strings.Join(args, " ") == "print system/"+UpdaterLabel {
			return "state = running\npid = 123\n", nil
		}
		return "", errors.New("unexpected launchctl call")
	}
	t.Cleanup(func() {
		runLaunchctlCommand = oldRun
	})

	if err := (UpdaterManager{}).Load(); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(calls) != 1 || calls[0] != "print system/"+UpdaterLabel {
		t.Fatalf("launchctl calls = %#v, want only status print", calls)
	}
}
