package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeedDevinCloudRunIDFromDevinSessionEnv(t *testing.T) {
	platformFlag = "devin"
	t.Setenv("BEACON_ORIGIN", "cloud")
	t.Setenv("BEACON_RUN_PROVIDER", "devin_cloud")
	t.Setenv("BEACON_RUN_ID", "")
	t.Setenv("DEVIN_SESSION_ID", "devin-session-42")

	seedDevinCloudRunID()

	if got := os.Getenv("BEACON_RUN_ID"); got != "devin-session-42" {
		t.Fatalf("BEACON_RUN_ID = %q, want devin-session-42", got)
	}
}

func TestSeedDevinCloudRunIDKeepsExplicitRunID(t *testing.T) {
	platformFlag = "devin-cli"
	t.Setenv("BEACON_ORIGIN", "cloud")
	t.Setenv("BEACON_RUN_PROVIDER", "devin_cloud")
	t.Setenv("BEACON_RUN_ID", "explicit-run")
	t.Setenv("DEVIN_SESSION_ID", "devin-session-42")

	seedDevinCloudRunID()

	if got := os.Getenv("BEACON_RUN_ID"); got != "explicit-run" {
		t.Fatalf("BEACON_RUN_ID = %q, want explicit-run preserved", got)
	}
}

func TestSeedDevinCloudRunIDNoopOutsideDevinCloud(t *testing.T) {
	platformFlag = "claude"
	t.Setenv("BEACON_ORIGIN", "cloud")
	t.Setenv("BEACON_RUN_PROVIDER", "devin_cloud")
	t.Setenv("BEACON_RUN_ID", "")
	t.Setenv("DEVIN_SESSION_ID", "devin-session-42")

	seedDevinCloudRunID()

	if got := os.Getenv("BEACON_RUN_ID"); got != "" {
		t.Fatalf("BEACON_RUN_ID = %q, want empty for non-Devin platform", got)
	}
}

func TestSeedDevinCloudRunIDGeneratesAndPersistsWhenNoDevinEnv(t *testing.T) {
	dir := t.TempDir()
	platformFlag = "devin"
	t.Setenv("BEACON_ORIGIN", "cloud")
	t.Setenv("BEACON_RUN_PROVIDER", "devin_cloud")
	t.Setenv("BEACON_ENDPOINT_LOG", filepath.Join(dir, "runtime.jsonl"))
	t.Setenv("BEACON_RUN_ID", "")
	// No DEVIN_* identifiers present.
	for _, key := range []string{"DEVIN_SESSION_ID", "DEVIN_RUN_ID", "DEVIN_SESSION", "ACI_RUN_ID"} {
		t.Setenv(key, "")
	}

	seedDevinCloudRunID()

	// Must be non-empty (cloudshuttle.Upload skips when RunID == "") and unique.
	first := os.Getenv("BEACON_RUN_ID")
	if !strings.HasPrefix(first, "devin-") || len(first) <= len("devin-") {
		t.Fatalf("BEACON_RUN_ID = %q, want a generated devin- id", first)
	}

	// A second hook process in the same session (run id cleared from env) must
	// reuse the persisted id rather than mint a new one.
	t.Setenv("BEACON_RUN_ID", "")
	seedDevinCloudRunID()
	if got := os.Getenv("BEACON_RUN_ID"); got != first {
		t.Fatalf("second seed = %q, want persisted %q", got, first)
	}
}
