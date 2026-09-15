package cmd

import (
	"io"
	"os"
	"testing"
)

// Old Codex hooks call this subcommand on every prompt. It must answer and do nothing else: no
// subprocess, no file, however the environment is set.
func TestInventoryHeartbeatSubcommandIsANoOp(t *testing.T) {
	t.Setenv("BEACON_ENDPOINT_CLI", "/definitely/not/a/binary")
	t.Setenv("BEACON_ENDPOINT_LOG", t.TempDir()+"/runtime.jsonl")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })
	_, _ = io.WriteString(w, `{"session_id":"s1","cwd":"/repo"}`)
	_ = w.Close()

	runInventoryHeartbeat(inventoryHeartbeatCmd, nil)

	if entries, _ := os.ReadDir(t.TempDir()); len(entries) != 0 {
		t.Fatalf("no-op must not write files: %v", entries)
	}
	if !inventoryHeartbeatCmd.Hidden {
		t.Fatal("the retired subcommand should be hidden from help")
	}
}
