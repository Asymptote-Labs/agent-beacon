package cmd

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/depscan"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/diff"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/state"
)

var postToolCmd = &cobra.Command{
	Use:   "post-tool",
	Short: "Record file-edit telemetry and dependency scan findings",
	Long: `PostToolUse hook - triggered after Write, Edit, or MultiEdit operations.
The public Beacon build records local metadata and dependency scan findings only.`,
	Run: runPostTool,
}

func init() {
	rootCmd.AddCommand(postToolCmd)
}

// evaluationParams holds the platform-independent fields needed for local hook handling.
type evaluationParams struct {
	sessionID string
	toolName  string
	filePath  string
	diffStr   string
}

func runPostTool(cmd *cobra.Command, args []string) {
	input, err := readStdinJSON()
	if err != nil {
		outputJSON(emptyResponse)
		return
	}

	// Resolve session ID early so we can use per-session logger
	sessionID := resolveSessionID(input, platformFlag)
	var logger *logging.Logger
	if sessionID != "" {
		logger = logging.NewSessionLogger("post-tool-async-scan", platformFlag, sessionID)
	} else {
		logger = logging.NewLoggerForPlatform("post-tool-async-scan", platformFlag)
	}

	var params *evaluationParams

	// Check if dep-scan is enabled before parsing, so parsers can allow dep files through
	depScanEnabled := config.IsDepScanEnabled()

	if platformFlag == "cursor" {
		// Cursor fires two hook types through post-tool:
		//   - afterFileEdit: has "edits" array and top-level "file_path" (no output supported)
		//   - postToolUse: has "tool_name" and "tool_input" (supports additional_context/followup via stop)
		// We use hook_event_name (present in all Cursor hook inputs) to distinguish them.
		hookEvent, _ := input["hook_event_name"].(string)
		if hookEvent != "afterFileEdit" {
			// postToolUse — used for dep scan only (result stored in state, delivered via stop hook)
			params = parseCursorPostToolInput(input, logger, depScanEnabled)
			if params != nil && depscan.IsDependencyFile(params.filePath) && depScanEnabled {
				runDepScan(params, logger)
				return
			}
			outputJSON(emptyResponse)
			return
		}
		// afterFileEdit — used for async evaluation only (no output supported).
		// Dep scan is handled by postToolUse above, so don't allow dep files through here.
		params = parseCursorInput(input, logger, false)
	} else {
		params = parseClaudeCopilotInput(input, logger, depScanEnabled)
	}

	if params == nil {
		outputJSON(emptyResponse)
		return
	}

	// Dep scan: if this is a dependency file and dep-scan is enabled, check for CVEs.
	if depscan.IsDependencyFile(params.filePath) && depScanEnabled {
		runDepScan(params, logger)
		return
	}

	recordLocalEdit(params, logger)
	outputJSON(emptyResponse)
}

// parseCursorInput extracts evaluation params from Cursor's afterFileEdit format.
func parseCursorInput(input map[string]interface{}, logger *logging.Logger, depScanEnabled bool) *evaluationParams {
	sessionID := resolveSessionID(input, "cursor")
	filePath, _ := input["file_path"].(string)
	if sessionID == "" || filePath == "" {
		return nil
	}

	if !config.IsScannableFile(filePath) && !(depScanEnabled && depscan.IsDependencyFile(filePath)) {
		logger.Debug("Skipping non-scannable file: " + filePath)
		return nil
	}

	// Construct diff from Cursor's edits array
	edits, _ := input["edits"].([]interface{})
	if len(edits) == 0 {
		logger.Debug("No edits in input, skipping")
		return nil
	}

	diffStr := diff.FromCursorEdits(filePath, edits)
	if diffStr == "" {
		logger.Debug("Could not construct diff from edits, skipping")
		return nil
	}

	logger.Debug("Constructed diff from cursor edits", "file_path", filePath, "num_edits", len(edits))

	return &evaluationParams{
		sessionID: sessionID,
		toolName:  "afterFileEdit",
		filePath:  filePath,
		diffStr:   diffStr,
	}
}

// parseClaudeCopilotInput extracts evaluation params from Claude/Copilot PostToolUse format.
func parseClaudeCopilotInput(input map[string]interface{}, logger *logging.Logger, depScanEnabled bool) *evaluationParams {
	var sessionID, toolName string
	var toolInput, toolResponse map[string]interface{}

	if platformFlag == "copilot" {
		sessionID = resolveSessionID(input, platformFlag)
		toolName = getFirstStr(input, "toolName", "tool_name")
		toolInput = resolveToolInput(input)
		toolResponse = resolveToolResponse(input)
	} else {
		sessionID, _ = input["session_id"].(string)
		toolName, _ = input["tool_name"].(string)
		toolInput, _ = input["tool_input"].(map[string]interface{})
		toolResponse, _ = input["tool_response"].(map[string]interface{})
	}

	if !isFileEditTool(platformFlag, toolName) {
		return nil
	}

	filePath := diff.GetStringFromMaps("file_path", toolInput, toolResponse)
	if filePath == "" {
		filePath = diff.GetStringFromMaps("filePath", toolInput, toolResponse)
	}

	if sessionID == "" || toolName == "" || filePath == "" {
		return nil
	}

	if !config.IsScannableFile(filePath) && !(depScanEnabled && depscan.IsDependencyFile(filePath)) {
		logger.Debug("Skipping non-scannable file: " + filePath)
		return nil
	}

	logger.Debug("Constructing diff", "tool_name", toolName, "file_path", filePath,
		"has_tool_input", toolInput != nil, "has_tool_response", toolResponse != nil)

	diffStr := diff.FromToolResponse(toolName, toolInput, toolResponse)
	if diffStr == "" {
		logger.Debug("Could not construct diff, skipping", "tool_name", toolName)
		return nil
	}

	return &evaluationParams{
		sessionID: sessionID,
		toolName:  toolName,
		filePath:  filePath,
		diffStr:   diffStr,
	}
}

// recordLocalEdit logs file-edit metadata without sending code or diffs to a hosted service.
func recordLocalEdit(params *evaluationParams, logger *logging.Logger) {
	logger.Info("File edit observed", "file_path", params.filePath, "tool_name", params.toolName)
}

// resolveToolInput extracts tool input from Copilot's various formats.
func resolveToolInput(input map[string]interface{}) map[string]interface{} {
	if m, ok := input["tool_input"].(map[string]interface{}); ok {
		return m
	}
	// Fallback: some Copilot versions send toolArgs as stringified JSON
	if argsStr, ok := input["toolArgs"].(string); ok && argsStr != "" {
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(argsStr), &parsed); err == nil {
			return parsed
		}
	}
	if m, ok := input["toolArgs"].(map[string]interface{}); ok {
		return m
	}
	return nil
}

// resolveToolResponse extracts tool response from Copilot's various formats.
func resolveToolResponse(input map[string]interface{}) map[string]interface{} {
	if m, ok := input["tool_response"].(map[string]interface{}); ok {
		return m
	}
	if m, ok := input["toolResult"].(map[string]interface{}); ok {
		return m
	}
	// If tool_response is a plain string, wrap it for downstream compatibility
	if respStr, ok := input["tool_response"].(string); ok && respStr != "" {
		return map[string]interface{}{"result": respStr}
	}
	return nil
}

// runDepScan handles the dep-scan path: parse diff for packages, query OSV, return CVE report.
func runDepScan(params *evaluationParams, logger *logging.Logger) {
	start := time.Now()

	logger.Debug("Dep file detected", "file_path", params.filePath)

	packages := depscan.ParseDiffForPackages(params.diffStr, params.filePath)
	if len(packages) == 0 {
		logger.Debug("No packages found in diff", "file_path", params.filePath)
		outputJSON(emptyResponse)
		return
	}

	pkgNames := make([]string, len(packages))
	for i, p := range packages {
		pkgNames[i] = p.Name + "@" + p.Version
	}
	logger.Info("Packages parsed from diff", "file_path", params.filePath, "package_count", len(packages), "packages", strings.Join(pkgNames, ", "))

	// Query OSV.dev for vulnerabilities
	findings := depscan.QueryPackages(packages, logger)

	elapsed := time.Since(start)

	if len(findings) == 0 {
		logger.Info("No vulnerabilities found", "package_count", len(packages), "duration_ms", elapsed.Milliseconds())
		outputJSON(emptyResponse)
		return
	}

	// Deduplicate and format
	deduplicated := depscan.DeduplicateFindings(packages, findings)
	report := depscan.FormatCVEReport(deduplicated)

	totalCVEs := 0
	for _, f := range deduplicated {
		totalCVEs += len(f.CVEs)
	}

	logger.Info("Vulnerabilities found",
		"total_vulns", totalCVEs,
		"affected_packages", len(deduplicated),
		"duration_ms", elapsed.Milliseconds())

	// Return platform-appropriate response
	if platformFlag == "cursor" {
		// Cursor's postToolUse additional_context is unreliable — store report
		// in session state for delivery via the stop hook's followup_message.
		st := state.NewSessionState(params.sessionID, platformFlag)
		st.SetDepScanReport(report)
		logger.Info("CVE report stored for stop hook", "platform", "cursor", "affected_packages", len(deduplicated), "cve_count", totalCVEs, "duration_ms", elapsed.Milliseconds())
		outputJSON(emptyResponse)
	} else {
		logger.Info("CVE report injected", "platform", platformFlag, "affected_packages", len(deduplicated), "cve_count", totalCVEs, "duration_ms", elapsed.Milliseconds())
		outputJSON(map[string]interface{}{
			"decision": "block",
			"reason":   report,
			"hookSpecificOutput": map[string]interface{}{
				"hookEventName":     "PostToolUse",
				"additionalContext": report,
			},
		})
	}
}

// parseCursorPostToolInput extracts evaluation params from Cursor's postToolUse format.
// Cursor postToolUse has: conversation_id, tool_name, tool_input (map), tool_output (JSON string),
// workspace_roots (array), model, generation_id.
func parseCursorPostToolInput(input map[string]interface{}, logger *logging.Logger, depScanEnabled bool) *evaluationParams {
	sessionID := getFirstStr(input, "conversation_id")
	toolName := getFirstStr(input, "tool_name")
	toolInput, _ := input["tool_input"].(map[string]interface{})

	if !isFileEditTool(platformFlag, toolName) {
		return nil
	}

	filePath := diff.GetStringFromMaps("file_path", toolInput)
	if filePath == "" {
		return nil
	}

	if sessionID == "" || toolName == "" {
		return nil
	}

	if !config.IsScannableFile(filePath) && !(depScanEnabled && depscan.IsDependencyFile(filePath)) {
		return nil
	}

	// Build diff from content (Write/Create tool)
	content := diff.GetStringFromMaps("content", toolInput)
	if content == "" {
		return nil
	}
	diffStr := diff.FromToolResponse(toolName, toolInput, nil)
	if diffStr == "" {
		return nil
	}

	return &evaluationParams{
		sessionID: sessionID,
		toolName:  toolName,
		filePath:  filePath,
		diffStr:   diffStr,
	}
}

// isFileEditTool returns true if the tool name represents a file edit operation.
func isFileEditTool(platform, toolName string) bool {
	if platform == "copilot" {
		lower := strings.ToLower(toolName)
		return strings.Contains(lower, "edit") ||
			strings.Contains(lower, "write") ||
			strings.Contains(lower, "create") ||
			strings.Contains(lower, "patch")
	}
	if platform == "factory" {
		return toolName == "Write" || toolName == "Edit" || toolName == "MultiEdit" || toolName == "Create"
	}
	return toolName == "Write" || toolName == "Edit" || toolName == "MultiEdit"
}
