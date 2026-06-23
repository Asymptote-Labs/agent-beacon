package service

import (
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
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plist missing %q", want)
		}
	}
	// One-shot scheduled job: must not RunAtLoad or KeepAlive.
	if strings.Contains(out, "<key>KeepAlive</key>") {
		t.Errorf("updater plist should not set KeepAlive")
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
