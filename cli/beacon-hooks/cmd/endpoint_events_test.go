package cmd

import (
	"context"
	"os"
	"os/exec"
	"reflect"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
)

func TestMaybeEmitInventoryHeartbeatInvokesBeaconCLI(t *testing.T) {
	oldPlatform := platformFlag
	oldRunner := runInventoryHeartbeatCommand
	defer func() {
		platformFlag = oldPlatform
		runInventoryHeartbeatCommand = oldRunner
	}()
	platformFlag = "cursor"
	t.Setenv("BEACON_ENDPOINT_CLI", "/usr/local/bin/beacon")
	t.Setenv("BEACON_ENDPOINT_CONFIG", "/tmp/beacon/config.json")
	t.Setenv("BEACON_ENDPOINT_LOG", "/tmp/beacon/runtime.jsonl")

	var gotName string
	var gotArgs []string
	runInventoryHeartbeatCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = append([]string(nil), args...)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestInventoryHeartbeatCommandHelperProcess")
		cmd.Env = append(os.Environ(), "GO_WANT_INVENTORY_HELPER_PROCESS=1")
		return cmd
	}

	logger := logging.NewLoggerForPlatform("test", "cursor")
	maybeEmitInventoryHeartbeat(logger, map[string]interface{}{"cwd": "/repo"})

	if gotName != "/usr/local/bin/beacon" {
		t.Fatalf("command name = %q, want beacon path", gotName)
	}
	wantArgs := []string{
		"endpoint", "inventory", "heartbeat",
		"--trigger", "hook",
		"--trigger-harness", "cursor",
		"--config", "/tmp/beacon/config.json",
		"--log-path", "/tmp/beacon/runtime.jsonl",
		"--working-dir", "/repo",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestMaybeEmitInventoryHeartbeatSkipsUnsupportedPlatform(t *testing.T) {
	oldPlatform := platformFlag
	oldRunner := runInventoryHeartbeatCommand
	defer func() {
		platformFlag = oldPlatform
		runInventoryHeartbeatCommand = oldRunner
	}()
	platformFlag = "codex"
	t.Setenv("BEACON_ENDPOINT_CLI", "/usr/local/bin/beacon")
	called := false
	runInventoryHeartbeatCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		called = true
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestInventoryHeartbeatCommandHelperProcess")
		cmd.Env = append(os.Environ(), "GO_WANT_INVENTORY_HELPER_PROCESS=1")
		return cmd
	}

	logger := logging.NewLoggerForPlatform("test", "codex")
	maybeEmitInventoryHeartbeat(logger, map[string]interface{}{"cwd": "/repo"})

	if called {
		t.Fatal("inventory heartbeat command should not run for unsupported platform")
	}
}

func TestInventoryHeartbeatCommandHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_INVENTORY_HELPER_PROCESS") != "1" {
		return
	}
	os.Exit(0)
}
