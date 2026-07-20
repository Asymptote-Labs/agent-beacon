package cmd

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/cursorusage"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/tokens"
)

func TestTokenUsageSyncCursorCommandRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"token-usage", "sync-cursor"})
	if err != nil {
		t.Fatalf("Find token-usage sync-cursor returned error: %v", err)
	}
	if cmd == nil || cmd.Use != "sync-cursor" {
		t.Fatalf("token-usage sync-cursor command not registered: %#v", cmd)
	}
	for _, flag := range []string{"db", "state", "log-path", "print", "since", "rebuild-state", "user", "system"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Fatalf("token-usage sync-cursor command missing --%s flag", flag)
		}
	}
}

func TestCursorSyncFeedsTokenUsageReport(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.vscdb")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE cursorDiskKV (key TEXT PRIMARY KEY, value BLOB)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	rows := map[string]string{
		"bubbleId:conv-a:bubble-1": `{"createdAt": 1751500000000, "modelInfo": {"modelName": "claude-4.5-sonnet"}, "tokenCount": {"inputTokens": 100, "outputTokens": 40, "cacheReadTokens": 900}}`,
		"bubbleId:conv-a:bubble-2": `{"createdAt": 1751500060000, "modelInfo": {"modelName": "claude-4.5-sonnet"}, "tokenCount": {"inputTokens": 50, "outputTokens": 10}}`,
		"bubbleId:conv-b:bubble-3": `{"createdAt": 1751500120000, "model": "gpt-5.2", "usage": {"input_tokens": 30, "output_tokens": 5}}`,
	}
	for key, value := range rows {
		if _, err := db.Exec(`INSERT INTO cursorDiskKV (key, value) VALUES (?, ?)`, key, []byte(value)); err != nil {
			t.Fatalf("insert %s: %v", key, err)
		}
	}
	db.Close()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	sum, err := cursorusage.SyncOnce(cursorusage.Options{
		DBPath:    dbPath,
		StatePath: filepath.Join(dir, "state.json"),
		LogPath:   logPath,
		UserMode:  true,
	})
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if sum.Emitted != 3 {
		t.Fatalf("summary = %+v, want 3 emitted", sum)
	}

	output := runTokenUsageCommand(t, "--log-path", logPath, "--json")
	var report tokens.Report
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("unmarshal JSON report: %v\n%s", err, output)
	}
	if report.Totals.InputTokens != 180 || report.Totals.OutputTokens != 55 || report.Totals.CacheReadInputTokens != 900 {
		t.Fatalf("totals = %#v", report.Totals)
	}
	if len(report.ByHarness) != 1 || report.ByHarness[0].Key != "cursor" {
		t.Fatalf("by_harness = %#v, want cursor only", report.ByHarness)
	}
	if len(report.ByModel) != 2 {
		t.Fatalf("by_model = %#v, want claude + gpt entries", report.ByModel)
	}
	if len(report.BySession) != 2 {
		t.Fatalf("by_session = %#v, want conv-a and conv-b", report.BySession)
	}
}
