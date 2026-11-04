package handoff

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	LaunchModeCurrent     = "current"
	LaunchModeAuto        = "auto"
	LaunchModeNewTerminal = "new-terminal"
	LaunchModeTmux        = "tmux"

	launchSpecVersion = 1
	// launchSpecLifetime is how long a launch specification stays valid. A caller waits seconds for
	// its status, so a status file older than this has no caller left to read it.
	launchSpecLifetime = 5 * time.Minute
)

// DetachedOptions controls how LaunchDetached opens the terminal surface. The exported hooks keep
// every platform path testable from any OS; callers normally set only Mode, BeaconExecutable and
// SpecDir.
type DetachedOptions struct {
	Mode             string
	BeaconExecutable string
	SpecDir          string
	GOOS             string
	Getenv           func(string) string
	LookPath         func(string) (string, error)
	Start            func(*exec.Cmd) error
	Now              func() time.Time
	Sleep            func(time.Duration)
	StartupTimeout   time.Duration
}

// DetachedResult describes the terminal surface that accepted a handoff.
type DetachedResult struct {
	Status        string `json:"status"`
	Mode          string `json:"launch"`
	Launcher      string `json:"launcher"`
	SessionName   string `json:"session_name,omitempty"`
	AttachCommand string `json:"attach_command,omitempty"`
}

type launchSpec struct {
	Version      int       `json:"version"`
	Plan         Plan      `json:"plan"`
	StatusPath   string    `json:"status_path"`
	CleanupPaths []string  `json:"cleanup_paths,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	// CarriedEnv holds the caller's values of carriedEnvNames. A new terminal starts from its own
	// environment, not the caller's, so without them a runtime could look for its session store in
	// a different place. The plan's own Env still wins over them.
	CarriedEnv map[string]string `json:"carried_env,omitempty"`
}

// carriedEnvNames are the variables that locate the configuration and session store of a runtime
// handoff launches. They hold paths or profile names, never credentials, so they may be written
// to the launch specification.
var carriedEnvNames = []string{
	"CLAUDE_CONFIG_DIR",
	"CLINE_DIR",
	"CODEX_HOME",
	"COPILOT_HOME",
	"CURSOR_CONFIG_DIR",
	"GEMINI_CLI_HOME",
	"HERMES_DATA_DIR_SUFFIX",
	"HERMES_HOME",
	"KIRO_HOME",
	"OH_PERSISTENCE_DIR",
	"OMP_PROFILE",
	"OPENCLAW_STATE_DIR",
	"OPENCODE_DB",
	"PI_CODING_AGENT_DIR",
	"PI_CONFIG_DIR",
	"PI_PROFILE",
	"PRIME_AGENT_CODING_AGENT_DIR",
	"QWEN_HOME",
	"XDG_CONFIG_HOME",
	"XDG_DATA_HOME",
	"XDG_STATE_HOME",
}

type launchStatus struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

type detachedFiles struct {
	id         string
	specPath   string
	statusPath string
	scriptPath string
}

type launcherCommand struct {
	mode          string
	name          string
	executable    string
	args          []string
	sessionName   string
	attachCommand string
	needsScript   bool
}

// ValidateLaunchMode validates the public --launch values.
func ValidateLaunchMode(mode string) error {
	switch mode {
	case "", LaunchModeCurrent, LaunchModeAuto, LaunchModeNewTerminal, LaunchModeTmux:
		return nil
	default:
		return fmt.Errorf("launch mode must be %q, %q, %q or %q", LaunchModeCurrent, LaunchModeAuto, LaunchModeNewTerminal, LaunchModeTmux)
	}
}

// LaunchDetached writes a private one-time launch specification, opens a new terminal surface and
// waits until the Beacon trampoline confirms that the target runtime process started.
func LaunchDetached(plan Plan, opts DetachedOptions) (DetachedResult, error) {
	opts = normalizeDetachedOptions(opts)
	if err := ValidateLaunchMode(opts.Mode); err != nil {
		return DetachedResult{}, err
	}
	if opts.Mode == "" || opts.Mode == LaunchModeCurrent {
		return DetachedResult{}, errors.New("detached launch needs auto, new-terminal or tmux")
	}
	if !filepath.IsAbs(opts.BeaconExecutable) {
		return DetachedResult{}, fmt.Errorf("beacon executable must be absolute: %s", opts.BeaconExecutable)
	}
	if !filepath.IsAbs(opts.SpecDir) {
		return DetachedResult{}, fmt.Errorf("handoff launch directory must be absolute: %s", opts.SpecDir)
	}

	files, err := newDetachedFiles(opts.SpecDir)
	if err != nil {
		return DetachedResult{}, err
	}
	command, err := resolveLauncher(plan, files, opts)
	if err != nil {
		return DetachedResult{}, err
	}
	spec := launchSpec{
		Version:    launchSpecVersion,
		Plan:       plan,
		StatusPath: files.statusPath,
		ExpiresAt:  opts.Now().Add(launchSpecLifetime).UTC(),
		CarriedEnv: carriedEnv(opts.Getenv),
	}
	if command.needsScript {
		spec.CleanupPaths = append(spec.CleanupPaths, files.scriptPath)
	}
	if err := writeLaunchSpec(files.specPath, spec); err != nil {
		return DetachedResult{}, err
	}
	cleanup := func() {
		_ = os.Remove(files.specPath)
		_ = os.Remove(files.statusPath)
		_ = os.Remove(files.scriptPath)
	}
	if command.needsScript {
		if err := writePOSIXLauncher(files.scriptPath, opts.BeaconExecutable, files.specPath); err != nil {
			cleanup()
			return DetachedResult{}, err
		}
	}

	cmd := exec.Command(command.executable, command.args...)
	if err := opts.Start(cmd); err != nil {
		cleanup()
		return DetachedResult{}, fmt.Errorf("open %s: %w", command.name, err)
	}
	status, err := waitLaunchStatus(files.statusPath, opts)
	if err != nil {
		// Withdraw the specification so a terminal that opens late finds nothing to run: the caller
		// is told the launch failed and may retry, which must not start the runtime twice. When the
		// terminal claimed it first, the runtime may be starting.
		cancelled := cancelLaunchSpec(files.specPath)
		_ = os.Remove(files.statusPath)
		_ = os.Remove(files.scriptPath)
		if !cancelled {
			return DetachedResult{}, fmt.Errorf("%s opened and took the launch but the target runtime did not confirm startup: %w; it may still start there, so check %s before retrying", command.name, err, command.name)
		}
		return DetachedResult{}, fmt.Errorf("%s opened but the target runtime did not confirm startup: %w; the launch was cancelled", command.name, err)
	}
	_ = os.Remove(files.statusPath)
	if status.State != "started" {
		return DetachedResult{}, fmt.Errorf("%s could not start the target runtime: %s", command.name, status.Error)
	}
	return DetachedResult{
		Status:        "launched",
		Mode:          command.mode,
		Launcher:      command.name,
		SessionName:   command.sessionName,
		AttachCommand: command.attachCommand,
	}, nil
}

func carriedEnv(getenv func(string) string) map[string]string {
	var env map[string]string
	for _, name := range carriedEnvNames {
		if value := getenv(name); value != "" {
			if env == nil {
				env = map[string]string{}
			}
			env[name] = value
		}
	}
	return env
}

func normalizeDetachedOptions(opts DetachedOptions) DetachedOptions {
	if opts.GOOS == "" {
		opts.GOOS = runtime.GOOS
	}
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	if opts.Start == nil {
		opts.Start = func(cmd *exec.Cmd) error { return cmd.Start() }
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Sleep == nil {
		opts.Sleep = time.Sleep
	}
	if opts.StartupTimeout <= 0 {
		opts.StartupTimeout = 15 * time.Second
	}
	return opts
}

func newDetachedFiles(dir string) (detachedFiles, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return detachedFiles{}, fmt.Errorf("create handoff directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return detachedFiles{}, fmt.Errorf("restrict handoff directory: %w", err)
	}
	pruneStaleLaunchStatus(dir, time.Now())
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return detachedFiles{}, fmt.Errorf("generate handoff launch id: %w", err)
	}
	id := hex.EncodeToString(random[:])
	base := filepath.Join(dir, "launch-"+id)
	return detachedFiles{
		id:         id,
		specPath:   base + ".json",
		statusPath: base + ".status.json",
		scriptPath: base + ".command",
	}, nil
}

// pruneStaleLaunchStatus removes status files no caller is waiting for: a terminal that confirms
// after its caller gave up writes one that nobody reads.
func pruneStaleLaunchStatus(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "launch-") || !strings.HasSuffix(name, ".status.json") {
			continue
		}
		if info, err := entry.Info(); err == nil && now.Sub(info.ModTime()) > launchSpecLifetime {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

func writeLaunchSpec(path string, spec launchSpec) error {
	data, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("encode handoff launch spec: %w", err)
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create handoff launch spec: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write handoff launch spec: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write handoff launch spec: %w", err)
	}
	return nil
}

func writePOSIXLauncher(path, beaconExecutable, specPath string) error {
	content := "#!/bin/sh\nexec " + quotePOSIX(beaconExecutable) + " handoff _launch --spec " + quotePOSIX(specPath) + "\n"
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return fmt.Errorf("create handoff terminal launcher: %w", err)
	}
	if _, err := io.WriteString(file, content); err != nil {
		file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write handoff terminal launcher: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write handoff terminal launcher: %w", err)
	}
	return nil
}

func quotePOSIX(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func resolveLauncher(plan Plan, files detachedFiles, opts DetachedOptions) (launcherCommand, error) {
	mode := opts.Mode
	if mode == LaunchModeAuto {
		mode = automaticLaunchMode(opts)
	}
	switch mode {
	case LaunchModeTmux:
		return tmuxLauncher(plan, files, opts)
	case LaunchModeNewTerminal:
		return terminalLauncher(plan, files, opts)
	default:
		return launcherCommand{}, fmt.Errorf("cannot resolve launch mode %q", mode)
	}
}

func automaticLaunchMode(opts DetachedOptions) string {
	if opts.GOOS == "linux" {
		if isHeadlessLinux(opts) {
			return LaunchModeTmux
		}
		if _, _, ok := findLinuxTerminal(opts); ok {
			return LaunchModeNewTerminal
		}
		return LaunchModeTmux
	}
	return LaunchModeNewTerminal
}

func isHeadlessLinux(opts DetachedOptions) bool {
	display := opts.Getenv("DISPLAY")
	if opts.Getenv("SSH_CONNECTION") != "" || opts.Getenv("SSH_TTY") != "" {
		host, _, ok := strings.Cut(display, ":")
		return !ok || host == "" || host == "unix" || strings.HasPrefix(host, "/")
	}
	return display == "" && opts.Getenv("WAYLAND_DISPLAY") == ""
}

func terminalLauncher(plan Plan, files detachedFiles, opts DetachedOptions) (launcherCommand, error) {
	runnerArgs := []string{"handoff", "_launch", "--spec", files.specPath}
	switch opts.GOOS {
	case "darwin":
		if cmux, ok := cmuxLauncher(files, opts); ok {
			return cmux, nil
		}
		open, err := opts.LookPath("open")
		if err != nil {
			return launcherCommand{}, errors.New("open Terminal.app: open was not found")
		}
		return launcherCommand{
			mode:        LaunchModeNewTerminal,
			name:        "Terminal.app",
			executable:  open,
			args:        []string{"-a", "Terminal.app", files.scriptPath},
			needsScript: true,
		}, nil
	case "windows":
		if wt, err := opts.LookPath("wt.exe"); err == nil {
			// wt splits its command line into subcommands at every ";", so a literal one is escaped.
			args := []string{"-w", "new", "new-tab", "--startingDirectory", escapeWindowsTerminal(plan.Dir), escapeWindowsTerminal(opts.BeaconExecutable)}
			for _, arg := range runnerArgs {
				args = append(args, escapeWindowsTerminal(arg))
			}
			return launcherCommand{mode: LaunchModeNewTerminal, name: "Windows Terminal", executable: wt, args: args}, nil
		}
		for _, name := range []string{"powershell.exe", "pwsh.exe", "pwsh"} {
			if powershell, err := opts.LookPath(name); err == nil {
				script := powershellStartProcess(opts.BeaconExecutable, runnerArgs, plan.Dir)
				return launcherCommand{
					mode:       LaunchModeNewTerminal,
					name:       "PowerShell",
					executable: powershell,
					args:       []string{"-NoProfile", "-NonInteractive", "-EncodedCommand", encodePowerShell(script)},
				}, nil
			}
		}
		return launcherCommand{}, errors.New("open a new Windows terminal: neither wt.exe nor PowerShell was found")
	case "linux":
		if isHeadlessLinux(opts) {
			return launcherCommand{}, errors.New("open a new Linux terminal: no local graphical display is available; use --launch tmux")
		}
		path, kind, ok := findLinuxTerminal(opts)
		if !ok {
			return launcherCommand{}, errors.New("open a new Linux terminal: no supported terminal emulator was found; set TERMINAL or use --launch tmux")
		}
		args, err := linuxTerminalArgs(kind, plan.Dir, opts.BeaconExecutable, runnerArgs)
		if err != nil {
			return launcherCommand{}, err
		}
		return launcherCommand{mode: LaunchModeNewTerminal, name: kind, executable: path, args: args}, nil
	default:
		return launcherCommand{}, fmt.Errorf("open a new terminal: unsupported platform %s", opts.GOOS)
	}
}

func escapeWindowsTerminal(arg string) string {
	return strings.ReplaceAll(arg, ";", `\;`)
}

func cmuxLauncher(files detachedFiles, opts DetachedOptions) (launcherCommand, bool) {
	socketPath := strings.TrimSpace(opts.Getenv("CMUX_SOCKET_PATH"))
	if socketPath == "" {
		return launcherCommand{}, false
	}
	var executable string
	if configured := strings.TrimSpace(opts.Getenv("CMUX_BUNDLED_CLI_PATH")); configured != "" {
		executable, _ = opts.LookPath(configured)
	}
	if executable == "" {
		executable, _ = opts.LookPath("cmux")
	}
	if executable == "" {
		return launcherCommand{}, false
	}
	args := []string{"--socket", socketPath, "new-surface", "--type", "terminal", "--command", quotePOSIX(files.scriptPath), "--focus", "true"}
	if workspace := strings.TrimSpace(opts.Getenv("CMUX_WORKSPACE_ID")); workspace != "" {
		args = append(args, "--workspace", workspace)
	}
	return launcherCommand{
		mode:        LaunchModeNewTerminal,
		name:        "cmux",
		executable:  executable,
		args:        args,
		needsScript: true,
	}, true
}

func tmuxLauncher(plan Plan, files detachedFiles, opts DetachedOptions) (launcherCommand, error) {
	if opts.GOOS == "windows" {
		return launcherCommand{}, errors.New("tmux launch is not supported on Windows; use --launch new-terminal")
	}
	tmux, err := opts.LookPath("tmux")
	if err != nil {
		return launcherCommand{}, errors.New("launch with tmux: tmux was not found on PATH")
	}
	name := "beacon-handoff-" + files.id[:8]
	return launcherCommand{
		mode:          LaunchModeTmux,
		name:          "tmux",
		executable:    tmux,
		args:          []string{"new-session", "-d", "-s", name, "-c", plan.Dir, quotePOSIX(files.scriptPath)},
		sessionName:   name,
		attachCommand: "tmux attach -t " + name,
		needsScript:   true,
	}, nil
}

func findLinuxTerminal(opts DetachedOptions) (string, string, bool) {
	if configured := strings.TrimSpace(opts.Getenv("TERMINAL")); configured != "" && !strings.ContainsAny(configured, " \t\r\n") {
		if path, err := opts.LookPath(configured); err == nil {
			return path, filepath.Base(configured), true
		}
	}
	for _, candidate := range []string{"xdg-terminal-exec", "gnome-terminal", "konsole", "kitty", "wezterm", "alacritty", "foot", "x-terminal-emulator", "xterm"} {
		if path, err := opts.LookPath(candidate); err == nil {
			return path, candidate, true
		}
	}
	return "", "", false
}

func linuxTerminalArgs(kind, dir, beaconExecutable string, runnerArgs []string) ([]string, error) {
	command := append([]string{beaconExecutable}, runnerArgs...)
	switch filepath.Base(kind) {
	case "xdg-terminal-exec":
		return append([]string{"--dir=" + dir, "--"}, command...), nil
	case "gnome-terminal":
		return append([]string{"--working-directory=" + dir, "--"}, command...), nil
	case "konsole":
		return append([]string{"--workdir", dir, "-e"}, command...), nil
	case "kitty":
		return append([]string{"--directory", dir}, command...), nil
	case "wezterm":
		return append([]string{"start", "--cwd", dir, "--"}, command...), nil
	case "alacritty":
		return append([]string{"--working-directory", dir, "-e"}, command...), nil
	case "foot":
		return append([]string{"--working-directory=" + dir}, command...), nil
	case "x-terminal-emulator", "xterm":
		return append([]string{"-e"}, command...), nil
	default:
		// $TERMINAL is commonly a terminal emulator with the conventional -e contract.
		return append([]string{"-e"}, command...), nil
	}
}

func powershellStartProcess(executable string, args []string, dir string) string {
	encodedExe := base64.StdEncoding.EncodeToString([]byte(executable))
	encodedDir := base64.StdEncoding.EncodeToString([]byte(dir))
	argLine := windowsCommandLine(args)
	encodedArgs := base64.StdEncoding.EncodeToString([]byte(argLine))
	return "$d={param($s)[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($s))};" +
		"Start-Process -FilePath (&$d '" + encodedExe + "') -ArgumentList (&$d '" + encodedArgs +
		"') -WorkingDirectory (&$d '" + encodedDir + "')"
}

func encodePowerShell(script string) string {
	encoded := utf16.Encode([]rune(script))
	data := make([]byte, 0, len(encoded)*2)
	for _, value := range encoded {
		data = append(data, byte(value), byte(value>>8))
	}
	return base64.StdEncoding.EncodeToString(data)
}

func windowsCommandLine(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, quoteWindowsArg(arg))
	}
	return strings.Join(quoted, " ")
}

func quoteWindowsArg(arg string) string {
	if arg != "" && !strings.ContainsAny(arg, " \t\n\v\"") {
		return arg
	}
	var out strings.Builder
	out.WriteByte('"')
	slashes := 0
	for _, r := range arg {
		if r == '\\' {
			slashes++
			continue
		}
		if r == '"' {
			out.WriteString(strings.Repeat("\\", slashes*2+1))
			out.WriteRune(r)
			slashes = 0
			continue
		}
		out.WriteString(strings.Repeat("\\", slashes))
		slashes = 0
		out.WriteRune(r)
	}
	out.WriteString(strings.Repeat("\\", slashes*2))
	out.WriteByte('"')
	return out.String()
}

func waitLaunchStatus(path string, opts DetachedOptions) (launchStatus, error) {
	deadline := opts.Now().Add(opts.StartupTimeout)
	for opts.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			var status launchStatus
			if err := json.Unmarshal(data, &status); err != nil {
				return launchStatus{}, fmt.Errorf("decode launch status: %w", err)
			}
			return status, nil
		}
		if !os.IsNotExist(err) {
			return launchStatus{}, fmt.Errorf("read launch status: %w", err)
		}
		opts.Sleep(50 * time.Millisecond)
	}
	return launchStatus{}, errors.New("timed out waiting for startup confirmation")
}

// RunLaunchSpec consumes a one-time launch specification and runs its target in the new terminal.
func RunLaunchSpec(path string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	claimed, err := claimLaunchSpec(path)
	if err != nil {
		return -1, err
	}
	spec, err := readLaunchSpecAt(claimed, path)
	_ = os.Remove(claimed)
	if err != nil {
		return -1, err
	}
	for _, cleanup := range spec.CleanupPaths {
		_ = os.Remove(cleanup)
	}
	if len(spec.CarriedEnv) > 0 {
		env := make(map[string]string, len(spec.CarriedEnv)+len(spec.Plan.Env))
		for name, value := range spec.CarriedEnv {
			if !overridden(name, spec.Plan.Env) {
				env[name] = value
			}
		}
		for name, value := range spec.Plan.Env {
			env[name] = value
		}
		spec.Plan.Env = env
	}
	started := func() error {
		return writeLaunchStatus(spec.StatusPath, launchStatus{State: "started"})
	}
	code, err := LaunchWithStarted(spec.Plan, stdin, stdout, stderr, started)
	if err != nil {
		_ = writeLaunchStatus(spec.StatusPath, launchStatus{State: "error", Error: err.Error()})
	}
	return code, err
}

// claimLaunchSpec renames the specification out of the path the caller would withdraw it from to
// cancel it, so either the caller cancels and the terminal finds nothing, or the terminal claims it
// and the caller's cancel fails.
func claimLaunchSpec(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("handoff launch spec path must be absolute")
	}
	claimed := path + ".claimed"
	if err := os.Rename(path, claimed); err != nil {
		if os.IsNotExist(err) {
			return "", errors.New("handoff launch spec is gone: the launch was cancelled or has already run")
		}
		return "", fmt.Errorf("claim handoff launch spec: %w", err)
	}
	return claimed, nil
}

// cancelLaunchSpec withdraws a specification no terminal has claimed and reports whether it did.
// Exactly one of it and claimLaunchSpec wins because both rename the published file. A plain
// remove is not enough: on macOS, an unlink racing the claim can succeed although the claim did.
func cancelLaunchSpec(path string) bool {
	withdrawn := path + ".cancelled"
	err := os.Rename(path, withdrawn)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		_ = os.Remove(path)
		return true
	}
	_ = os.Remove(withdrawn)
	return true
}

func readLaunchSpec(path string) (launchSpec, error) {
	return readLaunchSpecAt(path, path)
}

// readLaunchSpecAt reads the specification stored at path, which was published as published; the
// paths it names must stay in the published file's directory.
func readLaunchSpecAt(path, published string) (launchSpec, error) {
	if !filepath.IsAbs(path) || !filepath.IsAbs(published) {
		return launchSpec{}, errors.New("handoff launch spec path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return launchSpec{}, fmt.Errorf("open handoff launch spec: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return launchSpec{}, errors.New("handoff launch spec must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return launchSpec{}, fmt.Errorf("read handoff launch spec: %w", err)
	}
	var spec launchSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return launchSpec{}, fmt.Errorf("decode handoff launch spec: %w", err)
	}
	if spec.Version != launchSpecVersion {
		return launchSpec{}, fmt.Errorf("unsupported handoff launch spec version %d", spec.Version)
	}
	if time.Now().After(spec.ExpiresAt) {
		return launchSpec{}, errors.New("handoff launch spec has expired")
	}
	if !filepath.IsAbs(spec.Plan.Executable) || !filepath.IsAbs(spec.Plan.Dir) || !filepath.IsAbs(spec.StatusPath) {
		return launchSpec{}, errors.New("handoff launch spec paths must be absolute")
	}
	specDir := filepath.Clean(filepath.Dir(published))
	if filepath.Clean(filepath.Dir(spec.StatusPath)) != specDir {
		return launchSpec{}, errors.New("handoff launch status must stay in the spec directory")
	}
	for _, cleanup := range spec.CleanupPaths {
		if filepath.Clean(filepath.Dir(cleanup)) != specDir {
			return launchSpec{}, errors.New("handoff launch cleanup path must stay in the spec directory")
		}
	}
	return spec, nil
}

func writeLaunchStatus(path string, status launchStatus) error {
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	// Written beside the status and renamed into place, so the waiting caller never reads a
	// partly written file.
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("write handoff launch status: %w", err)
	}
	if err := os.Chmod(temp, 0o600); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("write handoff launch status: %w", err)
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("write handoff launch status: %w", err)
	}
	return nil
}
