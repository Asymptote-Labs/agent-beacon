package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	endpointinventory "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/inventory"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
	"github.com/spf13/cobra"
)

type inventoryResult struct {
	GeneratedAt       string                        `json:"generated_at"`
	RuntimeLog        lifecycle.RuntimeLogSource    `json:"runtime_log"`
	ConfigPath        string                        `json:"config_path"`
	LogPath           string                        `json:"log_path"`
	Harnesses         []harness.Harness             `json:"harnesses"`
	Hooks             map[string]hookTargetResult   `json:"hooks,omitempty"`
	Destinations      lifecycle.DestinationStatus   `json:"destinations"`
	LastEventObserved bool                          `json:"last_event_observed"`
	Configs           []endpointinventory.Config    `json:"configs,omitempty"`
	MCPServers        []endpointinventory.MCPServer `json:"mcp_servers,omitempty"`
	Skills            []endpointinventory.Skill     `json:"skills,omitempty"`
	UserScope         endpointinventory.UserScope   `json:"user_scope"`
}

func runEndpointInventory(cmd *cobra.Command, args []string) error {
	status := lifecycle.GetStatus(endpointUserMode(), endpointOpts.logPath)
	effectiveCfg := loadConfigForMode(status.RuntimeLog.EffectiveUserMode, status.LogPath)
	hookTargetNames, err := inventoryHookTargets()
	if err != nil {
		return err
	}
	configInventory := endpointinventory.Scan(endpointinventory.Options{})
	result := inventoryResult{
		GeneratedAt:       time.Now().UTC().Format(time.RFC3339),
		RuntimeLog:        status.RuntimeLog,
		ConfigPath:        status.ConfigPath,
		LogPath:           status.LogPath,
		Harnesses:         status.Harnesses,
		Hooks:             hookStatusesWithConfig(hookTargetNames, effectiveCfg),
		Destinations:      status.Destinations,
		LastEventObserved: status.LastEvent != "",
		Configs:           configInventory.Configs,
		MCPServers:        configInventory.MCPServers,
		Skills:            configInventory.Skills,
		UserScope:         configInventory.UserScope,
	}
	if endpointOpts.jsonOutput {
		result = filterInventoryDefaults(result)
		result = filterInventorySections(result)
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			return err
		}
		if endpointOpts.writeInventoryEvent {
			return writeInventoryEvents(effectiveCfg, configInventory)
		}
		return nil
	}
	showAllInventorySections := !inventorySectionFilterActive()
	if showAllInventorySections {
		fmt.Printf("Config: %s\n", result.ConfigPath)
		fmt.Printf("Runtime log: %s\n", result.LogPath)
		for _, h := range result.Harnesses {
			if !endpointOpts.allTargets && !h.Detected {
				continue
			}
			fmt.Printf("Harness: %s detected=%t telemetry=%s\n", h.DisplayName, h.Detected, h.TelemetryStatus)
		}
	}
	if showAllInventorySections || endpointOpts.inventoryHooks {
		for _, name := range hookTargetNames {
			if hook, ok := result.Hooks[name]; ok {
				fmt.Printf("Hook: %s status=%s installed=%t\n", name, hook.Status, hook.Installed)
			}
		}
	}
	for _, config := range result.Configs {
		if !endpointOpts.allTargets && !config.Exists {
			continue
		}
		if !inventoryConfigIncluded(config) {
			continue
		}
		path := config.Path
		if path == "" {
			path = config.PathHash
		}
		fmt.Printf("Config: %s scope=%s status=%s mcp_servers=%d path=%s\n", config.Runtime, config.Scope, config.ParserStatus, config.MCPServerCount, path)
	}
	if showAllInventorySections || endpointOpts.inventoryMCP {
		for _, server := range result.MCPServers {
			name := server.ServerName
			if name == "" {
				name = server.ServerNameHash
			}
			fmt.Printf("MCP: %s %s scope=%s transport=%s command_present=%t args=%d env_keys=%d\n", server.Runtime, name, server.SourceScope, server.Transport, server.CommandPresent, server.ArgsCount, server.EnvKeyCount)
		}
	}
	if showAllInventorySections || endpointOpts.inventorySkills {
		for _, skill := range result.Skills {
			if !endpointOpts.allTargets && !skill.Exists {
				continue
			}
			name := skill.SkillName
			if name == "" {
				name = skill.SkillNameHash
			}
			path := skill.ManifestPath
			if path == "" {
				path = skill.RootPath
			}
			if path == "" {
				path = skill.ManifestPathHash
			}
			fmt.Printf("Skill: %s %s scope=%s status=%s path=%s\n", skill.Runtime, name, skill.SourceScope, skill.ParserStatus, path)
		}
	}
	if endpointOpts.writeInventoryEvent {
		return writeInventoryEvents(effectiveCfg, configInventory)
	}
	return nil
}

func filterInventoryDefaults(result inventoryResult) inventoryResult {
	if endpointOpts.allTargets {
		return result
	}
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

	filteredSkills := []endpointinventory.Skill{}
	for _, s := range result.Skills {
		if s.Exists {
			filteredSkills = append(filteredSkills, s)
		}
	}
	result.Skills = filteredSkills
	return result
}

func inventorySectionFilterActive() bool {
	return endpointOpts.inventoryMCP || endpointOpts.inventorySkills || endpointOpts.inventoryHooks
}

func filterInventorySections(result inventoryResult) inventoryResult {
	if !inventorySectionFilterActive() {
		return result
	}
	result.Harnesses = nil
	if !endpointOpts.inventoryHooks {
		result.Hooks = nil
	}
	if !endpointOpts.inventoryMCP {
		result.MCPServers = nil
	}
	if !endpointOpts.inventorySkills {
		result.Skills = nil
	}
	filteredConfigs := []endpointinventory.Config{}
	for _, config := range result.Configs {
		if inventoryConfigIncluded(config) {
			filteredConfigs = append(filteredConfigs, config)
		}
	}
	result.Configs = filteredConfigs
	return result
}

func inventoryConfigIncluded(config endpointinventory.Config) bool {
	if !inventorySectionFilterActive() {
		return true
	}
	return (endpointOpts.inventoryMCP && config.MCPServerCount > 0) ||
		(endpointOpts.inventoryHooks && config.ConfigKind == endpointinventory.KindHookConfig)
}

func writeInventoryEvents(cfg endpointconfig.Config, result endpointinventory.Result) error {
	settings := endpointconfig.InventoryConfig(cfg)
	result = filterInventoryResult(result, settings.Runtimes)
	_, err := writeInventorySnapshotEvents(cfg, settings, result, "manual", "", true, endpointinventory.State{})
	return err
}

type inventoryHeartbeatWriteResult struct {
	Written         bool   `json:"written"`
	SnapshotWritten bool   `json:"snapshot_written"`
	SkippedReason   string `json:"skipped_reason,omitempty"`
	SnapshotDigest  string `json:"snapshot_digest,omitempty"`
	PreviousDigest  string `json:"previous_snapshot_digest,omitempty"`
}

const inventoryAttemptBackoff = 5 * time.Minute

func runEndpointInventoryHeartbeat(cmd *cobra.Command, args []string) error {
	cfg, err := loadInventoryHeartbeatConfig(endpointUserMode(), endpointOpts.logPath, endpointOpts.inventoryHeartbeatConfig)
	if err != nil {
		return err
	}
	settings := endpointconfig.InventoryConfig(cfg)
	result, err := writeInventoryHeartbeat(cfg, settings, endpointOpts.inventoryHeartbeatForce, endpointOpts.inventoryWorkingDir, endpointOpts.inventoryTrigger, endpointOpts.inventoryTriggerHarness)
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(result)
	}
	return err
}

func loadInventoryHeartbeatConfig(userMode bool, logPath, configPath string) (endpointconfig.Config, error) {
	if configPath == "" {
		return loadConfigForMode(userMode, logPath), nil
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return endpointconfig.Config{}, err
	}
	var cfg endpointconfig.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return endpointconfig.Config{}, err
	}
	endpointconfig.NormalizeDestinations(&cfg)
	if err := endpointconfig.ValidateDestinations(cfg.Destinations); err != nil {
		return endpointconfig.Config{}, err
	}
	if logPath != "" {
		cfg.LogPath = logPath
	}
	if cfg.LogPath == "" {
		cfg.LogPath = writer.DefaultPath(cfg.UserMode)
	}
	return cfg, nil
}

func writeInventoryHeartbeat(cfg endpointconfig.Config, settings endpointconfig.InventorySettings, force bool, workingDir, trigger, triggerHarness string) (inventoryHeartbeatWriteResult, error) {
	if !settings.Enabled && !force {
		return inventoryHeartbeatWriteResult{SkippedReason: "disabled"}, nil
	}
	now := time.Now().UTC()
	statePath := endpointinventory.StatePathForLog(cfg.LogPath, cfg.UserMode)
	locked, state, err := endpointinventory.LockState(statePath)
	if err != nil {
		return inventoryHeartbeatWriteResult{}, err
	}
	defer locked.Close()
	if !force && !endpointinventory.TTLExpired(state, now, settings.TTLSeconds) {
		return inventoryHeartbeatWriteResult{SkippedReason: "ttl_active", PreviousDigest: state.LastSnapshotDigest}, nil
	}
	if !force && endpointinventory.AttemptBackoffActive(state, now, inventoryAttemptBackoff) {
		return inventoryHeartbeatWriteResult{SkippedReason: "attempt_backoff", PreviousDigest: state.LastSnapshotDigest}, nil
	}
	scanOpts := endpointinventory.Options{
		WorkingDir: workingDir,
		Runtimes:   settings.Runtimes,
		Now:        func() time.Time { return now },
	}
	inventoryResult := endpointinventory.Scan(scanOpts)
	digest := endpointinventory.SnapshotDigest(inventoryResult)
	writeSnapshot := state.LastSnapshotDigest != digest
	if err := locked.Save(endpointinventory.State{
		LastEmittedAt:      state.LastEmittedAt,
		LastSnapshotDigest: state.LastSnapshotDigest,
		LastAttemptAt:      now.Format(time.RFC3339),
	}); err != nil {
		return inventoryHeartbeatWriteResult{SnapshotDigest: digest, PreviousDigest: state.LastSnapshotDigest}, err
	}
	writeResult, err := writeInventorySnapshotEvents(cfg, settings, inventoryResult, trigger, triggerHarness, writeSnapshot, state)
	if err != nil {
		return writeResult, err
	}
	if err := locked.Save(endpointinventory.State{
		LastEmittedAt:      now.Format(time.RFC3339),
		LastSnapshotDigest: digest,
	}); err != nil {
		return inventoryHeartbeatWriteResult{SnapshotDigest: digest, PreviousDigest: state.LastSnapshotDigest}, err
	}
	return writeResult, nil
}

func writeInventorySnapshotEvents(cfg endpointconfig.Config, settings endpointconfig.InventorySettings, result endpointinventory.Result, trigger, triggerHarness string, writeSnapshot bool, state endpointinventory.State) (inventoryHeartbeatWriteResult, error) {
	digest := endpointinventory.SnapshotDigest(result)
	previousDigest := state.LastSnapshotDigest
	counts := endpointinventory.CountsFor(result)
	inventoryMeta := map[string]interface{}{
		"generated_at":             result.GeneratedAt,
		"runtimes":                 settings.Runtimes,
		"trigger":                  trigger,
		"trigger_harness":          triggerHarness,
		"ttl_seconds":              settings.TTLSeconds,
		"snapshot_digest":          digest,
		"previous_snapshot_digest": previousDigest,
		"counts":                   counts,
	}
	if previousDigest == "" {
		inventoryMeta["change_reason"] = "initial"
	} else if previousDigest != digest {
		inventoryMeta["change_reason"] = "changed"
	} else {
		inventoryMeta["change_reason"] = "unchanged"
	}
	heartbeat := schema.NewEvent(schema.NewEventOptions{
		Action:       "inventory.heartbeat",
		Category:     "inventory",
		Severity:     schema.SeverityInfo,
		AgentVersion: version.GetVersion(),
		Harness:      schema.HarnessInfo{Name: "endpoint"},
		Message:      "Agent configuration inventory heartbeat observed",
	})
	heartbeat.Raw = map[string]interface{}{"inventory": inventoryMeta}
	inventoryLogPath := endpointinventory.LogPath(cfg.LogPath, cfg.UserMode)
	if _, err := writer.AppendEvent(heartbeat, writer.Options{Path: inventoryLogPath, UserMode: cfg.UserMode}); err != nil {
		return inventoryHeartbeatWriteResult{SnapshotDigest: digest, PreviousDigest: previousDigest}, err
	}
	out := inventoryHeartbeatWriteResult{Written: true, SnapshotDigest: digest, PreviousDigest: previousDigest}
	if !writeSnapshot && previousDigest != "" {
		return out, nil
	}
	snapshot := schema.NewEvent(schema.NewEventOptions{
		Action:       "inventory.snapshot",
		Category:     "inventory",
		Severity:     schema.SeverityInfo,
		AgentVersion: version.GetVersion(),
		Harness:      schema.HarnessInfo{Name: "endpoint"},
		Message:      "Agent configuration inventory snapshot observed",
	})
	snapshot.Raw = map[string]interface{}{
		"inventory": map[string]interface{}{
			"generated_at":             result.GeneratedAt,
			"runtimes":                 settings.Runtimes,
			"trigger":                  trigger,
			"trigger_harness":          triggerHarness,
			"ttl_seconds":              settings.TTLSeconds,
			"snapshot_digest":          digest,
			"previous_snapshot_digest": previousDigest,
			"counts":                   counts,
			"user_scope":               result.UserScope,
			"configs":                  existingInventoryConfigs(result.Configs),
			"mcp_servers":              result.MCPServers,
			"skills":                   existingInventorySkills(result.Skills),
		},
	}
	if _, err := writer.AppendEvent(snapshot, writer.Options{Path: inventoryLogPath, UserMode: cfg.UserMode}); err != nil {
		return out, err
	}
	out.SnapshotWritten = true
	return out, nil
}

func existingInventoryConfigs(configs []endpointinventory.Config) []endpointinventory.Config {
	out := make([]endpointinventory.Config, 0, len(configs))
	for _, config := range configs {
		if config.Exists {
			out = append(out, config)
		}
	}
	return out
}

func existingInventorySkills(skills []endpointinventory.Skill) []endpointinventory.Skill {
	out := make([]endpointinventory.Skill, 0, len(skills))
	for _, skill := range skills {
		if skill.Exists {
			out = append(out, skill)
		}
	}
	return out
}

func filterInventoryResult(result endpointinventory.Result, runtimes []string) endpointinventory.Result {
	allowed := map[string]bool{}
	for _, runtime := range runtimes {
		runtime = strings.TrimSpace(runtime)
		if runtime != "" {
			allowed[runtime] = true
		}
	}
	if len(allowed) == 0 {
		return result
	}
	configs := make([]endpointinventory.Config, 0, len(result.Configs))
	for _, config := range result.Configs {
		if allowed[config.Runtime] {
			configs = append(configs, config)
		}
	}
	servers := make([]endpointinventory.MCPServer, 0, len(result.MCPServers))
	for _, server := range result.MCPServers {
		if allowed[server.Runtime] {
			servers = append(servers, server)
		}
	}
	skills := make([]endpointinventory.Skill, 0, len(result.Skills))
	for _, skill := range result.Skills {
		if allowed[skill.Runtime] {
			skills = append(skills, skill)
		}
	}
	result.Configs = configs
	result.MCPServers = servers
	result.Skills = skills
	return result
}
