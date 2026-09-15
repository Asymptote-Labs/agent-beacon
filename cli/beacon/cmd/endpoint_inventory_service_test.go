package cmd

import (
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
)

func TestInventoryInstallDaemonReconcilesTheRequestedMode(t *testing.T) {
	oldOpts := endpointOpts
	oldReconcile := reconcileInventoryJob
	t.Cleanup(func() {
		endpointOpts = oldOpts
		reconcileInventoryJob = oldReconcile
	})
	var got lifecycle.InventoryJobOptions
	reconcileInventoryJob = func(opts lifecycle.InventoryJobOptions) (lifecycle.InventoryJobResult, error) {
		got = opts
		return lifecycle.InventoryJobResult{Enabled: true, UnitPath: "/fake/plist", Loaded: true}, nil
	}
	endpointOpts.userMode, endpointOpts.systemMode = true, false
	endpointOpts.logPath = "/tmp/scratch/runtime.jsonl"
	if err := runInventoryInstallDaemon(nil, nil); err != nil {
		t.Fatal(err)
	}
	if !got.UserMode || !got.Load || got.LogPath != "/tmp/scratch/runtime.jsonl" || got.Program == "" {
		t.Fatalf("reconcile options = %+v", got)
	}
}

func TestInventoryReconcileMessages(t *testing.T) {
	cases := map[string]lifecycle.InventoryJobResult{
		"Scheduled inventory job not installed: no scheduler":                 {Skipped: "no scheduler"},
		"Inventory heartbeat is disabled in config; scheduled job removed.":   {Removed: true},
		"Scheduled inventory job installed (/Library/LaunchDaemons/x.plist).": {Enabled: true, UnitPath: "/Library/LaunchDaemons/x.plist", Loaded: true},
	}
	for want, result := range cases {
		if got := inventoryReconcileMessage(result); got != want {
			t.Fatalf("message for %+v = %q, want %q", result, got, want)
		}
	}
}

func TestInventoryHeartbeatStatusLine(t *testing.T) {
	disabled := inventoryHeartbeatStatusLine(lifecycle.InventoryHeartbeatStatus{Enabled: false})
	if disabled != "Inventory heartbeat: disabled in config" {
		t.Fatalf("disabled line = %q", disabled)
	}
	never := inventoryHeartbeatStatusLine(lifecycle.InventoryHeartbeatStatus{
		Enabled: true, Interval: "6h0m0s", Job: service.Status{Loaded: false, Message: "Could not find service"},
	})
	for _, want := range []string{"scheduled every 6h0m0s", "job loaded=false running=false", "(Could not find service)", "last emitted never"} {
		if !strings.Contains(never, want) {
			t.Fatalf("line %q missing %q", never, want)
		}
	}
	live := inventoryHeartbeatStatusLine(lifecycle.InventoryHeartbeatStatus{
		Enabled: true, Interval: "6h0m0s", Job: service.Status{Loaded: true, Running: true}, LastEmittedAt: "2026-09-15T20:00:00Z",
	})
	if !strings.Contains(live, "job loaded=true running=true; last emitted 2026-09-15T20:00:00Z") || strings.Contains(live, "(") {
		t.Fatalf("live line = %q", live)
	}
}
