package service

import (
	"runtime"
	"strings"
	"testing"
)

// Detection must pick the SCM on Windows. Falling through to the supervised backend there would
// produce an endpoint that works until the machine reboots and then silently stops collecting,
// which is exactly the difference a service manager exists to make.
func TestDetectKindPicksTheServiceManagerPerPlatform(t *testing.T) {
	got := DetectKind()
	switch runtime.GOOS {
	case "windows":
		if got != KindWindowsService {
			t.Errorf("DetectKind() = %q on Windows, want %q", got, KindWindowsService)
		}
	case "darwin":
		if got != KindLaunchd {
			t.Errorf("DetectKind() = %q on macOS, want %q", got, KindLaunchd)
		}
	case "linux":
		if got != KindSystemd && got != KindSupervised {
			t.Errorf("DetectKind() = %q on Linux, want systemd or supervised", got)
		}
	}
}

// Windows has no per-user service manager -- no equivalent of `systemctl --user` or launchd's
// gui/<uid> domain -- so user mode is routed to the supervised backend. That is a real limitation
// rather than a shortcut, and status has to say so: a supervised collector does not survive logout.
func TestWindowsUserModeFallsBackToSupervised(t *testing.T) {
	system := Manager{Kind: KindWindowsService}
	if got := system.backend().kind(); got != KindWindowsService {
		t.Errorf("system mode backend = %q, want %q", got, KindWindowsService)
	}

	user := Manager{Kind: KindWindowsService, UserMode: true}
	if got := user.backend().kind(); got != KindSupervised {
		t.Errorf("user mode backend = %q, want %q -- Windows has no per-user service manager",
			got, KindSupervised)
	}
}

// The fallback's limitations have to reach the operator, and reach them wherever an installed
// endpoint is described -- not only while one happens to be running. Someone reading status on a
// configured-but-stopped Windows user-mode endpoint is exactly the person who needs to know that
// nothing will start it again and that it stops when they log off.
//
// Asserted against the message builder rather than through Status, because Status on a fresh
// machine reports "nothing recorded" and would let the claim go untested -- which is how the first
// version of this test passed locally and failed on a clean runner.
func TestSupervisedStatusNamesWhatItCannotPromise(t *testing.T) {
	caveats := supervisedCaveats(true)
	if !strings.Contains(caveats, "no automatic restart") {
		t.Errorf("caveats = %q, want the absence of restart-on-failure named", caveats)
	}

	// Logout is Windows-only and must be claimed only there: a detached POSIX process has its own
	// session and outlives the login that started it, so saying otherwise would be false.
	mentionsLogout := strings.Contains(caveats, "logout")
	if runtime.GOOS == "windows" && !mentionsLogout {
		t.Errorf("windows user mode must disclose that it stops at logout, got %q", caveats)
	}
	if runtime.GOOS != "windows" && mentionsLogout {
		t.Errorf("a detached POSIX collector does survive logout; %q claims otherwise", caveats)
	}

	// System mode never carries the logout caveat: it is not tied to a login session on any
	// platform, and on Windows it is not even this backend.
	if strings.Contains(supervisedCaveats(false), "logout") {
		t.Error("system mode must not claim a logout limitation")
	}
}

// The kind has to survive a round trip through the flag, or `--service=windows-service` in a
// scenario or an operator's command line silently selects something else.
func TestParseKindAcceptsTheWindowsSpellings(t *testing.T) {
	for _, in := range []string{"windows-service", "scm", "windows", "WINDOWS-SERVICE"} {
		got, err := ParseKind(in)
		if err != nil {
			t.Errorf("ParseKind(%q) returned %v", in, err)
			continue
		}
		if got != KindWindowsService {
			t.Errorf("ParseKind(%q) = %q, want %q", in, got, KindWindowsService)
		}
	}
	if _, err := ParseKind("services.msc"); err == nil {
		t.Error("an unrecognized kind must be rejected rather than defaulting")
	}
}

// unitPath is shown to a human. There is no file to point at, so it names the registry key that
// actually holds the definition -- the same trade the supervised backend makes by reporting its
// pidfile rather than an error.
func TestWindowsUnitPathNamesTheRegistryKey(t *testing.T) {
	path, err := windowsBackend{}.unitPath(false)
	if err != nil {
		t.Fatalf("unitPath returned %v", err)
	}
	if !strings.Contains(path, WindowsServiceName) {
		t.Errorf("unitPath = %q, want it to name the service %q", path, WindowsServiceName)
	}
	if !strings.HasPrefix(path, `HKLM\`) {
		t.Errorf("unitPath = %q, want the registry key holding the definition", path)
	}
}

// Off Windows the backend must refuse rather than quietly do nothing. An install that reports
// success while registering no service is the failure this package is shaped around avoiding.
func TestWindowsBackendRefusesOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this asserts the off-platform contract")
	}
	b := windowsBackend{}
	if b.available() {
		t.Fatal("the Windows backend must not report itself available off Windows")
	}
	if reason := b.unsupportedReason(); !strings.Contains(reason, "Windows") {
		t.Errorf("unsupportedReason = %q, want it to name the platform", reason)
	}
	if _, err := b.writeUnit(false, "beacon-otelcol", "otelcol.yaml"); err == nil {
		t.Error("writeUnit must fail off Windows rather than report a registration it did not make")
	}
	if err := b.load(false); err == nil {
		t.Error("load must fail off Windows")
	}
	if err := b.restart(false); err == nil {
		t.Error("restart must fail off Windows")
	}
	// Unload is the deliberate exception: uninstall calls it speculatively on every backend, and
	// failing would block removing an endpoint that was never a Windows service.
	if err := b.unload(false); err != nil {
		t.Errorf("unload must stay a no-op off Windows so uninstall can call it, got %v", err)
	}
	if st := b.status(false); st.Message == "" {
		t.Error("status off Windows must explain itself rather than look like a stopped service")
	}
}

// The noun appears in install plans and doctor output, so every kind needs one that reads as the
// artifact it installs.
func TestWindowsServiceNounIsSpecific(t *testing.T) {
	noun := KindWindowsService.ServiceNoun()
	if noun == "" || strings.Contains(noun, "collector service definition") {
		t.Errorf("ServiceNoun() = %q, want wording specific to the Windows SCM", noun)
	}
}
