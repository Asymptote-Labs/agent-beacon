package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
	activeConsoleUser   = defaultActiveConsoleUser
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

func defaultActiveConsoleUser() (consoleUserInfo, bool, error) {
	if runtime.GOOS != "darwin" {
		return consoleUserInfo{}, false, nil
	}
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
	cmdArgs := []string{"-u", info.Username, "env", "HOME=" + info.HomeDir, "USER=" + info.Username, "LOGNAME=" + info.Username, bin}
	cmdArgs = append(cmdArgs, args...)
	out, err := exec.CommandContext(ctx, "sudo", cmdArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("refresh hooks for %s: %s: %w", info.Username, strings.TrimSpace(string(out)), err)
	}
	if text := strings.TrimSpace(string(out)); text != "" {
		fmt.Println(text)
	}
	return nil
}
