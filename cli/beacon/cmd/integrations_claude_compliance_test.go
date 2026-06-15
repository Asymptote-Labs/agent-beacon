package cmd

import (
	"testing"
	"time"
)

func TestClaudeComplianceCommandsRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"integrations", "claude-compliance", "pull"},
		{"integrations", "claude-compliance", "status"},
		{"integrations", "claude-compliance", "validate"},
	} {
		cmd, _, err := rootCmd.Find(path)
		if err != nil {
			t.Fatalf("Find %v returned error: %v", path, err)
		}
		if cmd == nil || cmd.Use != path[len(path)-1] {
			t.Fatalf("command %v not registered: %#v", path, cmd)
		}
	}
}

func TestClaudeCompliancePullFlagsRegistered(t *testing.T) {
	for _, name := range []string{
		"api-key-env",
		"limit",
		"max-pages",
		"since",
		"overlap",
		"activity-type",
		"organization-id",
		"actor-id",
		"reset-cursor",
		"dry-run",
		"user",
		"system",
		"log-path",
	} {
		if integrationsClaudeCompliancePullCmd.Flags().Lookup(name) == nil {
			t.Fatalf("pull command missing --%s flag", name)
		}
	}
}

func TestParseSinceFlagAcceptsDurationAndRFC3339(t *testing.T) {
	now := time.Date(2026, 4, 10, 8, 30, 0, 0, time.UTC)
	duration, err := parseSinceFlag("24h", now)
	if err != nil {
		t.Fatalf("parseSinceFlag duration returned error: %v", err)
	}
	if got, want := duration.UTC().Format(time.RFC3339), "2026-04-09T08:30:00Z"; got != want {
		t.Fatalf("duration since = %s, want %s", got, want)
	}
	absolute, err := parseSinceFlag("2026-04-01T00:00:00Z", now)
	if err != nil {
		t.Fatalf("parseSinceFlag RFC3339 returned error: %v", err)
	}
	if got, want := absolute.UTC().Format(time.RFC3339), "2026-04-01T00:00:00Z"; got != want {
		t.Fatalf("absolute since = %s, want %s", got, want)
	}
}
