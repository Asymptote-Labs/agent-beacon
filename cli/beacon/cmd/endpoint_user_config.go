package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/spf13/cobra"
)

var endpointUserConfigCmd = &cobra.Command{
	Use:    "user-config",
	Short:  "Repair active user endpoint runtime configuration",
	Hidden: true,
}

var endpointUserConfigRepairInstalledCmd = &cobra.Command{
	Use:          "repair-installed",
	Short:        "Repair active console user runtime configuration for a system endpoint",
	Hidden:       true,
	SilenceUsage: true,
	RunE:         runEndpointUserConfigRepairInstalled,
}

type endpointUserConfigRepairResult struct {
	User                string   `json:"user,omitempty"`
	HomeDir             string   `json:"home_dir,omitempty"`
	RuntimeLogPath      string   `json:"runtime_log_path,omitempty"`
	NativeConfigTargets []string `json:"native_config_targets,omitempty"`
	NativeConfigPaths   []string `json:"native_config_paths,omitempty"`
	HookTargets         []string `json:"hook_targets,omitempty"`
	SkippedReason       string   `json:"skipped_reason,omitempty"`
	// Users holds one result per person configured, in the order they were resolved. The fields
	// above repeat the first of them, which is who this command configured when it configured only
	// one person, so a reader of the older single-user shape keeps working.
	Users []endpointUserConfigRepairResult `json:"users,omitempty"`
	// Error says why configuring this person failed, in a Users entry.
	Error string `json:"error,omitempty"`
}

type userConfigTargets struct {
	native []string
	hooks  []string
}

var loadConfigForUserRepair = loadConfigForMode

func runEndpointUserConfigRepairInstalled(cmd *cobra.Command, args []string) error {
	result, err := repairInstalledEndpointUserConfig()
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	} else {
		reportUserConfigRepair(cmd.OutOrStdout(), result)
	}
	return err
}

// reportUserConfigRepair says, in words, whose agent runtimes a root install pointed at the
// collector. A package postinstall runs as root, and the lines `endpoint install` prints before this
// describe root's own settings, so without this the install read as a success that had configured
// nobody who uses the machine -- or did configure them and never said so.
func reportUserConfigRepair(w io.Writer, result endpointUserConfigRepairResult) {
	if result.SkippedReason == "no_active_console_user" {
		fmt.Fprintln(w, "No logged-in user to configure: nobody is logged in, or only root is, so no one's agent runtime points at the collector yet.")
		fmt.Fprintln(w, "Each person who runs agents on this machine configures themselves once, logged in: sudo beacon endpoint user-config repair-installed --system")
		return
	}
	for _, user := range result.Users {
		if user.Error != "" {
			fmt.Fprintf(w, "Could not configure %s (%s): %s\n", user.User, user.HomeDir, user.Error)
			continue
		}
		parts := []string{}
		if len(user.NativeConfigTargets) > 0 {
			parts = append(parts, "telemetry for "+strings.Join(user.NativeConfigTargets, ", "))
		}
		if len(user.HookTargets) > 0 {
			parts = append(parts, "hooks for "+strings.Join(user.HookTargets, ", "))
		}
		if len(parts) == 0 {
			parts = append(parts, "nothing to configure")
		}
		fmt.Fprintf(w, "Configured %s (%s): %s, writing to %s\n", user.User, user.HomeDir, strings.Join(parts, "; "), user.RuntimeLogPath)
	}
}

func repairInstalledEndpointUserConfig() (endpointUserConfigRepairResult, error) {
	if endpointUserMode() {
		return endpointUserConfigRepairResult{SkippedReason: "requires_system_endpoint"}, fmt.Errorf("endpoint user-config repair-installed is intended for system endpoint installs; pass --system")
	}
	users, err := activeConsoleUsers()
	if err != nil {
		return endpointUserConfigRepairResult{}, err
	}
	if len(users) == 0 {
		return endpointUserConfigRepairResult{SkippedReason: "no_active_console_user"}, nil
	}

	cfg := loadConfigForUserRepair(false, endpointOpts.logPath)
	if cfg.LogPath == "" {
		cfg.LogPath = writer.DefaultPath(false)
	}
	targets, err := resolveUserConfigTargets(splitCSV(endpointOpts.hookHarnesses))
	if err != nil {
		return endpointUserConfigRepairResult{}, err
	}
	if len(targets.native) == 0 && len(targets.hooks) == 0 {
		targets = userConfigTargets{
			native: []string{"claude", "codex"},
			hooks:  []string{"claude", "codex", "cursor"},
		}
	}

	// One person's broken home (a config file they made unparseable, a launcher that refuses them)
	// must not leave everyone after them unconfigured, so each is attempted and the failures are
	// reported together.
	var result endpointUserConfigRepairResult
	var errs []error
	for _, info := range users {
		one, err := repairEndpointUserConfigFor(info, cfg, targets)
		if err != nil {
			one.Error = err.Error()
			errs = append(errs, err)
		}
		result.Users = append(result.Users, one)
	}
	first := result.Users[0]
	result.User, result.HomeDir, result.RuntimeLogPath = first.User, first.HomeDir, first.RuntimeLogPath
	result.NativeConfigTargets, result.NativeConfigPaths, result.HookTargets = first.NativeConfigTargets, first.NativeConfigPaths, first.HookTargets
	// With its error, so a reader of the single-user shape never takes a failure for success.
	result.Error = first.Error
	return result, errors.Join(errs...)
}

// repairEndpointUserConfigFor points one person's runtimes at the collector.
func repairEndpointUserConfigFor(info consoleUserInfo, cfg endpointconfig.Config, targets userConfigTargets) (endpointUserConfigRepairResult, error) {
	result := endpointUserConfigRepairResult{User: info.Username, HomeDir: info.HomeDir, RuntimeLogPath: cfg.LogPath}
	nativeTargets, nativePaths, err := repairNativeRuntimeConfigForUser(info, cfg, targets.native)
	result.NativeConfigTargets, result.NativeConfigPaths = nativeTargets, nativePaths
	if err != nil {
		return result, err
	}
	if len(targets.hooks) > 0 {
		hookTargets, err := repairHookTargetsForUser(info, cfg.LogPath, targets.hooks)
		result.HookTargets = hookTargets
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func resolveUserConfigTargets(values []string) (userConfigTargets, error) {
	seenNative := map[string]bool{}
	seenHooks := map[string]bool{}
	targets := userConfigTargets{native: []string{}, hooks: []string{}}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		endpointTarget, endpointOK := normalizeEndpointTarget(value)
		hookTarget, hookOK := normalizeHookTarget(value)
		if !endpointOK && !hookOK {
			return userConfigTargets{}, fmt.Errorf("unsupported user config harness %q", value)
		}
		if endpointOK && endpointTarget.Kind == endpointTargetOTLP {
			switch endpointTarget.Name {
			case "claude", "codex", "gemini":
				if !seenNative[endpointTarget.Name] {
					targets.native = append(targets.native, endpointTarget.Name)
					seenNative[endpointTarget.Name] = true
				}
			}
		}
		if hookOK && !seenHooks[hookTarget] {
			targets.hooks = append(targets.hooks, hookTarget)
			seenHooks[hookTarget] = true
		}
	}
	return targets, nil
}

// nativeConfigOptions is what the native runtime configs are rendered from. It is passed to the
// child process on its command line, so it holds only plain values.
type nativeConfigOptions struct {
	GRPCPort            int
	CaptureModelContext bool
}

// nativeConfigResult is what apply-native reports back to the root-side repair.
type nativeConfigResult struct {
	Targets []string `json:"native_config_targets"`
	Paths   []string `json:"native_config_paths"`
}

var (
	// writeAsConsoleUser reports whether native config must be written by a child process running
	// as the console user rather than by this process. True for root on POSIX: see
	// repairNativeRuntimeConfigForUser.
	writeAsConsoleUser    = defaultWriteAsConsoleUser
	runNativeConfigAsUser = defaultRunNativeConfigAsUser
)

func defaultWriteAsConsoleUser() bool {
	return runtime.GOOS != "windows" && os.Geteuid() == 0
}

// repairNativeRuntimeConfigForUser points the console user's Claude Code, Codex and Gemini configs
// at the collector.
//
// A system install runs this as root, and root must not write into a home directory its owner
// controls. The writers read and write paths such as ~/.claude/settings.json and their backups with
// ordinary file calls, which follow symlinks: with ~/.claude linked to /etc, or settings.json to
// /etc/shadow, a root writer edits or copies system files, and the chown that used to hand the
// results back to the user then gave them ownership of the target. A package postinstall and the
// scheduled updater both run this unattended, so any code running as that user -- a package script,
// a prompt-injected agent -- could turn the next upgrade into root.
//
// So as root on POSIX the work runs in a child process that has dropped to the user, through the
// same runuser/sudo launcher hook repair uses. Every file is then created by the user, with the
// user's own permissions, and nothing needs chowning; a symlink the user planted can only lead to
// files they could already write. Windows keeps the in-process path: an elevated process there
// writes into a profile whose ACL already grants its owner full control (see
// runHookRepairInProfile).
func repairNativeRuntimeConfigForUser(info consoleUserInfo, cfg endpointconfig.Config, targets []string) ([]string, []string, error) {
	if len(targets) == 0 {
		return nil, nil, nil
	}
	opts := nativeConfigOptions{GRPCPort: cfg.Collector.GRPCPort, CaptureModelContext: cfg.ClaudeCaptureModelContext}
	var result nativeConfigResult
	var err error
	if writeAsConsoleUser() {
		result, err = runNativeConfigAsUser(info, targets, opts)
	} else {
		result, err = withUserHome(info.HomeDir, func() (nativeConfigResult, error) {
			return applyNativeRuntimeConfig(targets, opts)
		})
	}
	return result.Targets, result.Paths, err
}

// applyNativeRuntimeConfig writes the native runtime configs into the home directory of whoever
// runs it.
func applyNativeRuntimeConfig(targets []string, opts nativeConfigOptions) (nativeConfigResult, error) {
	grpcEndpoint := fmt.Sprintf("http://127.0.0.1:%d", opts.GRPCPort)
	seen := map[string]bool{}
	result := nativeConfigResult{Targets: []string{}, Paths: []string{}}
	for _, target := range targets {
		target = strings.TrimSpace(target)
		if seen[target] {
			continue
		}
		var path string
		var err error
		switch target {
		case "claude":
			path, err = harness.ConfigureClaude(harness.ConfigureOptions{Endpoint: grpcEndpoint, UserMode: true, CaptureModelContext: opts.CaptureModelContext})
		case "codex":
			path, err = harness.ConfigureCodex(harness.ConfigureOptions{Endpoint: grpcEndpoint, UserMode: true})
		case "gemini":
			path, err = harness.ConfigureGemini(harness.ConfigureOptions{Endpoint: grpcEndpoint, UserMode: true})
		default:
			continue
		}
		if err != nil {
			return result, err
		}
		if err := collapseUserConfigBackups(path); err != nil {
			return result, err
		}
		result.Targets = append(result.Targets, target)
		result.Paths = append(result.Paths, path)
		seen[target] = true
	}
	return result, nil
}

var endpointUserConfigApplyNativeCmd = &cobra.Command{
	Use:          "apply-native",
	Short:        "Point this user's native runtime configs at the local collector",
	Hidden:       true,
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	RunE:         runEndpointUserConfigApplyNative,
}

var applyNativeOpts struct {
	harnesses           string
	grpcPort            int
	captureModelContext bool
}

// runEndpointUserConfigApplyNative is the child side of a root repair: it runs as the console user
// and writes only into that user's own home.
func runEndpointUserConfigApplyNative(cmd *cobra.Command, _ []string) error {
	if applyNativeOpts.grpcPort <= 0 || applyNativeOpts.grpcPort > 65535 {
		return fmt.Errorf("--grpc-port must be a TCP port, got %d", applyNativeOpts.grpcPort)
	}
	result, err := applyNativeRuntimeConfig(splitCSV(applyNativeOpts.harnesses), nativeConfigOptions{
		GRPCPort:            applyNativeOpts.grpcPort,
		CaptureModelContext: applyNativeOpts.captureModelContext,
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
}

func defaultRunNativeConfigAsUser(info consoleUserInfo, targets []string, opts nativeConfigOptions) (nativeConfigResult, error) {
	bin, err := os.Executable()
	if err != nil {
		return nativeConfigResult{}, err
	}
	args := []string{"endpoint", "user-config", "apply-native",
		"--harness", strings.Join(targets, ","),
		"--grpc-port", strconv.Itoa(opts.GRPCPort)}
	if opts.CaptureModelContext {
		args = append(args, "--claude-capture-model-context")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := consoleUserCommand(ctx, info, bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nativeConfigResult{}, fmt.Errorf("configure runtimes for %s: %s: %w", info.Username, strings.TrimSpace(stderr.String()), err)
	}
	var result nativeConfigResult
	if err := json.Unmarshal(out, &result); err != nil {
		return nativeConfigResult{}, fmt.Errorf("configure runtimes for %s: read result: %w", info.Username, err)
	}
	return result, nil
}

func collapseUserConfigBackups(path string) error {
	matches, err := filepath.Glob(path + ".beacon.*.bak")
	if err != nil || len(matches) == 0 {
		return err
	}
	sort.Strings(matches)
	latest := matches[len(matches)-1]
	data, err := os.ReadFile(latest)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".beacon.bak", data, 0600); err != nil {
		return err
	}
	for _, match := range matches {
		if err := os.Remove(match); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
