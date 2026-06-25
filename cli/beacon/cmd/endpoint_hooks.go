package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	"github.com/spf13/cobra"
)

var endpointHooksCmd = &cobra.Command{
	Use:   "hooks",
	Short: "Manage endpoint hook integrations",
}

var endpointHooksInstallCmd = &cobra.Command{
	Use:          "install",
	Short:        "Install endpoint hooks for supported harnesses",
	SilenceUsage: true,
	RunE:         runEndpointHooksInstall,
}

var endpointHooksUninstallCmd = &cobra.Command{
	Use:          "uninstall",
	Short:        "Uninstall endpoint hooks for supported harnesses",
	SilenceUsage: true,
	RunE:         runEndpointHooksUninstall,
}

var endpointHooksStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show endpoint hook integration status",
	SilenceUsage: true,
	RunE:         runEndpointHooksStatus,
}

func runEndpointHooksInstall(cmd *cobra.Command, args []string) error {
	targets, err := hookTargets()
	if err != nil {
		return err
	}
	if endpointOpts.dryRun {
		actions := []plannedAction{}
		for _, name := range targets {
			actions = append(actions, plannedAction{Action: "configure_harness", Target: name, Message: "install endpoint hook integration"})
		}
		return printPlannedActions(actions)
	}
	cfg := loadOrDefaultConfig()
	for _, name := range targets {
		if err := installEndpointHookTarget(name, cfg); err != nil {
			return err
		}
	}
	return nil
}

func installEndpointHookTarget(name string, cfg endpointconfig.Config) error {
	switch strings.TrimSpace(name) {
	case "antigravity":
		status, err := endpointhooks.InstallAntigravity(endpointhooks.AntigravityOptions{
			Level:    endpointhooks.Level(endpointOpts.hookLevel),
			LogPath:  cfg.LogPath,
			UserMode: cfg.UserMode,
		})
		if err != nil {
			return err
		}
		fmt.Printf("Antigravity hooks installed: %s\n", status.ConfigPath)
	case "cursor":
		status, err := endpointhooks.InstallCursor(endpointhooks.CursorOptions{
			Level:    endpointhooks.Level(endpointOpts.hookLevel),
			LogPath:  cfg.LogPath,
			UserMode: cfg.UserMode,
		})
		if err != nil {
			return err
		}
		fmt.Printf("Cursor hooks installed: %s\n", status.HooksJSONPath)
	case "claude":
		status, err := endpointhooks.InstallClaude(endpointhooks.ClaudeOptions{
			Level:    endpointhooks.Level(endpointOpts.hookLevel),
			LogPath:  cfg.LogPath,
			UserMode: cfg.UserMode,
		})
		if err != nil {
			return err
		}
		fmt.Printf("Claude Code hooks installed: %s\n", status.SettingsPath)
	case "codex":
		status, err := endpointhooks.InstallCodex(endpointhooks.CodexOptions{
			Level:    endpointhooks.Level(endpointOpts.hookLevel),
			LogPath:  cfg.LogPath,
			UserMode: cfg.UserMode,
		})
		if err != nil {
			return err
		}
		fmt.Printf("Codex CLI inventory hooks installed: %s\n", status.HooksPath)
	case "vscode":
		status, err := endpointhooks.InstallVSCode(endpointhooks.VSCodeOptions{
			Level:    endpointhooks.Level(endpointOpts.hookLevel),
			LogPath:  cfg.LogPath,
			UserMode: cfg.UserMode,
		})
		if err != nil {
			return err
		}
		fmt.Printf("VS Code hooks installed: %s\n", status.HooksPath)
	case "factory":
		status, err := endpointhooks.InstallFactory(endpointhooks.FactoryOptions{
			Level:    endpointhooks.Level(endpointOpts.hookLevel),
			LogPath:  cfg.LogPath,
			UserMode: cfg.UserMode,
		})
		if err != nil {
			return err
		}
		fmt.Printf("Factory hooks installed: %s\n", status.SettingsPath)
	case "opencode":
		status, err := endpointhooks.InstallOpenCode(endpointhooks.OpenCodeOptions{
			Level:    endpointhooks.Level(endpointOpts.hookLevel),
			LogPath:  cfg.LogPath,
			UserMode: cfg.UserMode,
		})
		if err != nil {
			return err
		}
		fmt.Printf("opencode plugin installed: %s\n", status.PluginPath)
	case "grok":
		status, err := endpointhooks.InstallGrok(endpointhooks.GrokOptions{
			Level:    endpointhooks.Level(endpointOpts.hookLevel),
			LogPath:  cfg.LogPath,
			UserMode: cfg.UserMode,
		})
		if err != nil {
			return err
		}
		fmt.Printf("Grok hooks installed: %s\n", status.HooksPath)
		if strings.Contains(status.Message, "/hooks-trust") {
			fmt.Println(status.Message)
		}
	case "hermes":
		status, err := endpointhooks.InstallHermes(endpointhooks.HermesOptions{
			Level:    endpointhooks.Level(endpointOpts.hookLevel),
			LogPath:  cfg.LogPath,
			UserMode: cfg.UserMode,
		})
		if err != nil {
			return err
		}
		fmt.Printf("Hermes Agent hooks installed: %s\n", status.ConfigPath)
		fmt.Println("Hermes may prompt to trust new shell hooks on first use; use HERMES_ACCEPT_HOOKS=1 or hooks_auto_accept: true for non-TTY runs.")
	case "devin-cli":
		status, err := endpointhooks.InstallDevin(endpointhooks.DevinOptions{
			Level:    endpointhooks.Level(endpointOpts.hookLevel),
			LogPath:  cfg.LogPath,
			UserMode: cfg.UserMode,
		})
		if err != nil {
			return err
		}
		fmt.Printf("Devin CLI hooks installed: %s\n", status.ConfigPath)
	case "devin-desktop":
		status, err := endpointhooks.InstallDevinDesktop(endpointhooks.DevinDesktopOptions{
			Level:    endpointhooks.Level(endpointOpts.hookLevel),
			LogPath:  cfg.LogPath,
			UserMode: cfg.UserMode,
		})
		if err != nil {
			return err
		}
		fmt.Printf("Devin Desktop hooks installed: %s\n", status.ConfigPath)
		fmt.Println("Devin Desktop hook files are installed; generate a Desktop event and check the runtime log to validate execution.")
	case "":
	default:
		return fmt.Errorf("unsupported hook harness %q", name)
	}
	return nil
}

func runEndpointHooksUninstall(cmd *cobra.Command, args []string) error {
	targets, err := hookTargets()
	if err != nil {
		return err
	}
	if endpointOpts.dryRun {
		actions := []plannedAction{}
		for _, name := range targets {
			actions = append(actions, plannedAction{Action: "remove_hook", Target: name, Message: "uninstall endpoint hook integration"})
		}
		return printPlannedActions(actions)
	}
	cfg := loadOrDefaultConfig()
	for _, name := range targets {
		if err := uninstallEndpointHookTarget(name, cfg); err != nil {
			return err
		}
	}
	return nil
}

func uninstallEndpointHookTarget(name string, cfg endpointconfig.Config) error {
	switch strings.TrimSpace(name) {
	case "antigravity":
		status, err := endpointhooks.UninstallAntigravity(endpointhooks.AntigravityOptions{
			Level:    endpointhooks.Level(endpointOpts.hookLevel),
			LogPath:  cfg.LogPath,
			UserMode: cfg.UserMode,
		})
		if err != nil {
			return err
		}
		fmt.Println(status.Message)
	case "cursor":
		status, err := endpointhooks.UninstallCursor(endpointhooks.CursorOptions{
			Level:    endpointhooks.Level(endpointOpts.hookLevel),
			LogPath:  cfg.LogPath,
			UserMode: cfg.UserMode,
		})
		if err != nil {
			return err
		}
		fmt.Println(status.Message)
	case "claude":
		status, err := endpointhooks.UninstallClaude(endpointhooks.ClaudeOptions{
			Level:    endpointhooks.Level(endpointOpts.hookLevel),
			LogPath:  cfg.LogPath,
			UserMode: cfg.UserMode,
		})
		if err != nil {
			return err
		}
		fmt.Println(status.Message)
	case "codex":
		status, err := endpointhooks.UninstallCodex(endpointhooks.CodexOptions{
			Level:    endpointhooks.Level(endpointOpts.hookLevel),
			LogPath:  cfg.LogPath,
			UserMode: cfg.UserMode,
		})
		if err != nil {
			return err
		}
		fmt.Println(status.Message)
	case "vscode":
		status, err := endpointhooks.UninstallVSCode(endpointhooks.VSCodeOptions{
			Level:    endpointhooks.Level(endpointOpts.hookLevel),
			LogPath:  cfg.LogPath,
			UserMode: cfg.UserMode,
		})
		if err != nil {
			return err
		}
		fmt.Println(status.Message)
	case "factory":
		status, err := endpointhooks.UninstallFactory(endpointhooks.FactoryOptions{
			Level:    endpointhooks.Level(endpointOpts.hookLevel),
			LogPath:  cfg.LogPath,
			UserMode: cfg.UserMode,
		})
		if err != nil {
			return err
		}
		fmt.Println(status.Message)
	case "opencode":
		status, err := endpointhooks.UninstallOpenCode(endpointhooks.OpenCodeOptions{
			Level:    endpointhooks.Level(endpointOpts.hookLevel),
			LogPath:  cfg.LogPath,
			UserMode: cfg.UserMode,
		})
		if err != nil {
			return err
		}
		fmt.Println(status.Message)
	case "grok":
		status, err := endpointhooks.UninstallGrok(endpointhooks.GrokOptions{
			Level:    endpointhooks.Level(endpointOpts.hookLevel),
			LogPath:  cfg.LogPath,
			UserMode: cfg.UserMode,
		})
		if err != nil {
			return err
		}
		fmt.Println(status.Message)
	case "hermes":
		status, err := endpointhooks.UninstallHermes(endpointhooks.HermesOptions{
			Level:    endpointhooks.Level(endpointOpts.hookLevel),
			LogPath:  cfg.LogPath,
			UserMode: cfg.UserMode,
		})
		if err != nil {
			return err
		}
		fmt.Println(status.Message)
	case "devin-cli":
		status, err := endpointhooks.UninstallDevin(endpointhooks.DevinOptions{
			Level:    endpointhooks.Level(endpointOpts.hookLevel),
			LogPath:  cfg.LogPath,
			UserMode: cfg.UserMode,
		})
		if err != nil {
			return err
		}
		fmt.Println(status.Message)
	case "devin-desktop":
		status, err := endpointhooks.UninstallDevinDesktop(endpointhooks.DevinDesktopOptions{
			Level:    endpointhooks.Level(endpointOpts.hookLevel),
			LogPath:  cfg.LogPath,
			UserMode: cfg.UserMode,
		})
		if err != nil {
			return err
		}
		fmt.Println(status.Message)
	case "":
	default:
		return fmt.Errorf("unsupported hook harness %q", name)
	}
	return nil
}

func runEndpointHooksStatus(cmd *cobra.Command, args []string) error {
	targets, err := hookTargets()
	if err != nil {
		return err
	}
	cfg := loadOrDefaultConfig()
	statuses := map[string]interface{}{}
	for _, name := range targets {
		switch strings.TrimSpace(name) {
		case "antigravity":
			statuses["antigravity"] = endpointhooks.AntigravityHookStatus(endpointhooks.AntigravityOptions{
				Level:    endpointhooks.Level(endpointOpts.hookLevel),
				LogPath:  cfg.LogPath,
				UserMode: cfg.UserMode,
			})
		case "cursor":
			statuses["cursor"] = endpointhooks.CursorHookStatus(endpointhooks.CursorOptions{
				Level:    endpointhooks.Level(endpointOpts.hookLevel),
				LogPath:  cfg.LogPath,
				UserMode: cfg.UserMode,
			})
		case "claude":
			statuses["claude"] = endpointhooks.ClaudeHookStatus(endpointhooks.ClaudeOptions{
				Level:    endpointhooks.Level(endpointOpts.hookLevel),
				LogPath:  cfg.LogPath,
				UserMode: cfg.UserMode,
			})
		case "codex":
			statuses["codex"] = endpointhooks.CodexHookStatus(endpointhooks.CodexOptions{
				Level:    endpointhooks.Level(endpointOpts.hookLevel),
				LogPath:  cfg.LogPath,
				UserMode: cfg.UserMode,
			})
		case "vscode":
			statuses["vscode"] = endpointhooks.VSCodeHookStatus(endpointhooks.VSCodeOptions{
				Level:    endpointhooks.Level(endpointOpts.hookLevel),
				LogPath:  cfg.LogPath,
				UserMode: cfg.UserMode,
			})
		case "factory":
			statuses["factory"] = endpointhooks.FactoryHookStatus(endpointhooks.FactoryOptions{
				Level:    endpointhooks.Level(endpointOpts.hookLevel),
				LogPath:  cfg.LogPath,
				UserMode: cfg.UserMode,
			})
		case "opencode":
			statuses["opencode"] = endpointhooks.OpenCodeHookStatus(endpointhooks.OpenCodeOptions{
				Level:    endpointhooks.Level(endpointOpts.hookLevel),
				LogPath:  cfg.LogPath,
				UserMode: cfg.UserMode,
			})
		case "grok":
			statuses["grok"] = endpointhooks.GrokHookStatus(endpointhooks.GrokOptions{
				Level:    endpointhooks.Level(endpointOpts.hookLevel),
				LogPath:  cfg.LogPath,
				UserMode: cfg.UserMode,
			})
		case "hermes":
			statuses["hermes"] = endpointhooks.HermesHookStatus(endpointhooks.HermesOptions{
				Level:    endpointhooks.Level(endpointOpts.hookLevel),
				LogPath:  cfg.LogPath,
				UserMode: cfg.UserMode,
			})
		case "devin-cli":
			statuses["devin-cli"] = endpointhooks.DevinHookStatus(endpointhooks.DevinOptions{
				Level:    endpointhooks.Level(endpointOpts.hookLevel),
				LogPath:  cfg.LogPath,
				UserMode: cfg.UserMode,
			})
		case "devin-desktop":
			statuses["devin-desktop"] = endpointhooks.DevinDesktopHookStatus(endpointhooks.DevinDesktopOptions{
				Level:    endpointhooks.Level(endpointOpts.hookLevel),
				LogPath:  cfg.LogPath,
				UserMode: cfg.UserMode,
			})
		case "":
		default:
			return fmt.Errorf("unsupported hook harness %q", name)
		}
	}
	if endpointOpts.jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(statuses)
	}
	for _, name := range targets {
		switch strings.TrimSpace(name) {
		case "antigravity":
			status := statuses["antigravity"].(endpointhooks.AntigravityStatus)
			fmt.Printf("Antigravity hooks: installed=%t path=%s\n", status.Installed, status.ConfigPath)
			fmt.Println(status.Message)
		case "cursor":
			status := statuses["cursor"].(endpointhooks.CursorStatus)
			fmt.Printf("Cursor hooks: installed=%t path=%s\n", status.Installed, status.HooksJSONPath)
			fmt.Println(status.Message)
		case "claude":
			status := statuses["claude"].(endpointhooks.ClaudeStatus)
			fmt.Printf("Claude Code hooks: installed=%t path=%s\n", status.Installed, status.SettingsPath)
			fmt.Println(status.Message)
		case "vscode":
			status := statuses["vscode"].(endpointhooks.VSCodeStatus)
			fmt.Printf("VS Code hooks: installed=%t path=%s\n", status.Installed, status.HooksPath)
			fmt.Println(status.Message)
		case "factory":
			status := statuses["factory"].(endpointhooks.FactoryStatus)
			fmt.Printf("Factory hooks: installed=%t path=%s\n", status.Installed, status.SettingsPath)
			fmt.Println(status.Message)
		case "opencode":
			status := statuses["opencode"].(endpointhooks.OpenCodeStatus)
			fmt.Printf("opencode plugin: installed=%t path=%s\n", status.Installed, status.PluginPath)
			fmt.Println(status.Message)
		case "grok":
			status := statuses["grok"].(endpointhooks.GrokStatus)
			fmt.Printf("Grok hooks: installed=%t path=%s\n", status.Installed, status.HooksPath)
			fmt.Println(status.Message)
		case "hermes":
			status := statuses["hermes"].(endpointhooks.HermesStatus)
			fmt.Printf("Hermes Agent hooks: installed=%t path=%s\n", status.Installed, status.ConfigPath)
			fmt.Println(status.Message)
		case "devin-cli":
			status := statuses["devin-cli"].(endpointhooks.DevinStatus)
			fmt.Printf("Devin CLI hooks: installed=%t path=%s\n", status.Installed, status.ConfigPath)
			fmt.Println(status.Message)
		case "devin-desktop":
			status := statuses["devin-desktop"].(endpointhooks.DevinStatus)
			fmt.Printf("Devin Desktop hooks: installed=%t path=%s\n", status.Installed, status.ConfigPath)
			fmt.Println(status.Message)
		}
	}
	return nil
}
