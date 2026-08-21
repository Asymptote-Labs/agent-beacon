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
		"<array>",
		"<key>Minute</key>",
		"<integer>0</integer>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plist missing %q", want)
		}
	}
	for _, hour := range []string{"9", "12", "15", "18", "21"} {
		if !strings.Contains(out, "<integer>"+hour+"</integer>") {
			t.Errorf("plist missing hour %s", hour)
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
	// The launchd and systemd unit locations are absolute POSIX system paths, and these assertions
	// are about those exact strings. filepath.Join renders them with backslashes on Windows, so the
	// comparison there would be testing path rendering rather than the contract -- and neither
	// service manager exists on that platform to have a contract about.
	if runtime.GOOS == "windows" {
		t.Skip("launchd and systemd unit paths are POSIX-only")
	}
	if got := (UpdaterManager{Kind: KindLaunchd}).UnitPath(); got != "/Library/LaunchDaemons/com.beacon.endpoint.updater.plist" {
		t.Errorf("PlistPath = %q", got)
	}
}

func TestUpdaterLoadDefersReloadForRunningUpdater(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd updater load is macOS-only")
	}
	var calls []string
	oldRun := runLaunchctlCommand
	oldDeferred := startDeferredUpdaterReload
	runLaunchctlCommand = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if strings.Join(args, " ") == "print system/"+UpdaterLabel {
			return "state = running\npid = 123\n", nil
		}
		return "", errors.New("unexpected launchctl call")
	}
	var deferredPath string
	startDeferredUpdaterReload = func(path string) error {
		deferredPath = path
		return nil
	}
	t.Cleanup(func() {
		runLaunchctlCommand = oldRun
		startDeferredUpdaterReload = oldDeferred
	})

	if err := (UpdaterManager{Kind: KindLaunchd}).Load(); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(calls) != 1 || calls[0] != "print system/"+UpdaterLabel {
		t.Fatalf("launchctl calls = %#v, want only status print", calls)
	}
	if deferredPath != (UpdaterManager{Kind: KindLaunchd}).UnitPath() {
		t.Fatalf("deferred reload path = %q, want updater plist", deferredPath)
	}
}

func TestDeferredUpdaterReloadScriptWaitsForParentExit(t *testing.T) {
	script := deferredUpdaterReloadScript("/Library/LaunchDaemons/com.beacon.endpoint.updater.plist")
	for _, want := range []string{
		"/bin/launchctl print system/" + UpdaterLabel,
		"/usr/bin/grep -Eq 'state = running|pid ='",
		"SECONDS+600",
		"do sleep 2; done",
		"/bin/launchctl bootout system/" + UpdaterLabel,
		"/bin/launchctl bootstrap system '/Library/LaunchDaemons/com.beacon.endpoint.updater.plist'",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("deferred reload script missing %q:\n%s", want, script)
		}
	}
}

func TestUpdaterLaunchdUnsupportedOffDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("launchd is supported here")
	}
	if _, err := (UpdaterManager{Kind: KindLaunchd}).WriteUnit("/opt/beacon/bin/beacon"); err == nil {
		t.Error("launchd updater must not claim success off darwin")
	}
}

// Supervised mode has no scheduler. Refusing is deliberate: installing a timer that silently
// never fires would leave endpoints believing they auto-update when they do not.
func TestUpdaterRefusesSupervisedMode(t *testing.T) {
	m := UpdaterManager{Kind: KindSupervised}
	if m.Supported() {
		t.Fatal("supervised mode has no scheduler and must not report support")
	}
	_, err := m.WriteUnit("/opt/beacon/bin/beacon")
	if err == nil {
		t.Fatal("expected an error rather than a no-op timer")
	}
	if !strings.Contains(err.Error(), "scheduler") {
		t.Errorf("error should explain the missing scheduler, got %v", err)
	}
	if st := m.Status(); st.Loaded || st.Running {
		t.Errorf("supervised updater must not report loaded/running: %#v", st)
	}
}

func TestUpdaterSystemdUnitsPairTimerWithService(t *testing.T) {
	svc := updaterServiceUnit("/opt/beacon/bin/beacon")
	for _, want := range []string{"Type=oneshot", "endpoint update --scheduled"} {
		if !strings.Contains(svc, want) {
			t.Errorf("updater service unit missing %q:\n%s", want, svc)
		}
	}
	timer := updaterTimerUnit()
	for _, want := range []string{
		"OnCalendar=*-*-* 09,12,15,18,21:00:00", // same schedule as the launchd plist
		"Persistent=true",                       // runs a firing missed while asleep
		"Unit=" + UpdaterServiceUnit,
		"WantedBy=timers.target",
	} {
		if !strings.Contains(timer, want) {
			t.Errorf("updater timer missing %q:\n%s", want, timer)
		}
	}
}

func TestUpdaterUnitPathFollowsBackend(t *testing.T) {
	// The launchd and systemd unit locations are absolute POSIX system paths, and these assertions
	// are about those exact strings. filepath.Join renders them with backslashes on Windows, so the
	// comparison there would be testing path rendering rather than the contract -- and neither
	// service manager exists on that platform to have a contract about.
	if runtime.GOOS == "windows" {
		t.Skip("launchd and systemd unit paths are POSIX-only")
	}
	if got := (UpdaterManager{Kind: KindLaunchd}).UnitPath(); got != "/Library/LaunchDaemons/com.beacon.endpoint.updater.plist" {
		t.Errorf("launchd updater path = %q", got)
	}
	if got := (UpdaterManager{Kind: KindSystemd}).UnitPath(); got != "/etc/systemd/system/"+UpdaterTimerUnit {
		t.Errorf("systemd updater path = %q, want the timer unit", got)
	}
}
