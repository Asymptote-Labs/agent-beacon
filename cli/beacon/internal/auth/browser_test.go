package auth

import (
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
)

// fakeBrowserHost points OpenBrowser at a pretend OS and environment and records what it would
// have exec'd.
func fakeBrowserHost(t *testing.T, goos string, env map[string]string) *[]string {
	t.Helper()
	prevGOOS, prevEnv, prevStart := browserGOOS, browserEnv, startBrowser
	prevLookPath, prevIsWSL := browserLookPath, browserIsWSL
	t.Cleanup(func() {
		browserGOOS, browserEnv, startBrowser = prevGOOS, prevEnv, prevStart
		browserLookPath, browserIsWSL = prevLookPath, prevIsWSL
	})
	var started []string
	browserGOOS = goos
	browserEnv = func(key string) string { return env[key] }
	browserIsWSL = func() bool { return false }
	browserLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	startBrowser = func(cmd *exec.Cmd) error {
		started = append(started, filepath.Base(cmd.Path))
		return nil
	}
	return &started
}

func TestOpenBrowserChecksForADisplayOnLinux(t *testing.T) {
	const ssh = "10.0.0.5 51000 10.0.0.9 22"
	cases := []struct {
		name      string
		goos      string
		env       map[string]string
		noDisplay bool
		reason    string
	}{
		{name: "X session", goos: "linux", env: map[string]string{"DISPLAY": ":0"}},
		{name: "Wayland session", goos: "linux", env: map[string]string{"WAYLAND_DISPLAY": "wayland-0"}},
		{name: "no display", goos: "linux", env: map[string]string{}, noDisplay: true,
			reason: "no browser can be opened on this machine: DISPLAY and WAYLAND_DISPLAY are not set"},
		{name: "SSH session", goos: "linux", env: map[string]string{"SSH_CONNECTION": ssh}, noDisplay: true,
			reason: "no browser can be opened on this machine: this is an SSH session"},
		// tmux started on the desktop and attached over SSH keeps the desktop's DISPLAY.
		{name: "SSH with an inherited local DISPLAY", goos: "linux",
			env: map[string]string{"SSH_CONNECTION": ssh, "DISPLAY": ":0"}, noDisplay: true},
		{name: "SSH with an inherited Wayland display", goos: "linux",
			env: map[string]string{"SSH_TTY": "/dev/pts/3", "WAYLAND_DISPLAY": "wayland-0"}, noDisplay: true},
		{name: "SSH with X11 forwarding", goos: "linux",
			env: map[string]string{"SSH_CONNECTION": ssh, "DISPLAY": "localhost:10.0"}},
		// macOS and Windows are not checked: their openers do not fail silently.
		{name: "macOS", goos: "darwin", env: map[string]string{"SSH_CONNECTION": ssh}},
		{name: "Windows", goos: "windows", env: map[string]string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			started := fakeBrowserHost(t, tc.goos, tc.env)
			err := OpenBrowser("https://beacon.sh/cli/auth?port=54123")
			if tc.noDisplay {
				if !errors.Is(err, ErrNoDisplay) {
					t.Fatalf("OpenBrowser = %v, want ErrNoDisplay", err)
				}
				if tc.reason != "" && err.Error() != tc.reason {
					t.Fatalf("error = %q, want %q", err, tc.reason)
				}
				if len(*started) != 0 {
					t.Fatalf("started %v with nowhere to show a browser", *started)
				}
				return
			}
			if err != nil {
				t.Fatalf("OpenBrowser = %v, want nil", err)
			}
			if len(*started) != 1 {
				t.Fatalf("started %v, want exactly one opener", *started)
			}
		})
	}
}

// fakeWSL makes the host look like WSL with the given Windows-side openers on PATH.
func fakeWSL(t *testing.T, onPath ...string) {
	t.Helper()
	browserIsWSL = func() bool { return true }
	browserLookPath = func(name string) (string, error) {
		for _, p := range onPath {
			if p == name {
				return "/usr/bin/" + name, nil
			}
		}
		return "", exec.ErrNotFound
	}
}

// WSLg sets DISPLAY, so the display check used to pass and xdg-open was started. With no Linux
// browser it failed after Start returned nil, and sign-in printed "Opening ... in your browser",
// showed no URL, and waited out the timeout.
func TestOpenBrowserUsesTheWindowsBrowserUnderWSL(t *testing.T) {
	cases := []struct {
		name   string
		env    map[string]string
		onPath []string
		want   string
	}{
		{name: "wslview installed", env: map[string]string{"DISPLAY": ":0"}, onPath: []string{"wslview", "rundll32.exe"}, want: "wslview"},
		{name: "interop only", env: map[string]string{"DISPLAY": ":0"}, onPath: []string{"rundll32.exe"}, want: "rundll32.exe"},
		// WSL without WSLg has no display at all; the Windows browser is still reachable.
		{name: "no WSLg display", env: map[string]string{}, onPath: []string{"rundll32.exe"}, want: "rundll32.exe"},
		// Interop disabled: fall back to the Linux behaviour.
		{name: "no Windows opener", env: map[string]string{"DISPLAY": ":0"}, want: "xdg-open"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			started := fakeBrowserHost(t, "linux", tc.env)
			fakeWSL(t, tc.onPath...)
			if err := OpenBrowser("https://beacon.sh/cli/auth?state=a&port=54123"); err != nil {
				t.Fatalf("OpenBrowser = %v, want nil", err)
			}
			if len(*started) != 1 || (*started)[0] != tc.want {
				t.Fatalf("started %v, want %s", *started, tc.want)
			}
		})
	}
}

func TestOpenBrowserUnderWSLWithoutAnyOpenerOrDisplayReportsNoDisplay(t *testing.T) {
	started := fakeBrowserHost(t, "linux", map[string]string{})
	fakeWSL(t)
	if err := OpenBrowser("https://beacon.sh"); !errors.Is(err, ErrNoDisplay) {
		t.Fatalf("OpenBrowser = %v, want ErrNoDisplay", err)
	}
	if len(*started) != 0 {
		t.Fatalf("started %v", *started)
	}
}

// A browser opened on the Windows desktop of the machine someone is SSH'd into is just as
// invisible to them as one opened on its X display.
func TestOpenBrowserUnderWSLOverSSHReportsNoDisplay(t *testing.T) {
	started := fakeBrowserHost(t, "linux", map[string]string{"SSH_CONNECTION": "10.0.0.5 51000 10.0.0.9 22"})
	fakeWSL(t, "wslview")
	if err := OpenBrowser("https://beacon.sh"); !errors.Is(err, ErrNoDisplay) {
		t.Fatalf("OpenBrowser = %v, want ErrNoDisplay", err)
	}
	if len(*started) != 0 {
		t.Fatalf("started %v", *started)
	}
}
