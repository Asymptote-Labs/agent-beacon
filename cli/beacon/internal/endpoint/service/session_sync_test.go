package service

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionSyncUnitsRunTheScheduledSweep(t *testing.T) {
	args := sessionSyncJobArgs(false, "")
	if got := strings.Join(args, " "); got != "endpoint sessions sync --scheduled --system" {
		t.Fatalf("args = %q", got)
	}
	if got := strings.Join(sessionSyncJobArgs(true, "/u/runtime.jsonl"), " "); got != "endpoint sessions sync --scheduled --user --log-path /u/runtime.jsonl" {
		t.Fatalf("user args = %q", got)
	}

	plist := jobPlist(SessionSyncLabel, "/opt/beacon/bin/beacon", args, 15*time.Minute, sessionSyncSpec.runAtLoad, "/l.out", "/l.err", nil)
	for _, want := range []string{
		"<string>com.beacon.endpoint.session-sync</string>",
		"<string>endpoint</string>\n    <string>sessions</string>\n    <string>sync</string>\n    <string>--scheduled</string>\n    <string>--system</string>",
		"<key>RunAtLoad</key>\n  <false/>",
		"<key>StartInterval</key>\n  <integer>900</integer>",
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("plist missing %q:\n%s", want, plist)
		}
	}
	if strings.Contains(plist, "KeepAlive") {
		t.Fatalf("a one-shot job must not be kept alive:\n%s", plist)
	}

	svc := jobServiceUnit(sessionSyncSpec, "/opt/beacon/bin/beacon", args, false, nil)
	for _, want := range []string{"Description=Beacon endpoint session-store sync", "Type=oneshot", `ExecStart="/opt/beacon/bin/beacon" "endpoint" "sessions" "sync" "--scheduled" "--system"`, "User=root"} {
		if !strings.Contains(svc, want) {
			t.Fatalf("service unit missing %q:\n%s", want, svc)
		}
	}
	timer := jobTimerUnit(sessionSyncSpec, SessionSyncInterval())
	if !strings.Contains(timer, "OnActiveSec=900s") || strings.Contains(timer, "OnBootSec") ||
		!strings.Contains(timer, "OnUnitActiveSec=900s") || !strings.Contains(timer, "Unit=beacon-session-sync.service") {
		t.Fatalf("timer = %s", timer)
	}
}

func TestSessionSyncIntervalOverride(t *testing.T) {
	t.Setenv(SessionSyncIntervalEnv, "60")
	if got := SessionSyncInterval(); got != time.Minute {
		t.Fatalf("interval = %s", got)
	}
	t.Setenv(SessionSyncIntervalEnv, "nonsense")
	if got := SessionSyncInterval(); got != 15*time.Minute {
		t.Fatalf("interval = %s, want the default on a bad value", got)
	}
}

func TestSessionSyncAndInventoryJobsDoNotCollide(t *testing.T) {
	if sessionSyncSpec.label == inventorySpec.label || sessionSyncSpec.timerUnit == inventorySpec.timerUnit || sessionSyncSpec.serviceUnit == inventorySpec.serviceUnit {
		t.Fatal("the session sync job reuses an inventory job name")
	}
	m := SessionSyncManager{UserMode: false, Kind: KindLaunchd}
	if path, err := m.UnitPath(); err != nil || filepath.ToSlash(path) != "/Library/LaunchDaemons/com.beacon.endpoint.session-sync.plist" {
		t.Fatalf("unit path = %q, %v", path, err)
	}
}
