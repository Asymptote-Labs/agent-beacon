package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/integrations/claudecompliance"
)

var integrationsOpts struct {
	userMode        bool
	systemMode      bool
	logPath         string
	jsonOutput      bool
	apiKeyEnv       string
	limit           int
	maxPages        int
	since           string
	overlap         string
	activityTypes   []string
	organizationIDs []string
	actorIDs        []string
	resetCursor     bool
	dryRun          bool
}

var integrationsCmd = &cobra.Command{
	Use:   "integrations",
	Short: "Manage local Beacon integrations for external telemetry sources",
}

var integrationsClaudeComplianceCmd = &cobra.Command{
	Use:   "claude-compliance",
	Short: "Pull Claude Compliance API activity into local Beacon JSONL",
}

var integrationsClaudeCompliancePullCmd = &cobra.Command{
	Use:          "pull",
	Short:        "Pull Claude Compliance Activity Feed events",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runClaudeCompliancePull(cmd.Context())
	},
}

var integrationsClaudeComplianceStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show Claude Compliance integration state",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runClaudeComplianceStatus()
	},
}

var integrationsClaudeComplianceValidateCmd = &cobra.Command{
	Use:          "validate",
	Short:        "Validate Claude Compliance API access",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runClaudeComplianceValidate(cmd.Context())
	},
}

func init() {
	rootCmd.AddCommand(integrationsCmd)
	integrationsCmd.AddCommand(integrationsClaudeComplianceCmd)
	integrationsClaudeComplianceCmd.AddCommand(integrationsClaudeCompliancePullCmd)
	integrationsClaudeComplianceCmd.AddCommand(integrationsClaudeComplianceStatusCmd)
	integrationsClaudeComplianceCmd.AddCommand(integrationsClaudeComplianceValidateCmd)

	for _, c := range []*cobra.Command{
		integrationsClaudeCompliancePullCmd,
		integrationsClaudeComplianceStatusCmd,
		integrationsClaudeComplianceValidateCmd,
	} {
		c.Flags().BoolVar(&integrationsOpts.userMode, "user", true, "Use per-user Beacon paths")
		c.Flags().BoolVar(&integrationsOpts.systemMode, "system", false, "Use system Beacon paths")
		c.Flags().StringVar(&integrationsOpts.logPath, "log-path", "", "Local Beacon JSONL log path")
		c.Flags().StringVar(&integrationsOpts.apiKeyEnv, "api-key-env", claudecompliance.DefaultAPIKeyEnv, "Environment variable containing the Claude Compliance API key")
	}

	integrationsClaudeCompliancePullCmd.Flags().IntVar(&integrationsOpts.limit, "limit", claudecompliance.DefaultLimit, "Maximum activities per API request")
	integrationsClaudeCompliancePullCmd.Flags().IntVar(&integrationsOpts.maxPages, "max-pages", claudecompliance.DefaultMaxPages, "Maximum API pages to fetch per pull")
	integrationsClaudeCompliancePullCmd.Flags().StringVar(&integrationsOpts.since, "since", "", "Initial or manual lookback window, such as 24h, or an RFC3339 timestamp")
	integrationsClaudeCompliancePullCmd.Flags().StringVar(&integrationsOpts.overlap, "overlap", claudecompliance.DefaultOverlap.String(), "Overlap window for incremental sync, such as 10m; set 0 to disable")
	integrationsClaudeCompliancePullCmd.Flags().StringArrayVar(&integrationsOpts.activityTypes, "activity-type", nil, "Activity type to include; repeat for multiple values")
	integrationsClaudeCompliancePullCmd.Flags().StringArrayVar(&integrationsOpts.organizationIDs, "organization-id", nil, "Organization ID or UUID to include; repeat for multiple values")
	integrationsClaudeCompliancePullCmd.Flags().StringArrayVar(&integrationsOpts.actorIDs, "actor-id", nil, "Actor user ID to include; repeat for multiple values")
	integrationsClaudeCompliancePullCmd.Flags().BoolVar(&integrationsOpts.resetCursor, "reset-cursor", false, "Ignore saved cursor state for this pull")
	integrationsClaudeCompliancePullCmd.Flags().BoolVar(&integrationsOpts.dryRun, "dry-run", false, "Fetch and normalize without writing JSONL or state")
	integrationsClaudeCompliancePullCmd.Flags().BoolVar(&integrationsOpts.jsonOutput, "json", false, "Print pull summary as JSON")

	integrationsClaudeComplianceStatusCmd.Flags().BoolVar(&integrationsOpts.jsonOutput, "json", false, "Print status as JSON")
	integrationsClaudeComplianceValidateCmd.Flags().BoolVar(&integrationsOpts.jsonOutput, "json", false, "Print validation result as JSON")
}

func runClaudeCompliancePull(ctx context.Context) error {
	userMode := integrationsUserMode()
	now := time.Now().UTC()
	query, err := claudeComplianceQuery(now)
	if err != nil {
		return err
	}
	overlap, err := parseDurationFlag("overlap", integrationsOpts.overlap)
	if err != nil {
		return err
	}
	logPath := integrationsLogPath(userMode)
	client, err := claudeComplianceClientFromEnv()
	if err != nil {
		return err
	}
	summary, err := claudecompliance.PullActivities(ctx, claudecompliance.SyncOptions{
		Client:      client,
		StatePath:   claudecompliance.DefaultStatePath(userMode),
		Query:       query,
		MaxPages:    integrationsOpts.maxPages,
		Overlap:     overlap,
		ResetCursor: integrationsOpts.resetCursor,
		DryRun:      integrationsOpts.dryRun,
		Now:         func() time.Time { return now },
		WriteEvent: func(event schema.Event) error {
			_, err := writer.AppendEvent(event, writer.Options{Path: logPath, UserMode: userMode})
			return err
		},
	})
	if err != nil {
		if err == claudecompliance.ErrSinceRequired {
			return fmt.Errorf("%w; rerun with --since, for example --since 24h", err)
		}
		return err
	}
	if integrationsOpts.jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(summary)
	}
	verb := "wrote"
	if integrationsOpts.dryRun {
		verb = "would write"
	}
	fmt.Printf("Claude Compliance pull fetched %d activities, %s %d, skipped %d across %d page(s).\n", summary.Fetched, verb, summary.Written, summary.Skipped, summary.Pages)
	fmt.Printf("State: %s\n", summary.StatePath)
	fmt.Printf("Log: %s\n", logPath)
	return nil
}

func runClaudeComplianceStatus() error {
	userMode := integrationsUserMode()
	statePath := claudecompliance.DefaultStatePath(userMode)
	state, err := claudecompliance.LoadState(statePath)
	if err != nil {
		return err
	}
	logPath := integrationsLogPath(userMode)
	lastObserved, observed := claudecompliance.LastComplianceEvent(logPath)
	status := struct {
		Name                string `json:"name"`
		DisplayName         string `json:"display_name"`
		StatePath           string `json:"state_path"`
		LogPath             string `json:"log_path"`
		LastID              string `json:"last_id,omitempty"`
		LastSyncedAt        string `json:"last_synced_at,omitempty"`
		RecentIDs           int    `json:"recent_ids"`
		LastEventObserved   bool   `json:"last_event_observed"`
		LastEventObservedAt string `json:"last_event_observed_at,omitempty"`
		Message             string `json:"message"`
	}{
		Name:              claudecompliance.Name,
		DisplayName:       claudecompliance.DisplayName,
		StatePath:         statePath,
		LogPath:           logPath,
		LastID:            state.LastID,
		LastSyncedAt:      state.LastSyncedAt,
		RecentIDs:         len(state.RecentIDs),
		LastEventObserved: observed,
		Message:           "Run `beacon integrations claude-compliance pull --since 24h` to import recent activity",
	}
	if observed {
		status.LastEventObservedAt = lastObserved.UTC().Format(time.RFC3339)
		status.Message = "Claude Compliance events have been observed in local Beacon JSONL"
	}
	if integrationsOpts.jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(status)
	}
	fmt.Printf("%s: observed=%t recent_ids=%d", status.DisplayName, status.LastEventObserved, status.RecentIDs)
	if status.LastSyncedAt != "" {
		fmt.Printf(" synced=%s", status.LastSyncedAt)
	}
	if status.LastEventObservedAt != "" {
		fmt.Printf(" last=%s", status.LastEventObservedAt)
	}
	fmt.Println()
	fmt.Println(status.Message)
	return nil
}

func runClaudeComplianceValidate(ctx context.Context) error {
	client, err := claudeComplianceClientFromEnv()
	if err != nil {
		return err
	}
	resp, err := client.ListActivities(ctx, claudecompliance.Query{Limit: 1, Order: "desc"})
	if err != nil {
		return err
	}
	result := struct {
		OK      bool `json:"ok"`
		Records int  `json:"records"`
	}{
		OK:      true,
		Records: len(resp.Data),
	}
	if integrationsOpts.jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	fmt.Printf("Claude Compliance API access validated (%d record(s) returned).\n", result.Records)
	return nil
}

func integrationsUserMode() bool {
	return integrationsOpts.userMode && !integrationsOpts.systemMode
}

func integrationsLogPath(userMode bool) string {
	if strings.TrimSpace(integrationsOpts.logPath) != "" {
		return integrationsOpts.logPath
	}
	return writer.DefaultPath(userMode)
}

func claudeComplianceClientFromEnv() (*claudecompliance.Client, error) {
	envName := strings.TrimSpace(integrationsOpts.apiKeyEnv)
	if envName == "" {
		envName = claudecompliance.DefaultAPIKeyEnv
	}
	apiKey := strings.TrimSpace(os.Getenv(envName))
	if apiKey == "" {
		return nil, fmt.Errorf("%s is not set", envName)
	}
	return &claudecompliance.Client{APIKey: apiKey}, nil
}

func claudeComplianceQuery(now time.Time) (claudecompliance.Query, error) {
	query := claudecompliance.Query{
		Limit:           integrationsOpts.limit,
		Order:           "asc",
		ActivityTypes:   cleanStringSlice(integrationsOpts.activityTypes),
		OrganizationIDs: cleanStringSlice(integrationsOpts.organizationIDs),
		ActorIDs:        cleanStringSlice(integrationsOpts.actorIDs),
	}
	if strings.TrimSpace(integrationsOpts.since) != "" {
		since, err := parseSinceFlag(integrationsOpts.since, now)
		if err != nil {
			return claudecompliance.Query{}, err
		}
		query.CreatedAtGTE = since.UTC().Format(time.RFC3339)
	}
	return query, nil
}

func parseSinceFlag(value string, now time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	if duration, err := time.ParseDuration(value); err == nil {
		return now.Add(-duration), nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("--since must be a duration such as 24h or an RFC3339 timestamp: %w", err)
	}
	return parsed, nil
}

func parseDurationFlag(name, value string) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("--%s must be a duration such as 10m: %w", name, err)
	}
	return duration, nil
}

func cleanStringSlice(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
