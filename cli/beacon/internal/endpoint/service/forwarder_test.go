package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestForwarderPlistRunsVectorResidentWithKeepAlive(t *testing.T) {
	got := forwarderPlist(AsymptoteForwarderSpec(), "/opt/beacon/bin/vector", "/Users/me/.beacon/endpoint/asymptote/vector.toml", "/Users/me/Library/Logs/Beacon/f.out", "/Users/me/Library/Logs/Beacon/f.err")
	for _, want := range []string{
		"<string>com.beacon.endpoint.asymptote-forwarder</string>",
		"<string>/opt/beacon/bin/vector</string>",
		"<string>--config</string>",
		"<string>/Users/me/.beacon/endpoint/asymptote/vector.toml</string>",
		"<key>RunAtLoad</key>\n  <true/>",
		"<key>KeepAlive</key>\n  <true/>",
		"<key>ThrottleInterval</key>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("plist missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "StartCalendarInterval") {
		t.Fatal("forwarder must be resident, not scheduled")
	}
	escaped := forwarderPlist(ForwarderSpec{Label: "l"}, "/tmp/a&b", "/tmp/<c>.toml", `/x/"o".out`, "/x/e&.err")
	if !strings.Contains(escaped, "/tmp/a&amp;b") || !strings.Contains(escaped, "&lt;c&gt;") ||
		!strings.Contains(escaped, "/x/&quot;o&quot;.out") || !strings.Contains(escaped, "/x/e&amp;.err") {
		t.Fatalf("plist must XML-escape paths:\n%s", escaped)
	}
}

func TestForwarderSystemdUnitRestartsAlwaysAndScopesByMode(t *testing.T) {
	system := forwarderUnitFile(AsymptoteForwarderSpec(), "/usr/bin/vector", "/var/lib/beacon/asymptote/vector.toml", false)
	for _, want := range []string{
		`ExecStart="/usr/bin/vector" --config "/var/lib/beacon/asymptote/vector.toml"`,
		"Restart=always",
		"After=network-online.target",
		"User=root",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(system, want) {
			t.Fatalf("system unit missing %q:\n%s", want, system)
		}
	}
	user := forwarderUnitFile(AsymptoteForwarderSpec(), "/usr/bin/vector", "/home/me/.beacon/endpoint/asymptote/vector.toml", true)
	if strings.Contains(user, "User=root") || !strings.Contains(user, "WantedBy=default.target") {
		t.Fatalf("user unit must not run as root and must hook default.target:\n%s", user)
	}
}

func TestForwarderUnitPathFollowsBackendAndMode(t *testing.T) {
	// os.UserHomeDir reads HOME on Unix and USERPROFILE on Windows, so derive the
	// expectation from it rather than pinning an environment variable.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory available")
	}
	cases := []struct {
		kind     Kind
		userMode bool
		want     string
	}{
		{KindLaunchd, true, filepath.Join(home, "Library", "LaunchAgents", ForwarderLabel+".plist")},
		{KindLaunchd, false, filepath.Join("/Library/LaunchDaemons", ForwarderLabel+".plist")},
		{KindSystemd, true, filepath.Join(home, ".config", "systemd", "user", ForwarderSystemdUnit)},
		{KindSystemd, false, filepath.Join("/etc/systemd/system", ForwarderSystemdUnit)},
	}
	for _, c := range cases {
		got, err := ForwarderManager{UserMode: c.userMode, Kind: c.kind}.UnitPath()
		if err != nil || got != c.want {
			t.Errorf("%s user=%t: got %q (%v), want %q", c.kind, c.userMode, got, err, c.want)
		}
	}
	if (ForwarderManager{Kind: KindSystemd}).Label() != ForwarderSystemdUnit || (ForwarderManager{Kind: KindLaunchd}).Label() != ForwarderLabel {
		t.Fatal("labels must follow the backend")
	}
}

func TestForwarderRefusesSupervisedAndForeignBackends(t *testing.T) {
	m := ForwarderManager{Kind: KindSupervised}
	if m.Supported() {
		t.Fatal("supervised mode has no supervisor; the forwarder must refuse it")
	}
	if !strings.Contains(m.UnsupportedReason(), "install-pack") {
		t.Fatalf("unsupported reason should point at the pack: %s", m.UnsupportedReason())
	}
	if _, err := m.WriteUnit("/usr/bin/vector", "/tmp/vector.toml"); err == nil {
		t.Fatal("WriteUnit must fail when unsupported")
	}
	if err := m.Load(); err == nil {
		t.Fatal("Load must fail when unsupported")
	}
	if err := m.Unload(); err != nil {
		t.Fatalf("Unload must be a no-op when unsupported, got %v", err)
	}
	status := m.Status()
	if status.Loaded || status.Running || status.Message == "" {
		t.Fatalf("status = %+v", status)
	}
	if runtime.GOOS != "darwin" {
		if (ForwarderManager{Kind: KindLaunchd}).Supported() {
			t.Fatal("launchd must be unsupported off macOS")
		}
	}
}

// Re-enrollment reloads a running forwarder. launchd's bootout returns before the old Vector
// has exited (it drains in-flight requests for up to a minute), and a bootstrap issued while
// the old job is still registered is lost when it finally goes; the old pid also satisfies a
// naive "running" check. Load must wait for the old job to disappear, then prove the new one
// started. Seen live on 2026-09-04: connect reported running, no forwarder was left.
func TestForwarderLoadWaitsForTheOldJobAndVerifiesTheNewOne(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd only")
	}
	shrinkLaunchdWaits(t)
	testenv.SetHome(t, t.TempDir())
	var calls []string
	printsAfterBootout := 0
	bootstrapped := false
	oldRun := runLaunchctlCommand
	runLaunchctlCommand = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[0] {
		case "bootout":
			return "", nil
		case "print":
			if bootstrapped {
				return "state = running\npid = 200\n", nil
			}
			printsAfterBootout++
			if printsAfterBootout <= 2 {
				return "state = running\npid = 100\n", nil // the old instance, still draining
			}
			return "Could not find service", errors.New("exit status 113")
		case "bootstrap":
			if printsAfterBootout <= 2 {
				t.Fatalf("bootstrap issued while the old job was still registered: %#v", calls)
			}
			bootstrapped = true
			return "", nil
		}
		return "", fmt.Errorf("unexpected launchctl call: %s", strings.Join(args, " "))
	}
	t.Cleanup(func() { runLaunchctlCommand = oldRun })

	m := ForwarderManager{UserMode: true, Kind: KindLaunchd}
	if err := m.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !bootstrapped || !strings.HasPrefix(calls[0], "bootout ") {
		t.Fatalf("expected bootout, wait, bootstrap, verify: %#v", calls)
	}
}

func TestForwarderLoadFailsWhenTheNewInstanceNeverStarts(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd only")
	}
	shrinkLaunchdWaits(t)
	testenv.SetHome(t, t.TempDir())
	bootstrapped := false
	oldRun := runLaunchctlCommand
	runLaunchctlCommand = func(args ...string) (string, error) {
		switch args[0] {
		case "bootout":
			return "", nil
		case "bootstrap":
			bootstrapped = true
			return "", nil
		case "print":
			if !bootstrapped {
				return "Could not find service", errors.New("exit status 113") // old job gone
			}
			return "state = waiting\n", nil // loaded, never gets a pid
		}
		return "", fmt.Errorf("unexpected launchctl call: %s", strings.Join(args, " "))
	}
	t.Cleanup(func() { runLaunchctlCommand = oldRun })

	m := ForwarderManager{UserMode: true, Kind: KindLaunchd}
	err := m.Load()
	if err == nil || !strings.Contains(err.Error(), "has not started") {
		t.Fatalf("Load should report a forwarder that never started, got %v", err)
	}
}

// The Asymptote forwarder's unit and plist are what connect wrote before forwarders had a spec.
// UnitCurrent compares them byte for byte, so any change here would rewrite and restart every
// connected endpoint's forwarder on its next upgrade.
func TestAsymptoteForwarderUnitsAreUnchangedBySpecs(t *testing.T) {
	unit := forwarderUnitFile(AsymptoteForwarderSpec(), "/opt/beacon/bin/vector", "/etc/beacon/endpoint/asymptote/vector.toml", false)
	wantUnit := `[Unit]
Description=Beacon endpoint forwarder to Asymptote managed ingest
Documentation=https://docs.asymptotelabs.ai/cli/endpoint-connect
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart="/opt/beacon/bin/vector" --config "/etc/beacon/endpoint/asymptote/vector.toml"
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal
User=root

[Install]
WantedBy=multi-user.target
`
	if unit != wantUnit {
		t.Fatalf("systemd unit changed:\n%s\nwant:\n%s", unit, wantUnit)
	}
	plist := forwarderPlist(AsymptoteForwarderSpec(), "/opt/beacon/bin/vector", "/a/vector.toml", "/l/f.out", "/l/f.err")
	if !strings.Contains(plist, "    <string>/a/vector.toml</string>\n  </array>\n  <key>RunAtLoad</key>") ||
		strings.Contains(plist, "EnvironmentVariables") {
		t.Fatalf("plist changed:\n%s", plist)
	}
	if (ForwarderManager{}).spec().WindowsService != "" {
		t.Fatal("the Asymptote forwarder must not be offered as a Windows service")
	}
}

// Another forwarder carries its own names, extra Vector arguments and environment into every unit.
func TestForwarderSpecReachesTheUnitPlistAndNames(t *testing.T) {
	spec := ForwarderSpec{
		Label:          "com.beacon.endpoint.test-forwarder",
		SystemdUnit:    "beacon-test-forwarder.service",
		WindowsService: "BeaconTestForwarder",
		Description:    "Beacon test forwarder",
		Args:           []string{"--graceful-shutdown-limit-secs", "10"},
		Env:            []string{"AZURE_AUTHORITY_HOST=https://login.example.test"},
	}
	unit := forwarderUnitFile(spec, "/opt/beacon/bin/vector", "/etc/f/vector.toml", false)
	for _, want := range []string{
		"Description=Beacon test forwarder\n",
		`ExecStart="/opt/beacon/bin/vector" --config "/etc/f/vector.toml" "--graceful-shutdown-limit-secs" "10"`,
		`Environment="AZURE_AUTHORITY_HOST=https://login.example.test"`,
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "Documentation=") {
		t.Fatalf("a spec without documentation must not write an empty Documentation=:\n%s", unit)
	}
	plist := forwarderPlist(spec, "/opt/beacon/bin/vector", "/etc/f/vector.toml", "/l/o", "/l/e")
	for _, want := range []string{
		"<string>com.beacon.endpoint.test-forwarder</string>",
		"    <string>--graceful-shutdown-limit-secs</string>\n    <string>10</string>\n  </array>",
		"<key>EnvironmentVariables</key>\n  <dict>\n    <key>AZURE_AUTHORITY_HOST</key>\n    <string>https://login.example.test</string>\n  </dict>",
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("plist missing %q:\n%s", want, plist)
		}
	}
	names := map[Kind]string{KindSystemd: spec.SystemdUnit, KindLaunchd: spec.Label, KindWindowsService: spec.WindowsService}
	for kind, want := range names {
		if got := (ForwarderManager{Kind: kind, Spec: spec}).Label(); got != want {
			t.Errorf("%s label = %q, want %q", kind, got, want)
		}
	}
	path, err := ForwarderManager{Kind: KindWindowsService, Spec: spec}.UnitPath()
	if err != nil || path != `HKLM\SYSTEM\CurrentControlSet\Services\BeaconTestForwarder` {
		t.Fatalf("Windows unit path = %q (%v)", path, err)
	}
}

// A forwarder with a Windows service is supported only by a system install on Windows; the
// Asymptote forwarder keeps refusing there with the message it always had.
func TestForwarderWindowsServiceSupport(t *testing.T) {
	spec := ForwarderSpec{WindowsService: "BeaconTestForwarder", Label: "l", SystemdUnit: "u.service"}
	system := ForwarderManager{Kind: KindWindowsService, Spec: spec}
	user := ForwarderManager{Kind: KindWindowsService, Spec: spec, UserMode: true}
	asymptote := ForwarderManager{Kind: KindWindowsService}

	if asymptote.Supported() || !strings.Contains(asymptote.UnsupportedReason(), "install-pack") {
		t.Fatalf("Asymptote forwarder on Windows: supported=%t reason=%q", asymptote.Supported(), asymptote.UnsupportedReason())
	}
	if user.Supported() {
		t.Fatal("a user-mode forwarder must be refused on Windows")
	}
	if runtime.GOOS == "windows" {
		if !system.Supported() {
			t.Fatalf("system forwarder unsupported: %s", system.UnsupportedReason())
		}
		if !strings.Contains(user.UnsupportedReason(), "--system") {
			t.Fatalf("user-mode reason should point at --system: %q", user.UnsupportedReason())
		}
		return
	}
	if system.Supported() || !strings.Contains(system.UnsupportedReason(), "only on Windows") {
		t.Fatalf("off Windows: supported=%t reason=%q", system.Supported(), system.UnsupportedReason())
	}
	if err := system.Unload(); err != nil {
		t.Fatalf("Unload off Windows must be a no-op, got %v", err)
	}
}
