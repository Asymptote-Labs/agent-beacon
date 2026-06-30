package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

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
}

type userConfigTargets struct {
	native []string
	hooks  []string
}

func runEndpointUserConfigRepairInstalled(cmd *cobra.Command, args []string) error {
	result, err := repairInstalledEndpointUserConfig()
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	return err
}

func repairInstalledEndpointUserConfig() (endpointUserConfigRepairResult, error) {
	if endpointUserMode() {
		return endpointUserConfigRepairResult{SkippedReason: "requires_system_endpoint"}, fmt.Errorf("endpoint user-config repair-installed is intended for system endpoint installs; pass --system")
	}
	info, ok, err := activeConsoleUser()
	if err != nil {
		return endpointUserConfigRepairResult{}, err
	}
	if !ok {
		return endpointUserConfigRepairResult{SkippedReason: "no_active_console_user"}, nil
	}

	cfg := loadConfigForMode(false, endpointOpts.logPath)
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

	nativeTargets, nativePaths, err := repairNativeRuntimeConfigForUser(info, cfg, targets.native)
	if err != nil {
		return endpointUserConfigRepairResult{}, err
	}
	var hookTargets []string
	if len(targets.hooks) > 0 {
		hookTargets, err = repairHookTargetsForUser(info, cfg.LogPath, targets.hooks)
		if err != nil {
			return endpointUserConfigRepairResult{}, err
		}
	}
	return endpointUserConfigRepairResult{
		User:                info.Username,
		HomeDir:             info.HomeDir,
		RuntimeLogPath:      cfg.LogPath,
		NativeConfigTargets: nativeTargets,
		NativeConfigPaths:   nativePaths,
		HookTargets:         hookTargets,
	}, nil
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

func repairNativeRuntimeConfigForUser(info consoleUserInfo, cfg endpointconfig.Config, targets []string) ([]string, []string, error) {
	grpcEndpoint := fmt.Sprintf("http://127.0.0.1:%d", cfg.Collector.GRPCPort)
	seen := map[string]bool{}
	var configured []string
	var paths []string
	_, err := withUserHome(info.HomeDir, func() (struct{}, error) {
		for _, target := range targets {
			switch strings.TrimSpace(target) {
			case "claude":
				if seen["claude"] {
					continue
				}
				path, err := harness.ConfigureClaude(harness.ConfigureOptions{Endpoint: grpcEndpoint, UserMode: true})
				if err != nil {
					return struct{}{}, err
				}
				if err := collapseUserConfigBackups(path); err != nil {
					return struct{}{}, err
				}
				if err := chownUserConfigArtifacts(info, path); err != nil {
					return struct{}{}, err
				}
				configured = append(configured, "claude")
				paths = append(paths, path)
				seen["claude"] = true
			case "codex":
				if seen["codex"] {
					continue
				}
				path, err := harness.ConfigureCodex(harness.ConfigureOptions{Endpoint: grpcEndpoint, UserMode: true})
				if err != nil {
					return struct{}{}, err
				}
				if err := collapseUserConfigBackups(path); err != nil {
					return struct{}{}, err
				}
				if err := chownUserConfigArtifacts(info, path); err != nil {
					return struct{}{}, err
				}
				configured = append(configured, "codex")
				paths = append(paths, path)
				seen["codex"] = true
			case "gemini":
				if seen["gemini"] {
					continue
				}
				path, err := harness.ConfigureGemini(harness.ConfigureOptions{Endpoint: grpcEndpoint, UserMode: true})
				if err != nil {
					return struct{}{}, err
				}
				if err := collapseUserConfigBackups(path); err != nil {
					return struct{}{}, err
				}
				if err := chownUserConfigArtifacts(info, path); err != nil {
					return struct{}{}, err
				}
				configured = append(configured, "gemini")
				paths = append(paths, path)
				seen["gemini"] = true
			}
		}
		return struct{}{}, nil
	})
	return configured, paths, err
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

func chownUserConfigArtifacts(info consoleUserInfo, path string) error {
	uid, gid, err := userOwnership(info)
	if err != nil {
		return err
	}
	for _, candidate := range userConfigArtifacts(path) {
		if err := os.Chown(candidate, uid, gid); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func userConfigArtifacts(path string) []string {
	artifacts := []string{filepath.Dir(path), path}
	if matches, err := filepath.Glob(path + ".beacon.*.bak"); err == nil {
		artifacts = append(artifacts, matches...)
	}
	artifacts = append(artifacts, path+".beacon.bak")
	return artifacts
}

func userOwnership(info consoleUserInfo) (int, int, error) {
	if u, err := user.Lookup(info.Username); err == nil {
		uid, uidErr := strconv.Atoi(u.Uid)
		gid, gidErr := strconv.Atoi(u.Gid)
		if uidErr == nil && gidErr == nil {
			return uid, gid, nil
		}
	}
	stat, err := os.Stat(info.HomeDir)
	if err != nil {
		return 0, 0, err
	}
	sys, ok := stat.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("could not determine ownership for %s", info.HomeDir)
	}
	return int(sys.Uid), int(sys.Gid), nil
}
