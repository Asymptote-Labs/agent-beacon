package dashboard

import "sort"

type Count struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Summary struct {
	TotalEvents              int            `json:"total_events"`
	LastEventTime            string         `json:"last_event_time,omitempty"`
	ActiveSessions           int            `json:"active_sessions"`
	MalformedLines           int            `json:"malformed_lines"`
	CountsByAction           map[string]int `json:"counts_by_action"`
	CountsByHarness          map[string]int `json:"counts_by_harness"`
	CountsBySeverity         map[string]int `json:"counts_by_severity"`
	CountsByCategory         map[string]int `json:"counts_by_category"`
	CountsByApprovalDecision map[string]int `json:"counts_by_approval_decision"`
	CountsByMCPServer        map[string]int `json:"counts_by_mcp_server"`
	CountsByRepository       map[string]int `json:"counts_by_repository"`
	CountsByModel            map[string]int `json:"counts_by_model"`
	PromptEvents             int            `json:"prompt_events"`
	ToolEvents               int            `json:"tool_events"`
	CommandEvents            int            `json:"command_events"`
	FileEvents               int            `json:"file_events"`
	MCPEvents                int            `json:"mcp_events"`
	ApprovalEvents           int            `json:"approval_events"`
	HighSeverityEvents       int            `json:"high_severity_events"`
	CriticalSeverityEvents   int            `json:"critical_severity_events"`
	NeedsReviewEvents        int            `json:"needs_review_events"`
	FailedToolEvents         int            `json:"failed_tool_events"`
	DeniedApprovalEvents     int            `json:"denied_approval_events"`
	PolicyBlockedEvents      int            `json:"policy_blocked_events"`
	TopActions               []Count        `json:"top_actions"`
	TopHarnesses             []Count        `json:"top_harnesses"`
	TopModels                []Count        `json:"top_models"`
	TopRepositories          []Count        `json:"top_repositories"`
	TopMCPServers            []Count        `json:"top_mcp_servers"`
}

func BuildSummary(result EventResult) Summary {
	summary := Summary{
		TotalEvents:              result.TotalMatched,
		MalformedLines:           result.MalformedLines,
		CountsByAction:           map[string]int{},
		CountsByHarness:          map[string]int{},
		CountsBySeverity:         map[string]int{},
		CountsByCategory:         map[string]int{},
		CountsByApprovalDecision: map[string]int{},
		CountsByMCPServer:        map[string]int{},
		CountsByRepository:       map[string]int{},
		CountsByModel:            map[string]int{},
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
		if event.Event.Category != "" {
			summary.CountsByCategory[event.Event.Category]++
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
		if event.Repository != "" {
			summary.CountsByRepository[event.Repository]++
		}
		if event.Model != "" {
			summary.CountsByModel[event.Model]++
		}
		if event.MCP != nil && event.MCP.Server != "" {
			summary.CountsByMCPServer[event.MCP.Server]++
		}
		if event.Approval != nil && event.Approval.Decision != "" {
			summary.CountsByApprovalDecision[event.Approval.Decision]++
		}
		promptEvent := event.Event.Category == "prompt"
		commandEvent := event.Event.Category == "command" || event.Command != nil
		fileEvent := event.Event.Category == "file" || event.File != nil
		mcpEvent := event.Event.Category == "mcp" || event.MCP != nil
		approvalEvent := event.Event.Category == "approval" || event.Approval != nil
		switch event.Event.Category {
		case "prompt":
			summary.PromptEvents++
		case "tool":
			summary.ToolEvents++
		}
		if !promptEvent && event.Event.Action == "prompt.submitted" {
			summary.PromptEvents++
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
		switch event.Severity {
		case "high":
			summary.HighSeverityEvents++
		case "critical":
			summary.CriticalSeverityEvents++
		}
		if event.Event.Action == "tool.failed" {
			summary.FailedToolEvents++
		}
		if event.Event.Action == "policy.blocked" {
			summary.PolicyBlockedEvents++
		}
		if event.Event.Action == "approval.denied" || (event.Approval != nil && event.Approval.Decision == "denied") {
			summary.DeniedApprovalEvents++
		}
		if isNeedsReview(record) {
			summary.NeedsReviewEvents++
		}
	}
	summary.ActiveSessions = len(sessions)
	summary.TopActions = topCounts(summary.CountsByAction, 5)
	summary.TopHarnesses = topCounts(summary.CountsByHarness, 5)
	summary.TopModels = topCounts(summary.CountsByModel, 5)
	summary.TopRepositories = topCounts(summary.CountsByRepository, 5)
	summary.TopMCPServers = topCounts(summary.CountsByMCPServer, 5)
	return summary
}

func isNeedsReview(record EventRecord) bool {
	event := record.Event
	if event.Severity == "high" || event.Severity == "critical" {
		return true
	}
	switch event.Event.Action {
	case "approval.denied", "policy.blocked", "tool.failed", "endpoint.tamper_detected", "endpoint.health_failed":
		return true
	}
	if event.Approval != nil && event.Approval.Decision == "denied" {
		return true
	}
	if record.WazuhLevel >= 9 {
		return true
	}
	return false
}

func topCounts(counts map[string]int, limit int) []Count {
	values := make([]Count, 0, len(counts))
	for name, count := range counts {
		values = append(values, Count{Name: name, Count: count})
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Count == values[j].Count {
			return values[i].Name < values[j].Name
		}
		return values[i].Count > values[j].Count
	})
	if len(values) > limit {
		values = values[:limit]
	}
	return values
}
