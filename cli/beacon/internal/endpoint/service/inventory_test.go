package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestInventoryPlistIsAOneShotIntervalJob(t *testing.T) {
	args := inventoryJobArgs(false, "")
	got := inventoryPlist(InventoryLabel, "/opt/beacon/bin/beacon", args, 6*time.Hour)
	for _, want := range []string{
		"<string>com.beacon.endpoint.inventory</string>",
		"<string>/opt/beacon/bin/beacon</string>",
		"<string>endpoint</string>\n    <string>inventory</string>\n    <string>heartbeat</string>\n    <string>--scheduled</string>\n    <string>--system</string>",
		"<key>RunAtLoad</key>\n  <true/>",
		"<key>StartInterval</key>\n  <integer>21600</integer>",
		"/tmp/com.beacon.endpoint.inventory.err",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("plist missing %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{"KeepAlive", "StartCalendarInterval", "--log-path"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("plist must not contain %q:\n%s", forbidden, got)
		}
	}
	user := inventoryPlist(InventoryLabel, "/usr/local/bin/beacon", inventoryJobArgs(true, "/Users/me/.beacon/endpoint/logs/runtime.jsonl"), time.Hour)
	if !strings.Contains(user, "<string>--user</string>") || strings.Contains(user, "<string>--system</string>") {
		t.Fatalf("user-mode plist must carry --user:\n%s", user)
	}
	if !strings.Contains(user, "<string>--log-path</string>\n    <string>/Users/me/.beacon/endpoint/logs/runtime.jsonl</string>") {
		t.Fatalf("plist must pass the pinned log path:\n%s", user)
	}
	escaped := inventoryPlist("l", "/tmp/a&b", []string{"--log-path", "/tmp/<c>.jsonl"}, time.Hour)
	if !strings.Contains(escaped, "/tmp/a&amp;b") || !strings.Contains(escaped, "&lt;c&gt;") {
		t.Fatalf("plist must XML-escape program and arguments:\n%s", escaped)
	}
}

func TestInventoryIntervalEnvOverride(t *testing.T) {
	t.Setenv(InventoryIntervalEnv, "")
	if got := InventoryInterval(); got != 6*time.Hour {
		t.Fatalf("default interval = %s, want 6h", got)
	}
	t.Setenv(InventoryIntervalEnv, "60")
	if got := InventoryInterval(); got != time.Minute {
		t.Fatalf("overridden interval = %s, want 1m", got)
	}
	plist := inventoryPlist(InventoryLabel, "/opt/beacon/bin/beacon", inventoryJobArgs(false, ""), InventoryInterval())
	if !strings.Contains(plist, "<key>StartInterval</key>\n  <integer>60</integer>") {
		t.Fatalf("plist should use the overridden interval:\n%s", plist)
	}
	if !strings.Contains(inventoryTimerUnit(InventoryInterval()), "OnUnitActiveSec=60s") {
		t.Fatal("timer should use the overridden interval")
	}
	for _, junk := range []string{"0", "-5", "soon"} {
		t.Setenv(InventoryIntervalEnv, junk)
		if got := InventoryInterval(); got != 6*time.Hour {
			t.Fatalf("%q should fall back to the default, got %s", junk, got)
		}
	}
}

func TestInventorySystemdUnitsPairTimerWithOneshot(t *testing.T) {
	system := inventoryServiceUnit("/usr/bin/beacon", inventoryJobArgs(false, ""), false)
	for _, want := range []string{
		"Type=oneshot",
		`ExecStart="/usr/bin/beacon" "endpoint" "inventory" "heartbeat" "--scheduled" "--system"`,
		"User=root",
		"StandardOutput=journal",
	} {
		if !strings.Contains(system, want) {
			t.Fatalf("system service unit missing %q:\n%s", want, system)
		}
	}
	user := inventoryServiceUnit("/home/me/.local/bin/beacon", inventoryJobArgs(true, "/home/me/.beacon/endpoint/logs/runtime.jsonl"), true)
	if strings.Contains(user, "User=root") {
		t.Fatalf("user service unit must not run as root:\n%s", user)
	}
	if !strings.Contains(user, `"--user" "--log-path" "/home/me/.beacon/endpoint/logs/runtime.jsonl"`) {
		t.Fatalf("user service unit must carry --user and the log path:\n%s", user)
	}
	timer := inventoryTimerUnit(6 * time.Hour)
	for _, want := range []string{
		"OnBootSec=2min",
		"OnUnitActiveSec=21600s",
		"Persistent=true",
		"Unit=" + InventoryServiceUnit,
		"WantedBy=timers.target",
	} {
		if !strings.Contains(timer, want) {
			t.Fatalf("timer missing %q:\n%s", want, timer)
		}
	}
	if strings.Contains(timer, "OnCalendar") {
		t.Fatal("the inventory timer is interval-based, not calendar-based")
	}
}

func TestInventoryUnitPathFollowsBackendAndMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("launchd and systemd unit paths are POSIX-only")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory available")
	}
	cases := []struct {
		kind     Kind
		userMode bool
		want     string
	}{
		{KindLaunchd, true, filepath.Join(home, "Library", "LaunchAgents", InventoryLabel+".plist")},
		{KindLaunchd, false, "/Library/LaunchDaemons/" + InventoryLabel + ".plist"},
		{KindSystemd, true, filepath.Join(home, ".config", "systemd", "user", InventoryTimerUnit)},
		{KindSystemd, false, "/etc/systemd/system/" + InventoryTimerUnit},
	}
	for _, c := range cases {
		got, err := InventoryManager{UserMode: c.userMode, Kind: c.kind}.UnitPath()
		if err != nil || got != c.want {
			t.Errorf("%s user=%t: got %q (%v), want %q", c.kind, c.userMode, got, err, c.want)
		}
	}
	if (InventoryManager{Kind: KindSystemd}).Label() != InventoryTimerUnit || (InventoryManager{Kind: KindLaunchd}).Label() != InventoryLabel {
		t.Fatal("labels must follow the backend")
	}
}

func TestInventoryUnitPathsIncludeTheServiceOnSystemd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	m := InventoryManager{UserMode: true, Kind: KindSystemd}
	paths := m.UnitPaths()
	wantTimer := filepath.Join(home, ".config", "systemd", "user", InventoryTimerUnit)
	wantService := filepath.Join(home, ".config", "systemd", "user", InventoryServiceUnit)
	if len(paths) != 2 || paths[0] != wantTimer || paths[1] != wantService {
		t.Fatalf("UnitPaths = %q, want timer then service", paths)
	}
	if err := os.MkdirAll(filepath.Dir(wantTimer), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m.RemoveUnits()
	for _, p := range paths {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("RemoveUnits left %s behind", p)
		}
	}
	if launchd := (InventoryManager{UserMode: true, Kind: KindLaunchd}).UnitPaths(); len(launchd) != 1 {
		t.Fatalf("launchd has one unit file, got %q", launchd)
	}
}

// Supervised mode has no scheduler. Refusing is deliberate: a timer that silently never fires
// would leave the dashboard believing inventory is fresh when nothing is being written.
func TestInventoryRefusesSupervisedMode(t *testing.T) {
	m := InventoryManager{Kind: KindSupervised}
	if m.Supported() {
		t.Fatal("supervised mode has no scheduler and must not report support")
	}
	if !strings.Contains(m.UnsupportedReason(), "own scheduler") {
		t.Fatalf("unsupported reason should tell the operator what to run instead: %s", m.UnsupportedReason())
	}
	if _, err := m.WriteUnit("/opt/beacon/bin/beacon", ""); err == nil {
		t.Fatal("WriteUnit must fail when unsupported")
	}
	if err := m.Load(); err == nil {
		t.Fatal("Load must fail when unsupported")
	}
	if err := m.Unload(); err != nil {
		t.Fatalf("Unload must be a no-op when unsupported, got %v", err)
	}
	if st := m.Status(); st.Loaded || st.Running || st.Message == "" {
		t.Fatalf("status = %+v", st)
	}
	if runtime.GOOS != "darwin" {
		if (InventoryManager{Kind: KindLaunchd}).Supported() {
			t.Fatal("launchd must be unsupported off macOS")
		}
	}
}

func TestInventoryWriteUnitRendersTheModeIntoThePlist(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(InventoryIntervalEnv, "")
	m := InventoryManager{UserMode: true, Kind: KindLaunchd}
	path, err := m.WriteUnit("/usr/local/bin/beacon", "/tmp/scratch/runtime.jsonl")
	if err != nil {
		t.Fatalf("WriteUnit: %v", err)
	}
	if path != filepath.Join(home, "Library", "LaunchAgents", InventoryLabel+".plist") {
		t.Fatalf("unit written to %s", path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<string>--user</string>", "<string>/tmp/scratch/runtime.jsonl</string>", "<integer>21600</integer>"} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("written plist missing %q:\n%s", want, content)
		}
	}
}

// Loading is what fires the first heartbeat (RunAtLoad), so a reconcile on an already-loaded
// job must bootstrap again rather than treat "already bootstrapped" as done: that is how a
// package upgrade writes a fresh heartbeat within seconds.
func TestInventoryLoadBootstrapsAndReloadsAnExistingJob(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd only")
	}
	shrinkLaunchdWaits(t)
	t.Setenv("HOME", t.TempDir())
	var calls []string
	alreadyLoaded := true
	oldRun := runLaunchctlCommand
	runLaunchctlCommand = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[0] {
		case "bootstrap":
			if alreadyLoaded {
				alreadyLoaded = false
				return "Bootstrap failed: 5: Input/output error\nservice already bootstrapped", errors.New("exit status 5")
			}
			return "", nil
		case "bootout":
			return "", nil
		case "print":
			return "Could not find service", errors.New("exit status 113")
		}
		return "", fmt.Errorf("unexpected launchctl call: %s", strings.Join(args, " "))
	}
	t.Cleanup(func() { runLaunchctlCommand = oldRun })

	m := InventoryManager{UserMode: true, Kind: KindLaunchd}
	if err := m.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	path, _ := m.UnitPath()
	domain := serviceDomain(true)
	wantFirst := "bootstrap " + domain + " " + path
	if len(calls) < 3 || calls[0] != wantFirst || !strings.HasPrefix(calls[1], "bootout "+domain+"/"+InventoryLabel) || calls[len(calls)-1] != wantFirst {
		t.Fatalf("expected bootstrap, bootout, bootstrap; got %#v", calls)
	}

	// A fresh job bootstraps in one call.
	calls = nil
	if err := m.Load(); err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if len(calls) != 1 || calls[0] != wantFirst {
		t.Fatalf("fresh load should be a single bootstrap, got %#v", calls)
	}
}
