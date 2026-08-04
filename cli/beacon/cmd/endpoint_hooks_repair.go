package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
	"time"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/spf13/cobra"
)

var endpointHooksRepairInstalledCmd = &cobra.Command{
	Use:          "repair-installed",
	Short:        "Refresh installed user hooks",
	Hidden:       true,
	SilenceUsage: true,
	RunE:         runEndpointHooksRepairInstalled,
}

type consoleUserInfo struct {
	Username string
	HomeDir  string
}

type endpointHookRepairResult struct {
	User           string   `json:"user,omitempty"`
	HomeDir        string   `json:"home_dir,omitempty"`
	RuntimeLogPath string   `json:"runtime_log_path,omitempty"`
	Targets        []string `json:"targets,omitempty"`
	SkippedReason  string   `json:"skipped_reason,omitempty"`
}

var (
	activeConsoleUser = defaultActiveConsoleUser
	// lookupConsoleUser is a seam so the SUDO_USER-before-logind ordering can be tested without
	// depending on which accounts happen to exist on the machine running the tests -- as root in
	// a container, the obvious test skips, which left the ordering unverified.
	lookupConsoleUser   = resolveConsoleUser
	runHookRepairAsUser = defaultRunHookRepairAsUser
)

func runEndpointHooksRepairInstalled(cmd *cobra.Command, args []string) error {
	result, err := repairInstalledEndpointHooks()
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(result)
	}
	return err
}

func repairInstalledEndpointHooks() (endpointHookRepairResult, error) {
	info, ok, err := activeConsoleUser()
	if err != nil {
		return endpointHookRepairResult{}, err
	}
	if !ok {
		return endpointHookRepairResult{SkippedReason: "no_active_console_user"}, nil
	}

	logPath := endpointOpts.logPath
	if logPath == "" {
		cfg := loadConfigForMode(false, "")
		logPath = cfg.LogPath
	}
	if logPath == "" {
		logPath = writer.DefaultPath(false)
	}

	targets, err := repairHookTargetsForUser(info, logPath, nil)
	if err != nil {
		return endpointHookRepairResult{}, err
	}
	return endpointHookRepairResult{
		User:           info.Username,
		HomeDir:        info.HomeDir,
		RuntimeLogPath: logPath,
		Targets:        targets,
	}, nil
}

func repairHookTargetsForUser(info consoleUserInfo, logPath string, requestedTargets []string) ([]string, error) {
	targetSet := map[string]bool{}
	if requestedTargets != nil {
		for _, target := range requestedTargets {
			targetSet[target] = true
		}
	} else if strings.TrimSpace(endpointOpts.hookHarnesses) != "" {
		targets, err := canonicalHookTargets(splitCSV(endpointOpts.hookHarnesses))
		if err != nil {
			return nil, err
		}
		for _, target := range targets {
			targetSet[target] = true
		}
	}
	installed, err := installedHookTargetsForUser(info.HomeDir, logPath)
	if err != nil {
		return nil, err
	}
	for _, target := range installed {
		targetSet[target] = true
	}
	targets := orderedRepairTargets(targetSet)
	if len(targets) == 0 {
		return nil, nil
	}
	args := []string{"endpoint", "hooks", "install", "--harness", strings.Join(targets, ","), "--level", endpointOpts.hookLevel, "--log-path", logPath}
	if err := runHookRepairAsUser(info, args...); err != nil {
		return targets, err
	}
	return targets, nil
}

func orderedRepairTargets(targetSet map[string]bool) []string {
	targets := []string{}
	for _, target := range repairTargetOrder() {
		if targetSet[target] {
			targets = append(targets, target)
		}
	}
	return targets
}

func installedHookTargetsForUser(homeDir, logPath string) ([]string, error) {
	candidates, err := canonicalHookTargets(repairTargetOrder())
	if err != nil {
		return nil, err
	}
	return withUserHome(homeDir, func() ([]string, error) {
		cfg := endpointconfig.Default(false, logPath)
		statuses := hookStatusesWithConfig(candidates, cfg)
		targets := []string{}
		for _, name := range candidates {
			if status, ok := statuses[name]; ok && status.Installed {
				targets = append(targets, name)
			}
		}
		return targets, nil
	})
}

func repairTargetOrder() []string {
	return []string{"claude", "codex", "cursor", "vscode", "factory", "opencode", "grok", "hermes", "devin-cli", "devin-desktop", "antigravity"}
}

func withUserHome[T any](homeDir string, fn func() (T, error)) (T, error) {
	oldHome, hadHome := os.LookupEnv("HOME")
	_ = os.Setenv("HOME", homeDir)
	defer func() {
		if hadHome {
			_ = os.Setenv("HOME", oldHome)
		} else {
			_ = os.Unsetenv("HOME")
		}
	}()
	return fn()
}

// defaultActiveConsoleUser resolves the human whose runtime configuration a system install should
// reach.
//
// A system endpoint runs as root, and `endpoint install` configures the *installing* user's
// Claude Code and Codex settings -- which for a package install is root, not the person at the
// keyboard. Everything the operator actually wants captured therefore depends on resolving that
// person, which is what this does. When it cannot, callers report "no active console user" and
// skip rather than pretending to have configured someone.
func defaultActiveConsoleUser() (consoleUserInfo, bool, error) {
	switch runtime.GOOS {
	case "darwin":
		return darwinActiveConsoleUser()
	case "linux":
		return linuxActiveConsoleUser()
	}
	return consoleUserInfo{}, false, nil
}

// linuxActiveConsoleUser resolves the console user from sudo's environment, then from logind.
//
// SUDO_USER comes first because it is the exact question being asked -- "who invoked this install"
// -- and it is set for every realistic path (`sudo apt install ./beacon.deb`, `sudo dpkg -i`,
// `sudo rpm -U`, `sudo ./install-endpoint.sh`). logind is the fallback for the cases sudo cannot
// answer: an unattended fleet run, a root shell, or a re-run from a systemd unit. Neither is
// guessed at: an unresolvable user is reported as absent.
func linuxActiveConsoleUser() (consoleUserInfo, bool, error) {
	if u := strings.TrimSpace(os.Getenv("SUDO_USER")); u != "" {
		return lookupConsoleUser(u)
	}
	// loginctl lists sessions as: SESSION UID USER SEAT [TTY]. A session with a seat is someone at
	// the machine; one without is an ssh login or a service. Both can be a real human whose agent
	// runs there, so both are acceptable -- but a seated session is the better answer when a host
	// has both, which is why this makes two passes instead of taking whichever loginctl printed
	// first. An earlier version claimed that ordering in a comment while the code took the first
	// active session it saw.
	out, err := exec.Command("loginctl", "list-sessions", "--no-legend").Output()
	if err != nil {
		// No logind, or it refused. Not an error worth failing an install over: this is a
		// best-effort identification, and the caller already handles "unknown".
		return consoleUserInfo{}, false, nil
	}
	lines := strings.Split(string(out), "\n")
	for _, seatedOnly := range []bool{true, false} {
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}
			seated := len(fields) > 3 && strings.TrimSpace(fields[3]) != ""
			if seated != seatedOnly {
				continue
			}
			st, err := exec.Command("loginctl", "show-session", fields[0], "-p", "State", "--value").Output()
			if err != nil || strings.TrimSpace(string(st)) != "active" {
				continue
			}
			if info, ok, err := lookupConsoleUser(fields[2]); ok || err != nil {
				return info, ok, err
			}
		}
	}
	return consoleUserInfo{}, false, nil
}

// resolveConsoleUser validates a username and resolves its home directory.
//
// root is rejected: a system install has already configured root, and treating it as the console
// user would report success for configuring nobody. A user without a real home is rejected for
// the same reason -- there is nowhere for a settings file to go.
func resolveConsoleUser(username string) (consoleUserInfo, bool, error) {
	username = strings.TrimSpace(username)
	if username == "" || username == "root" {
		return consoleUserInfo{}, false, nil
	}
	u, err := user.Lookup(username)
	if err != nil {
		return consoleUserInfo{}, false, nil
	}
	if u.HomeDir == "" || u.HomeDir == "/" || u.HomeDir == "/nonexistent" {
		return consoleUserInfo{}, false, nil
	}
	if _, err := os.Stat(u.HomeDir); err != nil {
		return consoleUserInfo{}, false, nil
	}
	return consoleUserInfo{Username: username, HomeDir: u.HomeDir}, true, nil
}

func darwinActiveConsoleUser() (consoleUserInfo, bool, error) {
	out, err := exec.Command("stat", "-f", "%Su", "/dev/console").Output()
	if err != nil {
		return consoleUserInfo{}, false, err
	}
	username := strings.TrimSpace(string(out))
	if username == "" || username == "root" || username == "loginwindow" {
		return consoleUserInfo{}, false, nil
	}
	homeOut, err := exec.Command("dscl", ".", "-read", "/Users/"+username, "NFSHomeDirectory").Output()
	if err != nil {
		return consoleUserInfo{}, false, err
	}
	home := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(homeOut)), "NFSHomeDirectory:"))
	if home == "" {
		return consoleUserInfo{}, false, fmt.Errorf("could not resolve home directory for %s", username)
	}
	return consoleUserInfo{Username: username, HomeDir: home}, true, nil
}

func defaultRunHookRepairAsUser(info consoleUserInfo, args ...string) error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// runuser is the root-side tool for this and ships with util-linux, so it is present on a
	// minimal Debian or RPM system where sudo may not be installed at all -- and a package
	// postinstall runs as root, which is exactly runuser's precondition. sudo remains the path
	// everywhere else, including macOS.
	launcher, cmdArgs := "sudo", []string{"-u", info.Username}
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		if path, err := exec.LookPath("runuser"); err == nil {
			launcher, cmdArgs = path, []string{"-u", info.Username, "--"}
		}
	}
	cmdArgs = append(cmdArgs, "env", "HOME="+info.HomeDir, "USER="+info.Username,
		"LOGNAME="+info.Username, bin)
	cmdArgs = append(cmdArgs, args...)
	out, err := exec.CommandContext(ctx, launcher, cmdArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("refresh hooks for %s: %s: %w", info.Username, strings.TrimSpace(string(out)), err)
	}
	if text := strings.TrimSpace(string(out)); text != "" {
		fmt.Println(text)
	}
	return nil
}
