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
	browserGOOS     = runtime.GOOS
	browserEnv      = os.Getenv
	startBrowser    = func(cmd *exec.Cmd) error { return cmd.Start() }
	browserLookPath = exec.LookPath
	browserIsWSL    = isWSL
)

// isWSL reports whether Beacon is running inside Windows Subsystem for Linux. WSL sets
// WSL_DISTRO_NAME in every shell; the kernel release is the fallback for environments that
// scrub it (sudo, systemd units).
func isWSL() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" || os.Getenv("WSL_INTEROP") != "" {
		return true
	}
	release, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(release)), "microsoft")
}

// wslOpener returns the command that opens url in the Windows browser when running under WSL,
// or nil when not under WSL or no Windows opener is reachable.
//
// Under WSL, xdg-open is the wrong tool. WSLg sets DISPLAY and WAYLAND_DISPLAY, so CheckDisplay
// passes, but a stock Ubuntu has no Linux browser and no handler for https: xdg-open starts,
// fails after Start has returned nil, and sign-in waits for a redirect that cannot come. The
// person's browser is on Windows, and WSL2 forwards 127.0.0.1 to it, so the loopback redirect
// works once the URL is opened there.
//
// wslview (from wslu) is preferred because it handles quoting. rundll32.exe is reached through
// WSL interop and, unlike `cmd.exe /c start`, does not reinterpret the & in a query string.
func wslOpener(url string) []string {
	if !browserIsWSL() {
		return nil
	}
	if _, err := browserLookPath("wslview"); err == nil {
		return []string{"wslview", url}
	}
	if _, err := browserLookPath("rundll32.exe"); err == nil {
		return []string{"rundll32.exe", "url.dll,FileProtocolHandler", url}
	}
	return nil
}

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
	// Under WSL the browser opens on Windows, not on a Linux display.
	if wslOpener("") != nil {
		return nil
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
		if opener := wslOpener(url); opener != nil {
			cmd = exec.Command(opener[0], opener[1:]...)
			break
		}
		cmd = exec.Command("xdg-open", url)
	default:
		return fmt.Errorf("unsupported platform: %s", browserGOOS)
	}
	return startBrowser(cmd)
}
