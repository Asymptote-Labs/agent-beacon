package service

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestForwarderPlistRunsVectorResidentWithKeepAlive(t *testing.T) {
	got := forwarderPlist(ForwarderLabel, "/opt/beacon/bin/vector", "/Users/me/.beacon/endpoint/asymptote/vector.toml")
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
	escaped := forwarderPlist("l", "/tmp/a&b", "/tmp/<c>.toml")
	if !strings.Contains(escaped, "/tmp/a&amp;b") || !strings.Contains(escaped, "&lt;c&gt;") {
		t.Fatalf("plist must XML-escape paths:\n%s", escaped)
	}
}

func TestForwarderSystemdUnitRestartsAlwaysAndScopesByMode(t *testing.T) {
	system := forwarderUnitFile("/usr/bin/vector", "/var/lib/beacon/asymptote/vector.toml", false)
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
	user := forwarderUnitFile("/usr/bin/vector", "/home/me/.beacon/endpoint/asymptote/vector.toml", true)
	if strings.Contains(user, "User=root") || !strings.Contains(user, "WantedBy=default.target") {
		t.Fatalf("user unit must not run as root and must hook default.target:\n%s", user)
	}
}

func TestForwarderUnitPathFollowsBackendAndMode(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	cases := []struct {
		kind     Kind
		userMode bool
		want     string
	}{
		{KindLaunchd, true, filepath.Join("/home/tester", "Library", "LaunchAgents", ForwarderLabel+".plist")},
		{KindLaunchd, false, filepath.Join("/Library/LaunchDaemons", ForwarderLabel+".plist")},
		{KindSystemd, true, filepath.Join("/home/tester", ".config", "systemd", "user", ForwarderSystemdUnit)},
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
