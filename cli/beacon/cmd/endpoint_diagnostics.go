package cmd

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/integrations/cowork"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/integrations/openclaw"
	endpointinventory "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/inventory"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

type doctorResult struct {
	Status      string              `json:"status"`
	Checks      []diagnostics.Check `json:"checks"`
	GeneratedAt string              `json:"generated_at"`
}

type inventoryResult struct {
	GeneratedAt       string                          `json:"generated_at"`
	RuntimeLog        lifecycle.RuntimeLogSource      `json:"runtime_log"`
	ConfigPath        string                          `json:"config_path"`
	LogPath           string                          `json:"log_path"`
	ContentRetention  endpointconfig.ContentRetention `json:"content_retention"`
	Harnesses         []harness.Harness               `json:"harnesses"`
	Hooks             map[string]hookTargetResult     `json:"hooks,omitempty"`
	Destinations      lifecycle.DestinationStatus     `json:"destinations"`
	LastEventObserved bool                            `json:"last_event_observed"`
	Configs           []endpointinventory.Config      `json:"configs,omitempty"`
	MCPServers        []endpointinventory.MCPServer   `json:"mcp_servers,omitempty"`
	UserScope         endpointinventory.UserScope     `json:"user_scope"`
}

type hookTargetResult struct {
	Target    string      `json:"target"`
	Status    string      `json:"status"`
	Installed bool        `json:"installed,omitempty"`
	Message   string      `json:"message,omitempty"`
	Path      string      `json:"path,omitempty"`
	Raw       interface{} `json:"raw,omitempty"`
}

type validationStage struct {
	Name     string `json:"name"`
	Target   string `json:"target,omitempty"`
	Status   string `json:"status"`
	Severity string `json:"severity"`
	Message  string `json:"message,omitempty"`
	Evidence string `json:"evidence,omitempty"`
}

type plannedAction struct {
	Action  string `json:"action"`
	Target  string `json:"target,omitempty"`
	Message string `json:"message,omitempty"`
}

func runEndpointDoctor(cmd *cobra.Command, args []string) error {
	status := lifecycle.GetStatus(endpointUserMode(), endpointOpts.logPath)
	checks := append([]diagnostics.Check{}, status.Diagnostics...)
	checks = append(checks,
		collectorCheck(status),
		serviceCheck(status),
		lastEventCheck(status),
	)
	for _, h := range status.Harnesses {
		checks = append(checks, harnessCheck(h))
	}
	result := doctorResult{
		Status:      aggregateCheckStatus(checks),
		Checks:      checks,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if endpointOpts.jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	fmt.Printf("Beacon endpoint doctor: %s\n", result.Status)
	for _, check := range checks {
		if check.Status == "ok" {
			continue
		}
		fmt.Printf("%s: %s", check.Name, check.Status)
		if check.Target != "" {
			fmt.Printf(" target=%s", check.Target)
		}
		if check.Message != "" {
			fmt.Printf(" (%s)", check.Message)
		}
		fmt.Println()
	}
	if result.Status == "ok" {
		fmt.Println("All endpoint health checks passed.")
	}
	if result.Status == "fail" {
		return fmt.Errorf("endpoint health checks failed")
	}
	return nil
}

func runEndpointInventory(cmd *cobra.Command, args []string) error {
	status := lifecycle.GetStatus(endpointUserMode(), endpointOpts.logPath)
	effectiveCfg := loadConfigForMode(status.RuntimeLog.EffectiveUserMode, status.LogPath)
	hookTargetNames, err := hookTargets()
	if err != nil {
		return err
	}
	configInventory := endpointinventory.Scan(endpointinventory.Options{ContentRetention: string(effectiveCfg.ContentRetention)})
	result := inventoryResult{
		GeneratedAt:       time.Now().UTC().Format(time.RFC3339),
		RuntimeLog:        status.RuntimeLog,
		ConfigPath:        status.ConfigPath,
		LogPath:           status.LogPath,
		ContentRetention:  effectiveCfg.ContentRetention,
		Harnesses:         status.Harnesses,
		Hooks:             hookStatusesWithConfig(hookTargetNames, effectiveCfg),
		Destinations:      status.Destinations,
		LastEventObserved: status.LastEvent != "",
		Configs:           configInventory.Configs,
		MCPServers:        configInventory.MCPServers,
		UserScope:         configInventory.UserScope,
	}
	if endpointOpts.jsonOutput {
		if !endpointOpts.allTargets {
			filtered := []harness.Harness{}
			for _, h := range result.Harnesses {
				if h.Detected {
					filtered = append(filtered, h)
				}
			}
			result.Harnesses = filtered

			filteredConfigs := []endpointinventory.Config{}
			existingPaths := map[string]bool{}
			for _, c := range result.Configs {
				if c.Exists {
					filteredConfigs = append(filteredConfigs, c)
					existingPaths[c.PathHash] = true
				}
			}
			result.Configs = filteredConfigs

			filteredServers := []endpointinventory.MCPServer{}
			for _, s := range result.MCPServers {
				if existingPaths[s.SourcePathHash] {
					filteredServers = append(filteredServers, s)
				}
			}
			result.MCPServers = filteredServers
		}
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			return err
		}
		if endpointOpts.writeInventoryEvent {
			return writeInventoryEvents(effectiveCfg, configInventory)
		}
		return nil
	}
	fmt.Printf("Config: %s\n", result.ConfigPath)
	fmt.Printf("Runtime log: %s\n", result.LogPath)
	fmt.Printf("Content retention: %s\n", result.ContentRetention)
	for _, h := range result.Harnesses {
		if !endpointOpts.allTargets && !h.Detected {
			continue
		}
		fmt.Printf("Harness: %s detected=%t telemetry=%s\n", h.DisplayName, h.Detected, h.TelemetryStatus)
	}
	for _, name := range hookTargetNames {
		if hook, ok := result.Hooks[name]; ok {
			fmt.Printf("Hook: %s status=%s installed=%t\n", name, hook.Status, hook.Installed)
		}
	}
	for _, config := range result.Configs {
		if !endpointOpts.allTargets && !config.Exists {
			continue
		}
		path := config.Path
		if path == "" {
			path = config.PathHash
		}
		fmt.Printf("Config: %s scope=%s status=%s mcp_servers=%d path=%s\n", config.Runtime, config.Scope, config.ParserStatus, config.MCPServerCount, path)
	}
	for _, server := range result.MCPServers {
		name := server.ServerName
		if name == "" {
			name = server.ServerNameHash
		}
		fmt.Printf("MCP: %s %s scope=%s transport=%s command_present=%t args=%d env_keys=%d\n", server.Runtime, name, server.SourceScope, server.Transport, server.CommandPresent, server.ArgsCount, server.EnvKeyCount)
	}
	if endpointOpts.writeInventoryEvent {
		return writeInventoryEvents(effectiveCfg, configInventory)
	}
	return nil
}

func writeInventoryEvents(cfg endpointconfig.Config, result endpointinventory.Result) error {
	for _, config := range result.Configs {
		if !config.Exists {
			continue
		}
		servers := mcpServersForConfig(result.MCPServers, config)
		event := schema.NewEvent(schema.NewEventOptions{
			Action:       "config.inventory",
			Category:     "inventory",
			Severity:     schema.SeverityInfo,
			AgentVersion: version.GetVersion(),
			Harness: schema.HarnessInfo{
				Name:       config.Runtime,
				ConfigPath: config.Path,
			},
			Message: fmt.Sprintf("%s config inventory observed", config.Runtime),
		})
		event.Content = &schema.ContentInfo{
			Retention: string(cfg.ContentRetention),
			Included:  cfg.ContentRetention != endpointconfig.ContentRetentionMetadata,
			Redacted:  cfg.ContentRetention == endpointconfig.ContentRetentionRedacted,
		}
		event.Raw = map[string]interface{}{
			"inventory": map[string]interface{}{
				"config":      config,
				"mcp_servers": servers,
			},
		}
		if _, err := writer.AppendEvent(event, writer.Options{Path: cfg.LogPath, UserMode: cfg.UserMode}); err != nil {
			return err
		}
	}
	return nil
}

func mcpServersForConfig(servers []endpointinventory.MCPServer, config endpointinventory.Config) []endpointinventory.MCPServer {
	out := []endpointinventory.MCPServer{}
	for _, server := range servers {
		if server.Runtime == config.Runtime && server.SourcePathHash == config.PathHash {
			out = append(out, server)
		}
	}
	return out
}

func runEndpointTestEvent(cmd *cobra.Command, args []string) error {
	cfg := loadOrDefaultConfig()
	writableStage := stageFromCheck(checkLogWritable(cfg))
	stages := []validationStage{writableStage}
	if writableStage.Status != "ok" {
		if endpointOpts.jsonOutput {
			_ = json.NewEncoder(os.Stdout).Encode(stages)
		} else {
			fmt.Printf("%s: %s", writableStage.Name, writableStage.Status)
			if writableStage.Target != "" {
				fmt.Printf(" target=%s", writableStage.Target)
			}
			if writableStage.Message != "" {
				fmt.Printf(" (%s)", writableStage.Message)
			}
			fmt.Println()
		}
		return fmt.Errorf("runtime log is not writable: %s", writableStage.Evidence)
	}
	path, err := writeValidationEvent(cfg, "pipeline")
	if err != nil {
		stages = append(stages, validationStage{Name: "write_test_event", Target: cfg.LogPath, Status: "fail", Severity: "high", Message: err.Error(), Evidence: "append_failed"})
		if endpointOpts.jsonOutput {
			_ = json.NewEncoder(os.Stdout).Encode(stages)
		}
		return err
	}
	stages = append(stages, validationStage{Name: "write_test_event", Target: path, Status: "ok", Severity: "info", Message: "synthetic validation event written", Evidence: "append_succeeded"})
	if endpointOpts.jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(stages)
	}
	for _, stage := range stages {
		fmt.Printf("%s: %s", stage.Name, stage.Status)
		if stage.Target != "" {
			fmt.Printf(" target=%s", stage.Target)
		}
		if stage.Message != "" {
			fmt.Printf(" (%s)", stage.Message)
		}
		fmt.Println()
	}
	return nil
}

func runEndpointBundleDiagnostics(cmd *cobra.Command, args []string) error {
	cfg := loadOrDefaultConfig()
	out := endpointOpts.outputDir
	if out == "" {
		out = filepath.Join(endpointconfig.BaseDir(endpointUserMode()), "diagnostics-"+time.Now().UTC().Format("20060102T150405Z"))
	}
	if err := os.MkdirAll(out, 0755); err != nil {
		return err
	}
	status := lifecycle.GetStatus(endpointUserMode(), endpointOpts.logPath)
	status.LastEvent = redactLastEvent(status.LastEvent)
	if err := writeJSONFile(filepath.Join(out, "status.json"), status); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(out, "config.redacted.json"), redactConfig(cfg)); err != nil {
		return err
	}
	if endpointOpts.includeEventSummaries || endpointOpts.includeRawEvents {
		if err := writeEventBundleFiles(out, cfg.LogPath, endpointOpts.includeRawEvents); err != nil {
			return err
		}
	}
	fmt.Printf("Diagnostics bundle written to %s\n", out)
	if !endpointOpts.includeRawEvents {
		fmt.Println("Raw runtime events were not included.")
	}
	return nil
}

func runEndpointConfigShow(cmd *cobra.Command, args []string) error {
	return json.NewEncoder(os.Stdout).Encode(redactConfig(loadOrDefaultConfig()))
}

func runEndpointConfigValidate(cmd *cobra.Command, args []string) error {
	cfg := loadOrDefaultConfig()
	if err := endpointconfig.ValidateContentRetention(cfg.ContentRetention); err != nil {
		return err
	}
	if err := endpointconfig.ValidateDestinations(cfg.Destinations); err != nil {
		return err
	}
	fmt.Printf("Endpoint config is valid: %s\n", endpointconfig.ConfigPath(cfg.UserMode))
	return nil
}

func runEndpointConfigSetRetention(cmd *cobra.Command, args []string) error {
	mode := endpointconfig.ContentRetention(args[0])
	if err := endpointconfig.ValidateContentRetention(mode); err != nil {
		return err
	}
	cfg := loadOrDefaultConfig()
	cfg.ContentRetention = mode
	path, err := endpointconfig.Save(cfg)
	if err != nil {
		return err
	}
	fmt.Printf("Content retention set to %s in %s\n", mode, path)
	return nil
}

func runEndpointIntegrationsValidate(cmd *cobra.Command, args []string) error {
	targets := []string{"claude-cowork", "openclaw"}
	if !endpointOpts.allTargets {
		return fmt.Errorf("specify --all to validate all admin integrations, or use a specific integration validate command")
	}
	cfg := loadOrDefaultConfig()
	results := map[string]validationStage{}
	broken := []string{}
	for _, target := range targets {
		switch target {
		case "claude-cowork":
			status := cowork.GetStatus(cfg.LogPath)
			stage := validationStage{Name: "integration_validate", Target: target, Status: "broken", Severity: "medium", Message: status.Message, Evidence: "not_observed"}
			if status.LastEventObserved {
				stage.Status = "configured"
				stage.Severity = "info"
				stage.Evidence = "last_event_observed"
			}
			if stage.Status == "broken" {
				broken = append(broken, target)
			}
			results[target] = stage
		case "openclaw":
			status := openclaw.GetStatus(cfg.LogPath)
			stage := validationStage{Name: "integration_validate", Target: target, Status: "broken", Severity: "medium", Message: status.Message, Evidence: "not_observed"}
			if status.LastEventObserved {
				stage.Status = "configured"
				stage.Severity = "info"
				stage.Evidence = "last_event_observed"
			}
			if stage.Status == "broken" {
				broken = append(broken, target)
			}
			results[target] = stage
		}
	}
	if endpointOpts.jsonOutput {
		if err := json.NewEncoder(os.Stdout).Encode(results); err != nil {
			return err
		}
		if len(broken) > 0 {
			return fmt.Errorf("integration validation failed for %s", strings.Join(broken, ", "))
		}
		return nil
	}
	for _, target := range targets {
		stage := results[target]
		fmt.Printf("%s: %s", target, stage.Status)
		if stage.Message != "" {
			fmt.Printf(" (%s)", stage.Message)
		}
		fmt.Println()
	}
	if len(broken) > 0 {
		return fmt.Errorf("integration validation failed for %s", strings.Join(broken, ", "))
	}
	return nil
}

func plannedInstallActions(repair bool) []plannedAction {
	cfg := endpointconfig.Default(endpointUserMode(), endpointOpts.logPath)
	if endpointOpts.logPath != "" {
		cfg.LogPath = endpointOpts.logPath
	}
	otlpTargets, hookTargets, _ := splitEndpointTargets(splitHarnessCSV(endpointOpts.harnesses))
	actions := []plannedAction{}
	if repair {
		actions = append(actions, plannedAction{Action: "unload_service", Message: "repair unloads existing endpoint service if present"})
	}
	actions = append(actions,
		plannedAction{Action: "write_file", Target: cfg.Collector.ConfigPath, Message: "collector configuration"},
		plannedAction{Action: "write_plist", Message: "launchd service definition"},
		plannedAction{Action: "write_file", Target: endpointconfig.ConfigPath(cfg.UserMode), Message: "endpoint configuration"},
	)
	for _, h := range otlpTargets {
		actions = append(actions, plannedAction{Action: "configure_harness", Target: h})
	}
	for _, h := range hookTargets {
		actions = append(actions, plannedAction{Action: "configure_harness", Target: h, Message: "install endpoint hook integration"})
	}
	if !endpointOpts.noStart {
		actions = append(actions, plannedAction{Action: "load_service", Message: "start endpoint collector service"})
	}
	return actions
}

func plannedUninstallActions() []plannedAction {
	cfg := loadOrDefaultConfig()
	actions := []plannedAction{{Action: "unload_service", Message: "stop endpoint collector service if present"}}
	if !endpointOpts.keepConfig {
		actions = append(actions, plannedAction{Action: "restore_backup", Message: "restore backed up harness configs when available"})
	}
	actions = append(actions, plannedAction{Action: "remove_file", Target: endpointconfig.ConfigPath(cfg.UserMode), Message: "endpoint config"})
	if !endpointOpts.keepLogs {
		actions = append(actions, plannedAction{Action: "remove_file", Target: cfg.LogPath, Message: "runtime log"})
	}
	return actions
}

func printPlannedActions(actions []plannedAction) error {
	if endpointOpts.jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(actions)
	}
	for _, action := range actions {
		fmt.Printf("%s", action.Action)
		if action.Target != "" {
			fmt.Printf(" %s", action.Target)
		}
		if action.Message != "" {
			fmt.Printf(" (%s)", action.Message)
		}
		fmt.Println()
	}
	return nil
}

func hookTargets() ([]string, error) {
	if endpointOpts.allTargets {
		all := []string{"cursor", "vscode", "factory", "opencode", "grok", "hermes", "devin-cli", "devin-desktop", "antigravity"}
		if endpointOpts.hookLevel == "project" {
			filtered := all[:0:0]
			for _, t := range all {
				if t == "hermes" {
					continue
				}
				filtered = append(filtered, t)
			}
			return filtered, nil
		}
		return all, nil
	}
	return canonicalHookTargets(splitCSV(endpointOpts.hookHarnesses))
}

func hookStatuses(targets []string) map[string]hookTargetResult {
	cfg := loadOrDefaultConfig()
	return hookStatusesWithConfig(targets, cfg)
}

func hookStatusesWithConfig(targets []string, cfg endpointconfig.Config) map[string]hookTargetResult {
	statuses := map[string]hookTargetResult{}
	canonical, err := canonicalHookTargets(targets)
	if err != nil {
		return statuses
	}
	for _, name := range canonical {
		switch strings.TrimSpace(name) {
		case "antigravity":
			status := endpointhooks.AntigravityHookStatus(endpointhooks.AntigravityOptions{Level: endpointhooks.Level(endpointOpts.hookLevel), LogPath: cfg.LogPath, UserMode: cfg.UserMode})
			statuses[name] = hookTargetResult{Target: name, Status: targetStatus(status.Installed), Installed: status.Installed, Message: status.Message, Path: status.ConfigPath, Raw: status}
		case "cursor":
			status := endpointhooks.CursorHookStatus(endpointhooks.CursorOptions{Level: endpointhooks.Level(endpointOpts.hookLevel), LogPath: cfg.LogPath, UserMode: cfg.UserMode})
			statuses[name] = hookTargetResult{Target: name, Status: targetStatus(status.Installed), Installed: status.Installed, Message: status.Message, Path: status.HooksJSONPath, Raw: status}
		case "claude":
			status := endpointhooks.ClaudeHookStatus(endpointhooks.ClaudeOptions{Level: endpointhooks.Level(endpointOpts.hookLevel), LogPath: cfg.LogPath, UserMode: cfg.UserMode})
			statuses[name] = hookTargetResult{Target: name, Status: targetStatus(status.Installed), Installed: status.Installed, Message: status.Message, Path: status.SettingsPath, Raw: status}
		case "vscode":
			status := endpointhooks.VSCodeHookStatus(endpointhooks.VSCodeOptions{Level: endpointhooks.Level(endpointOpts.hookLevel), LogPath: cfg.LogPath, UserMode: cfg.UserMode})
			statuses[name] = hookTargetResult{Target: name, Status: targetStatus(status.Installed), Installed: status.Installed, Message: status.Message, Path: status.HooksPath, Raw: status}
		case "factory":
			status := endpointhooks.FactoryHookStatus(endpointhooks.FactoryOptions{Level: endpointhooks.Level(endpointOpts.hookLevel), LogPath: cfg.LogPath, UserMode: cfg.UserMode})
			statuses[name] = hookTargetResult{Target: name, Status: targetStatus(status.Installed), Installed: status.Installed, Message: status.Message, Path: status.SettingsPath, Raw: status}
		case "opencode":
			status := endpointhooks.OpenCodeHookStatus(endpointhooks.OpenCodeOptions{Level: endpointhooks.Level(endpointOpts.hookLevel), LogPath: cfg.LogPath, UserMode: cfg.UserMode})
			statuses[name] = hookTargetResult{Target: name, Status: targetStatus(status.Installed), Installed: status.Installed, Message: status.Message, Path: status.PluginPath, Raw: status}
		case "grok":
			status := endpointhooks.GrokHookStatus(endpointhooks.GrokOptions{Level: endpointhooks.Level(endpointOpts.hookLevel), LogPath: cfg.LogPath, UserMode: cfg.UserMode})
			statuses[name] = hookTargetResult{Target: name, Status: targetStatus(status.Installed), Installed: status.Installed, Message: status.Message, Path: status.HooksPath, Raw: status}
		case "hermes":
			status := endpointhooks.HermesHookStatus(endpointhooks.HermesOptions{Level: endpointhooks.Level(endpointOpts.hookLevel), LogPath: cfg.LogPath, UserMode: cfg.UserMode})
			statuses[name] = hookTargetResult{Target: name, Status: targetStatus(status.Installed), Installed: status.Installed, Message: status.Message, Path: status.ConfigPath, Raw: status}
		case "devin-cli":
			status := endpointhooks.DevinHookStatus(endpointhooks.DevinOptions{Level: endpointhooks.Level(endpointOpts.hookLevel), LogPath: cfg.LogPath, UserMode: cfg.UserMode})
			statuses["devin-cli"] = hookTargetResult{Target: "devin-cli", Status: targetStatus(status.Installed), Installed: status.Installed, Message: status.Message, Path: status.ConfigPath, Raw: status}
		case "devin-desktop":
			status := endpointhooks.DevinDesktopHookStatus(endpointhooks.DevinDesktopOptions{Level: endpointhooks.Level(endpointOpts.hookLevel), LogPath: cfg.LogPath, UserMode: cfg.UserMode})
			statuses["devin-desktop"] = hookTargetResult{Target: "devin-desktop", Status: targetStatus(status.Installed), Installed: status.Installed, Message: status.Message, Path: status.ConfigPath, Raw: status}
		}
	}
	return statuses
}

func targetStatus(installed bool) string {
	if installed {
		return "configured"
	}
	return "not_installed"
}

func aggregateCheckStatus(checks []diagnostics.Check) string {
	out := "ok"
	for _, check := range checks {
		if check.Status == "fail" {
			return "fail"
		}
		if check.Status == "warn" {
			out = "warn"
		}
	}
	return out
}

func collectorCheck(status lifecycle.Status) diagnostics.Check {
	if status.Collector.GRPCReady || status.Collector.HTTPReady {
		return diagnostics.Check{Name: "collector_reachability", Target: status.ConfigPath, Status: "ok", Severity: "info", Message: status.Collector.Message, Evidence: "collector_ready"}
	}
	return diagnostics.Check{Name: "collector_reachability", Target: status.ConfigPath, Status: "fail", Severity: "high", Message: status.Collector.Message, Evidence: "collector_not_ready"}
}

func serviceCheck(status lifecycle.Status) diagnostics.Check {
	if status.Service.Running {
		return diagnostics.Check{Name: "service", Status: "ok", Severity: "info", Message: status.Service.Message, Evidence: "service_running"}
	}
	if status.Service.Loaded {
		return diagnostics.Check{Name: "service", Status: "warn", Severity: "medium", Message: status.Service.Message, Evidence: "service_loaded_not_running"}
	}
	return diagnostics.Check{Name: "service", Status: "warn", Severity: "medium", Message: status.Service.Message, Evidence: "service_not_loaded"}
}

func lastEventCheck(status lifecycle.Status) diagnostics.Check {
	if status.LastEvent != "" {
		return diagnostics.Check{Name: "last_event", Target: status.LogPath, Status: "ok", Severity: "info", Message: "runtime log has events", Evidence: "last_event_present"}
	}
	return diagnostics.Check{Name: "last_event", Target: status.LogPath, Status: "warn", Severity: "low", Message: "runtime log has no events yet", Evidence: "last_event_missing"}
}

func harnessCheck(h harness.Harness) diagnostics.Check {
	if !h.Detected {
		return diagnostics.Check{Name: "harness", Target: h.Name, Status: "ok", Severity: "info", Message: "not installed", Evidence: "not_installed"}
	}
	if h.TelemetryStatus == harness.TelemetryEnabled {
		return diagnostics.Check{Name: "harness", Target: h.Name, Status: "ok", Severity: "info", Message: h.Message, Evidence: "configured"}
	}
	return diagnostics.Check{Name: "harness", Target: h.Name, Status: "warn", Severity: "medium", Message: h.Message, Evidence: string(h.TelemetryStatus)}
}

func checkLogWritable(cfg endpointconfig.Config) diagnostics.Check {
	dir := filepath.Dir(cfg.LogPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return diagnostics.Check{Name: "runtime_log_writable", Target: cfg.LogPath, Status: "fail", Severity: "high", Message: err.Error(), Evidence: "mkdir_failed"}
	}
	file, err := os.OpenFile(cfg.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return diagnostics.Check{Name: "runtime_log_writable", Target: cfg.LogPath, Status: "fail", Severity: "high", Message: err.Error(), Evidence: "open_failed"}
	}
	_ = file.Close()
	return diagnostics.Check{Name: "runtime_log_writable", Target: cfg.LogPath, Status: "ok", Severity: "info", Message: "runtime log is writable", Evidence: "open_succeeded"}
}

func stageFromCheck(check diagnostics.Check) validationStage {
	return validationStage{Name: check.Name, Target: check.Target, Status: check.Status, Severity: check.Severity, Message: check.Message, Evidence: check.Evidence}
}

func redactLastEvent(raw string) string {
	if raw == "" {
		return ""
	}
	return "[present]"
}

func redactConfig(cfg endpointconfig.Config) endpointconfig.Config {
	if cfg.Destinations == nil {
		return cfg
	}
	destinations := *cfg.Destinations
	changed := false
	if cfg.Destinations.SplunkHEC != nil && cfg.Destinations.SplunkHEC.Token != "" {
		splunk := *cfg.Destinations.SplunkHEC
		splunk.Token = "[REDACTED]"
		destinations.SplunkHEC = &splunk
		changed = true
	}
	if cfg.Destinations.FalconHEC != nil && cfg.Destinations.FalconHEC.Token != "" {
		falcon := *cfg.Destinations.FalconHEC
		falcon.Token = "[REDACTED]"
		destinations.FalconHEC = &falcon
		changed = true
	}
	if changed {
		cfg.Destinations = &destinations
	}
	return cfg
}

func writeJSONFile(path string, value interface{}) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

func writeEventBundleFiles(out, logPath string, includeRaw bool) error {
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return writeJSONFile(filepath.Join(out, "event-summaries.json"), []map[string]interface{}{})
		}
		return err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	summaries := []map[string]interface{}{}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event schema.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(line)))
		summaries = append(summaries, map[string]interface{}{
			"timestamp": event.Timestamp,
			"category":  event.Event.Category,
			"action":    event.Event.Action,
			"severity":  event.Severity,
			"harness":   event.Harness.Name,
			"hash":      hash,
		})
	}
	if err := writeJSONFile(filepath.Join(out, "event-summaries.json"), summaries); err != nil {
		return err
	}
	if includeRaw {
		return os.WriteFile(filepath.Join(out, "runtime.raw.jsonl"), data, 0600)
	}
	return nil
}

func syntheticEvent(destination string) schema.Event {
	mode := "local_jsonl"
	message := "Beacon endpoint pipeline validation event"
	switch destination {
	case "wazuh":
		mode = "localfile"
		message = "Beacon endpoint Wazuh validation event"
	case "datadog":
		mode = "agent_file"
		message = "Beacon endpoint datadog validation event"
	case "sumo":
		mode = "http_source_jsonl"
		message = "Beacon endpoint Sumo validation event"
	case "rapid7":
		mode = "custom_logs_webhook_ndjson"
		message = "Beacon endpoint Rapid7 validation event"
	case "s3":
		mode = "aws_s3_jsonl"
		message = "Beacon endpoint S3 validation event"
	case "cloudwatch":
		mode = "aws_cloudwatch_logs"
		message = "Beacon endpoint AWS CloudWatch Logs validation event"
	case "gcs":
		mode = "google_cloud_storage_jsonl"
		message = "Beacon endpoint GCS validation event"
	case "sentinel":
		mode = "azure_monitor_agent_custom_json_logs"
		message = "Beacon endpoint Sentinel validation event"
	}
	event := schema.NewEvent(schema.NewEventOptions{
		Action:       "agent.detected",
		Category:     "validation",
		Severity:     schema.SeverityInfo,
		AgentVersion: version.GetVersion(),
		Harness:      schema.HarnessInfo{Name: "test_harness", Version: "test"},
		Message:      message,
	})
	event.Destination = &schema.DestinationInfo{Type: destination, Mode: mode, Status: "configured"}
	return event
}
