package cmd

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	endpointintegrations "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/integrations"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/integrations/cowork"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/integrations/openclaw"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

type doctorResult struct {
	Status      string              `json:"status"`
	Checks      []diagnostics.Check `json:"checks"`
	Fixes       []plannedAction     `json:"fixes,omitempty"`
	Skipped     []plannedAction     `json:"skipped,omitempty"`
	GeneratedAt string              `json:"generated_at"`
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
	result := buildDoctorResult(status, time.Now())
	if endpointOpts.fix {
		fixPlan := planDoctorFixes(result, status)
		result.Fixes = fixPlan.Fixes
		result.Skipped = fixPlan.Skipped
		var fixErr error
		if err := applyDoctorFixes(fixPlan, status); err != nil {
			fixErr = err
		}
		status = lifecycle.GetStatus(endpointUserMode(), endpointOpts.logPath)
		result = buildDoctorResult(status, time.Now())
		result.Fixes = fixPlan.Fixes
		result.Skipped = fixPlan.Skipped
		if err := printDoctorResult(result); err != nil {
			return err
		}
		if fixErr != nil {
			return fixErr
		}
	} else {
		if err := printDoctorResult(result); err != nil {
			return err
		}
	}
	if result.Status == diagnostics.StatusFail {
		return fmt.Errorf("endpoint health checks failed")
	}
	return nil
}

func buildDoctorResult(status lifecycle.Status, generatedAt time.Time) doctorResult {
	checks := []diagnostics.Check{
		configValidationCheck(status.RuntimeLog.EffectiveUserMode),
	}
	checks = append(checks, actionableChecks(status.Diagnostics, status.RuntimeLog)...)
	checks = append(checks, collectorCheck(status), serviceCheck(status), lastEventCheck(status))
	for _, h := range status.Harnesses {
		checks = append(checks, harnessCheck(h, status.LogPath, status.RuntimeLog.EffectiveUserMode))
	}
	result := doctorResult{
		Status:      aggregateCheckStatus(checks),
		Checks:      checks,
		GeneratedAt: generatedAt.UTC().Format(time.RFC3339),
	}
	return result
}

func printDoctorResult(result doctorResult) error {
	if endpointOpts.jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	fmt.Printf("Beacon endpoint doctor: %s\n", result.Status)
	printDoctorChecks(result.Checks, diagnostics.StatusFail)
	printDoctorChecks(result.Checks, diagnostics.StatusWarn)
	if len(result.Fixes) > 0 {
		fmt.Println("Applied fixes:")
		for _, fix := range result.Fixes {
			printPlannedAction(fix)
		}
	}
	if len(result.Skipped) > 0 {
		fmt.Println("Skipped fixes:")
		for _, skipped := range result.Skipped {
			printPlannedAction(skipped)
		}
	}
	failures, warnings := checkCounts(result.Checks)
	fmt.Printf("Summary: %d failure(s), %d warning(s)\n", failures, warnings)
	if result.Status == diagnostics.StatusOK {
		fmt.Println("All endpoint health checks passed.")
	}
	return nil
}

func printDoctorChecks(checks []diagnostics.Check, status string) {
	for _, check := range checks {
		if check.Status != status {
			continue
		}
		fmt.Printf("%s: %s", check.Name, check.Status)
		if check.Target != "" {
			fmt.Printf(" target=%s", check.Target)
		}
		if check.Message != "" {
			fmt.Printf(" (%s)", check.Message)
		}
		if check.Action != "" {
			fmt.Printf(" action=%q", check.Action)
		}
		fmt.Println()
	}
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
	if err := endpointconfig.ValidateDestinations(cfg.Destinations); err != nil {
		return err
	}
	fmt.Printf("Endpoint config is valid: %s\n", endpointconfig.ConfigPath(cfg.UserMode))
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
		printPlannedAction(action)
	}
	return nil
}

func printPlannedAction(action plannedAction) {
	fmt.Printf("%s", action.Action)
	if action.Target != "" {
		fmt.Printf(" %s", action.Target)
	}
	if action.Message != "" {
		fmt.Printf(" (%s)", action.Message)
	}
	fmt.Println()
}

func inventoryHookTargets() ([]string, error) {
	if endpointOpts.inventoryHooks && !endpointOpts.allTargets && strings.TrimSpace(endpointOpts.hookHarnesses) == "" {
		return allHookTargetsForLevel(), nil
	}
	return hookTargets()
}

func hookTargets() ([]string, error) {
	if endpointOpts.allTargets {
		return allHookTargetsForLevel(), nil
	}
	return canonicalHookTargets(splitCSV(endpointOpts.hookHarnesses))
}

func allHookTargetsForLevel() []string {
	all := []string{"cursor", "vscode", "factory", "opencode", "grok", "hermes", "devin-cli", "devin-desktop", "antigravity"}
	if endpointOpts.hookLevel != "project" {
		return all
	}
	filtered := all[:0:0]
	for _, t := range all {
		if t == "hermes" {
			continue
		}
		filtered = append(filtered, t)
	}
	return filtered
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

func configValidationCheck(userMode bool) diagnostics.Check {
	path := endpointconfig.ConfigPath(userMode)
	if _, err := endpointconfig.Load(userMode); err != nil {
		action := doctorInstallCommand(userMode)
		if !os.IsNotExist(err) {
			action = "fix the endpoint config JSON or run " + doctorRepairCommand(userMode)
		}
		return diagnostics.Check{
			Name:     "config_valid",
			Target:   path,
			Status:   diagnostics.StatusFail,
			Severity: diagnostics.SeverityHigh,
			Message:  err.Error(),
			Evidence: "config_load_failed",
			Action:   action,
		}
	}
	return diagnostics.Check{
		Name:     "config_valid",
		Target:   path,
		Status:   diagnostics.StatusOK,
		Severity: diagnostics.SeverityInfo,
		Message:  "endpoint config is valid",
		Evidence: "config_load_succeeded",
	}
}

func actionableChecks(checks []diagnostics.Check, runtimeLog lifecycle.RuntimeLogSource) []diagnostics.Check {
	out := make([]diagnostics.Check, 0, len(checks))
	for _, check := range checks {
		if check.Action == "" {
			check.Action = actionForCheck(check, runtimeLog)
		}
		out = append(out, check)
	}
	return out
}

func actionForCheck(check diagnostics.Check, runtimeLog lifecycle.RuntimeLogSource) string {
	switch check.Name {
	case "config", "collector_config", "launchd_plist", "collector_health":
		return doctorRepairCommand(runtimeLog.EffectiveUserMode)
	case "runtime_log":
		return "beacon endpoint doctor --fix"
	case "runtime_log_permissions":
		if check.Evidence == "runtime_log_missing" || check.Evidence == "missing_optional_file" {
			return "beacon endpoint doctor --fix"
		}
		return "chmod 644 " + check.Target
	case "runtime_log_source":
		if runtimeLog.RequestedUserMode && !runtimeLog.EffectiveUserMode {
			return "stop the system collector or run beacon endpoint install --user"
		}
		return "review the requested and effective runtime log paths"
	}
	return ""
}

func aggregateCheckStatus(checks []diagnostics.Check) string {
	out := diagnostics.StatusOK
	for _, check := range checks {
		if check.Status == diagnostics.StatusFail {
			return diagnostics.StatusFail
		}
		if check.Status == diagnostics.StatusWarn {
			out = diagnostics.StatusWarn
		}
	}
	return out
}

func collectorCheck(status lifecycle.Status) diagnostics.Check {
	if status.Collector.BinaryPath == "" {
		return diagnostics.Check{Name: "collector_reachability", Target: status.ConfigPath, Status: diagnostics.StatusFail, Severity: diagnostics.SeverityHigh, Message: status.Collector.Message, Evidence: "collector_binary_missing", Action: doctorRepairCommand(status.RuntimeLog.EffectiveUserMode)}
	}
	if status.Collector.GRPCReady && status.Collector.HTTPReady {
		return diagnostics.Check{Name: "collector_reachability", Target: status.ConfigPath, Status: diagnostics.StatusOK, Severity: diagnostics.SeverityInfo, Message: status.Collector.Message, Evidence: "collector_ready"}
	}
	if status.Collector.GRPCReady || status.Collector.HTTPReady {
		return diagnostics.Check{Name: "collector_reachability", Target: status.ConfigPath, Status: diagnostics.StatusWarn, Severity: diagnostics.SeverityMedium, Message: "only one OTLP receiver is listening", Evidence: "collector_partially_ready", Action: doctorRepairCommand(status.RuntimeLog.EffectiveUserMode)}
	}
	return diagnostics.Check{Name: "collector_reachability", Target: status.ConfigPath, Status: diagnostics.StatusFail, Severity: diagnostics.SeverityHigh, Message: status.Collector.Message, Evidence: "collector_not_ready", Action: doctorRepairCommand(status.RuntimeLog.EffectiveUserMode)}
}

func serviceCheck(status lifecycle.Status) diagnostics.Check {
	if status.Service.Running {
		return diagnostics.Check{Name: "service", Status: diagnostics.StatusOK, Severity: diagnostics.SeverityInfo, Message: status.Service.Message, Evidence: "service_running"}
	}
	if status.Service.Loaded {
		return diagnostics.Check{Name: "service", Status: diagnostics.StatusWarn, Severity: diagnostics.SeverityMedium, Message: status.Service.Message, Evidence: "service_loaded_not_running", Action: doctorRepairCommand(status.RuntimeLog.EffectiveUserMode)}
	}
	return diagnostics.Check{Name: "service", Status: diagnostics.StatusWarn, Severity: diagnostics.SeverityMedium, Message: status.Service.Message, Evidence: "service_not_loaded", Action: doctorRepairCommand(status.RuntimeLog.EffectiveUserMode)}
}

func lastEventCheck(status lifecycle.Status) diagnostics.Check {
	if status.LastEvent != "" {
		return diagnostics.Check{Name: "last_event", Target: status.LogPath, Status: diagnostics.StatusOK, Severity: diagnostics.SeverityInfo, Message: "runtime log has events", Evidence: "last_event_present"}
	}
	return diagnostics.Check{Name: "last_event", Target: status.LogPath, Status: diagnostics.StatusWarn, Severity: diagnostics.SeverityLow, Message: "runtime log has no events yet", Evidence: "last_event_missing", Action: "beacon endpoint test-event"}
}

func harnessCheck(h harness.Harness, logPath string, effectiveUserMode bool) diagnostics.Check {
	if !h.Detected {
		return diagnostics.Check{Name: "harness", Target: h.Name, Status: diagnostics.StatusOK, Severity: diagnostics.SeverityInfo, Message: "not installed", Evidence: "not_installed"}
	}
	if h.TelemetryStatus == harness.TelemetryEnabled {
		if !harnessEventObserved(logPath, h.Name) {
			return diagnostics.Check{Name: "harness_observed", Target: h.Name, Status: diagnostics.StatusWarn, Severity: diagnostics.SeverityLow, Message: "telemetry is configured but no matching event has been observed yet", Evidence: "configured_not_observed", Action: "run " + h.DisplayName + " or beacon endpoint test-event"}
		}
		return diagnostics.Check{Name: "harness", Target: h.Name, Status: diagnostics.StatusOK, Severity: diagnostics.SeverityInfo, Message: h.Message, Evidence: "configured"}
	}
	return diagnostics.Check{Name: "harness", Target: h.Name, Status: diagnostics.StatusWarn, Severity: diagnostics.SeverityMedium, Message: h.Message, Evidence: string(h.TelemetryStatus), Action: harnessAction(h, effectiveUserMode)}
}

func harnessEventObserved(logPath, name string) bool {
	return endpointintegrations.HasRecentHarnessEvent(logPath, name)
}

func harnessAction(h harness.Harness, effectiveUserMode bool) string {
	switch h.Capability {
	case "hooks", "plugin":
		return "beacon endpoint hooks install --harness " + h.Name
	case "otel_env", "otel_config":
		return doctorRepairCommand(effectiveUserMode)
	case "admin_otel":
		return "beacon endpoint integrations claude-cowork setup"
	}
	return ""
}

func checkCounts(checks []diagnostics.Check) (int, int) {
	failures := 0
	warnings := 0
	for _, check := range checks {
		switch check.Status {
		case diagnostics.StatusFail:
			failures++
		case diagnostics.StatusWarn:
			warnings++
		}
	}
	return failures, warnings
}

func doctorRepairCommand(userMode bool) string {
	if userMode {
		return "beacon endpoint repair --user"
	}
	return "sudo beacon endpoint repair --system"
}

func doctorInstallCommand(userMode bool) string {
	if userMode {
		return "beacon endpoint install --user"
	}
	return "sudo beacon endpoint install --system"
}

type doctorFixPlan struct {
	Fixes   []plannedAction
	Skipped []plannedAction
}

func planDoctorFixes(result doctorResult, status lifecycle.Status) doctorFixPlan {
	plan := doctorFixPlan{}
	addFix := func(action plannedAction) {
		for _, existing := range plan.Fixes {
			if existing.Action == action.Action && existing.Target == action.Target {
				return
			}
		}
		plan.Fixes = append(plan.Fixes, action)
	}
	addSkip := func(action plannedAction) {
		for _, existing := range plan.Skipped {
			if existing.Action == action.Action && existing.Target == action.Target {
				return
			}
		}
		plan.Skipped = append(plan.Skipped, action)
	}

	configUsable := true
	for _, check := range result.Checks {
		if check.Name == "config_valid" && check.Status == diagnostics.StatusFail {
			configUsable = false
			addSkip(plannedAction{Action: "manual_fix", Target: check.Target, Message: "endpoint config could not be repaired safely: " + check.Message})
		}
	}

	for _, check := range result.Checks {
		if check.Status == diagnostics.StatusOK {
			continue
		}
		switch check.Name {
		case "runtime_log", "runtime_log_permissions", "last_event":
			if check.Evidence == "missing_optional_file" || check.Evidence == "runtime_log_missing" {
				addFix(plannedAction{Action: "create_runtime_log", Target: status.LogPath, Message: "create runtime log file and parent directory"})
			} else if check.Evidence == "last_event_missing" {
				addSkip(plannedAction{Action: "manual_fix", Target: check.Target, Message: "run beacon endpoint test-event or generate a runtime event"})
			}
		case "collector_config", "launchd_plist", "collector_health", "collector_reachability", "service":
			if runtime.GOOS != "darwin" {
				addSkip(plannedAction{Action: "repair_collector_service", Target: endpointconfig.ConfigPath(status.RuntimeLog.EffectiveUserMode), Message: "launchd service repair is only available on macOS"})
			} else if configUsable {
				addFix(plannedAction{Action: "repair_collector_service", Target: endpointconfig.ConfigPath(status.RuntimeLog.EffectiveUserMode), Message: "recreate managed collector config and launchd service"})
			} else {
				addSkip(plannedAction{Action: "repair_collector_service", Target: endpointconfig.ConfigPath(status.RuntimeLog.EffectiveUserMode), Message: "skipped because endpoint config is invalid"})
			}
		case "config", "config_valid":
			if check.Status == diagnostics.StatusFail {
				addSkip(plannedAction{Action: "manual_fix", Target: check.Target, Message: "review endpoint config or run " + doctorInstallCommand(status.RuntimeLog.EffectiveUserMode)})
			}
		case "harness":
			message := "review harness configuration manually"
			if check.Action != "" {
				message = "run " + check.Action
			}
			addSkip(plannedAction{Action: "manual_fix", Target: check.Target, Message: message})
		case "runtime_log_source":
			addSkip(plannedAction{Action: "manual_fix", Target: check.Target, Message: check.Action})
		}
	}
	return plan
}

func applyDoctorFixes(plan doctorFixPlan, status lifecycle.Status) error {
	var errs []error
	for _, action := range plan.Fixes {
		switch action.Action {
		case "create_runtime_log":
			if err := ensureLogFile(action.Target); err != nil {
				errs = append(errs, fmt.Errorf("%s %s: %w", action.Action, action.Target, err))
			}
		case "repair_collector_service":
			if err := repairCollectorServiceFromStatus(status); err != nil {
				errs = append(errs, fmt.Errorf("%s %s: %w", action.Action, action.Target, err))
			}
		}
	}
	return errors.Join(errs...)
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
	summaries := []map[string]interface{}{}
	err := writer.ScanEvents(logPath, func(raw []byte, event schema.Event) error {
		hash := fmt.Sprintf("%x", sha256.Sum256(raw))
		summaries = append(summaries, map[string]interface{}{
			"timestamp": event.Timestamp,
			"category":  event.Event.Category,
			"action":    event.Event.Action,
			"severity":  event.Severity,
			"harness":   event.Harness.Name,
			"hash":      hash,
		})
		return nil
	})
	if err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(out, "event-summaries.json"), summaries); err != nil {
		return err
	}
	if includeRaw {
		data, err := os.ReadFile(logPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		return os.WriteFile(filepath.Join(out, "runtime.raw.jsonl"), data, 0600)
	}
	return nil
}

func syntheticEvent(destination string) schema.Event {
	mode := "local_jsonl"
	message := "Beacon endpoint pipeline validation event"
	// Forwarding-destination modes/messages come from the destination registry
	// (see siemDestinations); everything else keeps the generic pipeline default.
	if m, msg, ok := destinationValidationMeta(destination); ok {
		mode = m
		message = msg
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
