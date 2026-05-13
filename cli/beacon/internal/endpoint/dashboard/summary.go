package dashboard

type Summary struct {
	TotalEvents      int            `json:"total_events"`
	LastEventTime    string         `json:"last_event_time,omitempty"`
	ActiveSessions   int            `json:"active_sessions"`
	MalformedLines   int            `json:"malformed_lines"`
	CountsByAction   map[string]int `json:"counts_by_action"`
	CountsByHarness  map[string]int `json:"counts_by_harness"`
	CountsBySeverity map[string]int `json:"counts_by_severity"`
	PromptEvents     int            `json:"prompt_events"`
	ToolEvents       int            `json:"tool_events"`
	CommandEvents    int            `json:"command_events"`
	FileEvents       int            `json:"file_events"`
	MCPEvents        int            `json:"mcp_events"`
	ApprovalEvents   int            `json:"approval_events"`
}

func BuildSummary(result EventResult) Summary {
	summary := Summary{
		TotalEvents:      result.TotalMatched,
		MalformedLines:   result.MalformedLines,
		CountsByAction:   map[string]int{},
		CountsByHarness:  map[string]int{},
		CountsBySeverity: map[string]int{},
	}
	sessions := map[string]bool{}
	for _, record := range result.Events {
		event := record.Event
		if summary.LastEventTime == "" || event.Timestamp > summary.LastEventTime {
			summary.LastEventTime = event.Timestamp
		}
		if event.Event.Action != "" {
			summary.CountsByAction[event.Event.Action]++
		}
		if event.Harness.Name != "" {
			summary.CountsByHarness[event.Harness.Name]++
		}
		if event.Severity != "" {
			summary.CountsBySeverity[string(event.Severity)]++
		}
		if event.Session != nil && event.Session.ID != "" {
			sessions[event.Session.ID] = true
		}
		promptEvent := event.Event.Category == "prompt"
		toolEvent := event.Event.Category == "tool" || event.Tool != nil
		commandEvent := event.Event.Category == "command" || event.Command != nil
		fileEvent := event.Event.Category == "file" || event.File != nil
		mcpEvent := event.Event.Category == "mcp" || event.MCP != nil
		approvalEvent := event.Event.Category == "approval" || event.Approval != nil
		switch event.Event.Category {
		case "prompt":
			summary.PromptEvents++
		}
		if !promptEvent && event.Event.Action == "prompt.submitted" {
			summary.PromptEvents++
		}
		if toolEvent {
			summary.ToolEvents++
		}
		if commandEvent {
			summary.CommandEvents++
		}
		if fileEvent {
			summary.FileEvents++
		}
		if mcpEvent {
			summary.MCPEvents++
		}
		if approvalEvent {
			summary.ApprovalEvents++
		}
	}
	summary.ActiveSessions = len(sessions)
	return summary
}
