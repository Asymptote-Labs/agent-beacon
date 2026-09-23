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
	t.Cleanup(func() { browserGOOS, browserEnv, startBrowser = prevGOOS, prevEnv, prevStart })
	var started []string
	browserGOOS = goos
	browserEnv = func(key string) string { return env[key] }
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

func TestOpenBrowserUsesXdgOpenOnLinux(t *testing.T) {
	started := fakeBrowserHost(t, "linux", map[string]string{"DISPLAY": ":1"})
	if err := OpenBrowser("https://beacon.sh"); err != nil {
		t.Fatal(err)
	}
	if len(*started) != 1 || (*started)[0] != "xdg-open" {
		t.Fatalf("started %v, want xdg-open", *started)
	}
}
