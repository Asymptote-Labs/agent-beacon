package cmd

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
	"github.com/spf13/cobra"
)

type validationStage struct {
	Name     string `json:"name"`
	Target   string `json:"target,omitempty"`
	Status   string `json:"status"`
	Severity string `json:"severity"`
	Message  string `json:"message,omitempty"`
	Evidence string `json:"evidence,omitempty"`
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
