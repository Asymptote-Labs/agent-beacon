package cmd

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	endpointinventory "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/inventory"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

func TestInventoryHeartbeatReportsEditionOnlyWhenNotDefault(t *testing.T) {
	for _, tc := range []struct {
		edition string
		want    interface{}
	}{
		{version.DefaultEdition, nil},
		{"partner", "partner"},
	} {
		t.Run(tc.edition, func(t *testing.T) {
			orig := version.Edition
			version.Edition = tc.edition
			t.Cleanup(func() { version.Edition = orig })
			testenv.SetHome(t, t.TempDir())
			logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
			cfg := endpointconfig.Default(true, logPath)
			settings := endpointconfig.InventoryConfig(cfg)

			if _, err := writeInventoryHeartbeat(cfg, settings, false, inventoryTriggerScheduled, heartbeatScope{}); err != nil {
				t.Fatal(err)
			}
			got := heartbeatInventoryField(t, endpointinventory.LogPath(logPath, true), "edition")
			if got != tc.want {
				t.Fatalf("inventory edition = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func heartbeatInventoryField(t *testing.T, path, field string) interface{} {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for scanner.Scan() {
		var event struct {
			Event struct {
				Action string `json:"action"`
			} `json:"event"`
			Raw struct {
				Inventory map[string]interface{} `json:"inventory"`
			} `json:"raw"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		if event.Event.Action == "inventory.heartbeat" {
			return event.Raw.Inventory[field]
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("no inventory.heartbeat event written")
	return nil
}
