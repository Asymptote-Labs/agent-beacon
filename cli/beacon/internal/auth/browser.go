package auth

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// ErrNoDisplay means a browser opened here would not be seen by the person running Beacon.
// CheckDisplay and OpenBrowser wrap it with the reason.
var ErrNoDisplay = errors.New("no browser can be opened on this machine")

// Variables so tests can drive the Linux branch from any OS without starting a browser.
var (
	browserGOOS  = runtime.GOOS
	browserEnv   = os.Getenv
	startBrowser = func(cmd *exec.Cmd) error { return cmd.Start() }
)

// CheckDisplay reports, on Linux, whether there is a display to open a browser on. It returns nil
// elsewhere, and nil when it cannot tell.
//
// This has to be decided before exec'ing anything. xdg-open with no display fails only after Start
// has returned nil, so the failure never reached the caller: sign-in said a browser was opening,
// showed no URL, and waited five minutes for a redirect that could not come.
//
// An SSH session counts as no display even when DISPLAY is set. There DISPLAY is usually a desktop
// session's, inherited through tmux or exported by hand, and a browser opened on it appears on a
// screen the person at the SSH client cannot see. The exception is X11 forwarding (ssh -X), whose
// DISPLAY names a host, as in localhost:10.0: the browser then runs here and shows up on the
// client, so the loopback redirect still works.
func CheckDisplay() error {
	if browserGOOS != "linux" {
		return nil
	}
	display := browserEnv("DISPLAY")
	if browserEnv("SSH_CONNECTION") != "" || browserEnv("SSH_TTY") != "" {
		if forwardedX11(display) {
			return nil
		}
		return fmt.Errorf("%w: this is an SSH session", ErrNoDisplay)
	}
	if display == "" && browserEnv("WAYLAND_DISPLAY") == "" {
		return fmt.Errorf("%w: DISPLAY and WAYLAND_DISPLAY are not set", ErrNoDisplay)
	}
	return nil
}

// forwardedX11 reports whether an X display names a remote host, which is what ssh -X sets. A
// local session's display has no host part (":0") or names the unix socket.
func forwardedX11(display string) bool {
	host, _, ok := strings.Cut(display, ":")
	return ok && host != "" && host != "unix" && !strings.HasPrefix(host, "/")
}

func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch browserGOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "linux":
		if err := CheckDisplay(); err != nil {
			return err
		}
		cmd = exec.Command("xdg-open", url)
	default:
		return fmt.Errorf("unsupported platform: %s", browserGOOS)
	}
	return startBrowser(cmd)
}
