package handoff

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func detachedTestOptions(goos string, available ...string) DetachedOptions {
	return DetachedOptions{
		Mode:   LaunchModeNewTerminal,
		GOOS:   goos,
		Getenv: func(string) string { return "" },
		LookPath: func(name string) (string, error) {
			for _, candidate := range available {
				if name == candidate {
					return "/bin/" + name, nil
				}
			}
			return "", exec.ErrNotFound
		},
	}
}

func TestResolveTerminalLauncherAcrossPlatforms(t *testing.T) {
	files := detachedFiles{
		id:         "0123456789abcdef01234567",
		specPath:   "/private/handoffs/launch.json",
		statusPath: "/private/handoffs/status.json",
		scriptPath: "/private/handoffs/launch.command",
	}
	plan := Plan{Dir: "/work/project"}
	for _, tc := range []struct {
		name       string
		opts       DetachedOptions
		launcher   string
		executable string
		contains   []string
		script     bool
	}{
		{
			name:       "macOS Terminal",
			opts:       detachedTestOptions("darwin", "open"),
			launcher:   "Terminal.app",
			executable: "/bin/open",
			contains:   []string{"-a", "Terminal.app", files.scriptPath},
			script:     true,
		},
		{
			name:       "Windows Terminal",
			opts:       detachedTestOptions("windows", "wt.exe"),
			launcher:   "Windows Terminal",
			executable: "/bin/wt.exe",
			contains:   []string{"--startingDirectory", plan.Dir, "handoff", "_launch", files.specPath},
		},
		{
			name:       "GNOME Terminal",
			opts:       detachedTestOptions("linux", "gnome-terminal"),
			launcher:   "gnome-terminal",
			executable: "/bin/gnome-terminal",
			contains:   []string{"--working-directory=" + plan.Dir, "--", "handoff", "_launch", files.specPath},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := normalizeDetachedOptions(tc.opts)
			if tc.opts.GOOS == "linux" {
				opts.Getenv = func(name string) string {
					if name == "DISPLAY" {
						return ":0"
					}
					return ""
				}
			}
			command, err := terminalLauncher(plan, files, opts)
			if err != nil {
				t.Fatal(err)
			}
			if command.name != tc.launcher || command.executable != tc.executable || command.needsScript != tc.script {
				t.Fatalf("launcher = %+v", command)
			}
			joined := strings.Join(command.args, "\x00")
			for _, want := range tc.contains {
				if !strings.Contains(joined, want) {
					t.Fatalf("args %q missing %q", command.args, want)
				}
			}
		})
	}
}

func TestAutomaticLaunchModeUsesTmuxHeadlessAndTerminalOnDesktop(t *testing.T) {
	opts := normalizeDetachedOptions(detachedTestOptions("linux", "gnome-terminal", "tmux"))
	opts.Mode = LaunchModeAuto
	opts.Getenv = func(string) string { return "" }
	if got := automaticLaunchMode(opts); got != LaunchModeTmux {
		t.Fatalf("headless auto mode = %q", got)
	}
	opts.Getenv = func(name string) string {
		if name == "DISPLAY" {
			return ":0"
		}
		return ""
	}
	if got := automaticLaunchMode(opts); got != LaunchModeNewTerminal {
		t.Fatalf("desktop auto mode = %q", got)
	}
	opts.Getenv = func(name string) string {
		switch name {
		case "DISPLAY":
			return "localhost:10.0"
		case "SSH_CONNECTION":
			return "client server"
		default:
			return ""
		}
	}
	if got := automaticLaunchMode(opts); got != LaunchModeNewTerminal {
		t.Fatalf("forwarded X11 auto mode = %q", got)
	}
}

func TestCmuxLauncherUsesTheCallingWorkspace(t *testing.T) {
	const cmuxPath = "/Applications/cmux.app/Contents/Resources/bin/cmux"
	opts := normalizeDetachedOptions(DetachedOptions{
		Mode: LaunchModeNewTerminal,
		GOOS: "darwin",
		Getenv: func(name string) string {
			switch name {
			case "CMUX_SOCKET_PATH":
				return "/Users/me/.local/state/cmux/cmux.sock"
			case "CMUX_WORKSPACE_ID":
				return "workspace-id"
			case "CMUX_BUNDLED_CLI_PATH":
				return cmuxPath
			default:
				return ""
			}
		},
		LookPath: func(name string) (string, error) {
			if name == cmuxPath {
				return cmuxPath, nil
			}
			return "", exec.ErrNotFound
		},
	})
	files := detachedFiles{scriptPath: "/Users/me/Library/Application Support/Beacon/launch.command"}
	command, err := terminalLauncher(Plan{Dir: "/work"}, files, opts)
	if err != nil {
		t.Fatal(err)
	}
	if command.name != "cmux" || command.executable != cmuxPath || !command.needsScript {
		t.Fatalf("command = %+v", command)
	}
	want := []string{
		"--socket", "/Users/me/.local/state/cmux/cmux.sock",
		"new-surface", "--type", "terminal",
		"--command", "'/Users/me/Library/Application Support/Beacon/launch.command'",
		"--focus", "true",
		"--workspace", "workspace-id",
	}
	if !reflect.DeepEqual(command.args, want) {
		t.Fatalf("args = %q, want %q", command.args, want)
	}
}

func TestMacOSLauncherFallsBackToTerminalOutsideCmux(t *testing.T) {
	opts := normalizeDetachedOptions(detachedTestOptions("darwin", "open"))
	opts.Getenv = func(name string) string {
		if name == "CMUX_SOCKET_PATH" {
			return "/Users/me/.local/state/cmux/cmux.sock"
		}
		return ""
	}
	command, err := terminalLauncher(Plan{}, detachedFiles{scriptPath: "/tmp/launch.command"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if command.name != "Terminal.app" {
		t.Fatalf("launcher = %q", command.name)
	}
}

func TestLaunchDetachedWaitsForTheTargetStartupStatus(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	var started *exec.Cmd
	result, err := LaunchDetached(Plan{
		Executable: "/bin/codex",
		Args:       []string{"resume", "session-1"},
		Dir:        dir,
	}, DetachedOptions{
		Mode:             LaunchModeNewTerminal,
		BeaconExecutable: "/opt/beacon/bin/beacon",
		SpecDir:          dir,
		GOOS:             "linux",
		Getenv: func(name string) string {
			if name == "DISPLAY" {
				return ":0"
			}
			return ""
		},
		LookPath: func(name string) (string, error) {
			if name == "gnome-terminal" {
				return "/usr/bin/gnome-terminal", nil
			}
			return "", exec.ErrNotFound
		},
		Start: func(cmd *exec.Cmd) error {
			started = cmd
			specPath := cmd.Args[len(cmd.Args)-1]
			data, err := os.ReadFile(specPath)
			if err != nil {
				return err
			}
			var spec launchSpec
			if err := json.Unmarshal(data, &spec); err != nil {
				return err
			}
			return writeLaunchStatus(spec.StatusPath, launchStatus{State: "started"})
		},
		Now:            func() time.Time { return now },
		Sleep:          func(time.Duration) {},
		StartupTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("LaunchDetached: %v", err)
	}
	if result.Status != "launched" || result.Mode != LaunchModeNewTerminal || result.Launcher != "gnome-terminal" {
		t.Fatalf("result = %+v", result)
	}
	if started == nil || started.Path != "/usr/bin/gnome-terminal" {
		t.Fatalf("started = %+v", started)
	}
}

func TestLaunchDetachedReportsAnUnconfirmedTerminal(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	_, err := LaunchDetached(Plan{Executable: "/bin/codex", Dir: dir}, DetachedOptions{
		Mode:             LaunchModeNewTerminal,
		BeaconExecutable: "/opt/beacon/bin/beacon",
		SpecDir:          dir,
		GOOS:             "darwin",
		LookPath:         func(string) (string, error) { return "/usr/bin/open", nil },
		Start:            func(*exec.Cmd) error { return nil },
		Now: func() time.Time {
			now = now.Add(time.Second)
			return now
		},
		Sleep:          func(time.Duration) {},
		StartupTimeout: 2 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "did not confirm startup") || !strings.Contains(err.Error(), "launch spec:") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunLaunchSpecStartsTheExactPlanAndConsumesPrivateFiles(t *testing.T) {
	testenv.RequirePOSIXExecutableFixtures(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "agent")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s|%s' \"$1\" \"$HANDOFF_MODE\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(dir, "launch.json")
	statusPath := filepath.Join(dir, "status.json")
	cleanupPath := filepath.Join(dir, "launch.command")
	if err := os.WriteFile(cleanupPath, []byte("temporary"), 0o700); err != nil {
		t.Fatal(err)
	}
	spec := launchSpec{
		Version: launchSpecVersion,
		Plan: Plan{
			Executable: script,
			Args:       []string{"argument with spaces"},
			Dir:        dir,
			Env:        map[string]string{"HANDOFF_MODE": "safe"},
		},
		StatusPath:   statusPath,
		CleanupPaths: []string{cleanupPath},
		ExpiresAt:    time.Now().Add(time.Minute),
	}
	if err := writeLaunchSpec(specPath, spec); err != nil {
		t.Fatal(err)
	}
	var stdout strings.Builder
	code, err := RunLaunchSpec(specPath, nil, &stdout, &strings.Builder{})
	if err != nil || code != 0 {
		t.Fatalf("RunLaunchSpec = %d, %v", code, err)
	}
	if stdout.String() != "argument with spaces|safe" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	for _, path := range []string{specPath, cleanupPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s was not consumed: %v", path, err)
		}
	}
	var status launchStatus
	data, err := os.ReadFile(statusPath)
	if err != nil || json.Unmarshal(data, &status) != nil || status.State != "started" {
		t.Fatalf("status = %+v, %v, data=%s", status, err, data)
	}
}

func TestReadLaunchSpecRejectsEscapingStatusAndCleanupPaths(t *testing.T) {
	dir := t.TempDir()
	base := launchSpec{
		Version:    launchSpecVersion,
		Plan:       Plan{Executable: "/bin/codex", Dir: dir},
		StatusPath: filepath.Join(dir, "status.json"),
		ExpiresAt:  time.Now().Add(time.Minute),
	}
	for i, edit := range []func(*launchSpec){
		func(spec *launchSpec) { spec.StatusPath = filepath.Join(t.TempDir(), "status.json") },
		func(spec *launchSpec) { spec.CleanupPaths = []string{filepath.Join(t.TempDir(), "script")} },
	} {
		spec := base
		edit(&spec)
		path := filepath.Join(dir, fmt.Sprintf("invalid-%d.json", i))
		if err := writeLaunchSpec(path, spec); err != nil {
			t.Fatal(err)
		}
		if _, err := readLaunchSpec(path); err == nil {
			t.Fatalf("readLaunchSpec accepted %+v", spec)
		}
	}
}

func TestLinuxTerminalArgumentsPreserveTheRunnerVector(t *testing.T) {
	command := []string{"/opt/beacon/bin/beacon", "handoff", "_launch", "--spec", "/home/me/a b/spec.json"}
	for _, kind := range []string{"xdg-terminal-exec", "gnome-terminal", "konsole", "kitty", "wezterm", "alacritty", "foot", "x-terminal-emulator", "xterm"} {
		args, err := linuxTerminalArgs(kind, "/work/a b", command[0], command[1:])
		if err != nil {
			t.Fatal(err)
		}
		if !containsSequence(args, command) {
			t.Fatalf("%s args = %q; command vector was not preserved", kind, args)
		}
	}
}

func containsSequence(values, sequence []string) bool {
	for i := 0; i+len(sequence) <= len(values); i++ {
		if reflect.DeepEqual(values[i:i+len(sequence)], sequence) {
			return true
		}
	}
	return false
}

func TestValidateLaunchMode(t *testing.T) {
	for _, mode := range []string{"", LaunchModeCurrent, LaunchModeAuto, LaunchModeNewTerminal, LaunchModeTmux} {
		if err := ValidateLaunchMode(mode); err != nil {
			t.Fatalf("%q: %v", mode, err)
		}
	}
	if err := ValidateLaunchMode("background"); err == nil {
		t.Fatal("invalid mode was accepted")
	}
}

func TestWindowsCommandLineQuotesArguments(t *testing.T) {
	got := windowsCommandLine([]string{"handoff", "_launch", "--spec", `C:\Users\A B\launch.json`, `ends\`})
	want := `handoff _launch --spec "C:\Users\A B\launch.json" ends\`
	if got != want {
		t.Fatalf("command line = %q, want %q", got, want)
	}
	if powershellStartProcess(`C:\Program Files\Beacon\beacon.exe`, []string{"handoff", "_launch", "--spec", `C:\A B\launch.json`}, `C:\Work`) == "" {
		t.Fatal("PowerShell fallback was empty")
	}
}

func TestTmuxLauncherNamesAResumableSession(t *testing.T) {
	opts := normalizeDetachedOptions(detachedTestOptions("linux", "tmux"))
	files := detachedFiles{id: "0123456789abcdef01234567", scriptPath: "/tmp/launch command"}
	command, err := tmuxLauncher(Plan{Dir: "/work"}, files, opts)
	if err != nil {
		t.Fatal(err)
	}
	if command.sessionName != "beacon-handoff-01234567" || command.attachCommand != "tmux attach -t beacon-handoff-01234567" {
		t.Fatalf("command = %+v", command)
	}
	if !reflect.DeepEqual(command.args, []string{"new-session", "-d", "-s", command.sessionName, "-c", "/work", "'/tmp/launch command'"}) {
		t.Fatalf("args = %q", command.args)
	}
}

func TestTerminalLauncherErrorsWithoutSupportedSurface(t *testing.T) {
	opts := normalizeDetachedOptions(detachedTestOptions("windows"))
	if _, err := terminalLauncher(Plan{Dir: `C:\work`}, detachedFiles{}, opts); err == nil {
		t.Fatal("Windows launcher succeeded without wt or PowerShell")
	}
	opts = normalizeDetachedOptions(detachedTestOptions("linux"))
	opts.Getenv = func(string) string { return "" }
	if _, err := terminalLauncher(Plan{Dir: "/work"}, detachedFiles{}, opts); err == nil || !strings.Contains(err.Error(), "tmux") {
		t.Fatalf("Linux headless error = %v", err)
	}
	opts = normalizeDetachedOptions(detachedTestOptions("plan9"))
	if _, err := terminalLauncher(Plan{}, detachedFiles{}, opts); err == nil {
		t.Fatal("unsupported OS was accepted")
	}
}

func TestLaunchDetachedReportsLauncherStartFailure(t *testing.T) {
	dir := t.TempDir()
	_, err := LaunchDetached(Plan{Executable: "/bin/codex", Dir: dir}, DetachedOptions{
		Mode:             LaunchModeNewTerminal,
		BeaconExecutable: "/opt/beacon/bin/beacon",
		SpecDir:          dir,
		GOOS:             "darwin",
		LookPath:         func(string) (string, error) { return "/usr/bin/open", nil },
		Start:            func(*exec.Cmd) error { return errors.New("denied") },
	})
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("err = %v", err)
	}
}
