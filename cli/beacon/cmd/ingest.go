package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	beaconauth "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/auth"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/ingest"
	endpointingest "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/ingest/endpoint"
	"github.com/spf13/cobra"
)

var ingestCmd = &cobra.Command{
	Use:   "ingest",
	Short: "Upload Beacon telemetry to configured ingest destinations",
}

var ingestStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show configured ingest status",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		state := endpointIngestStatus()
		if endpointOpts.jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(map[string]ingest.State{
				"endpoint": state,
			})
		}
		fmt.Println("Beacon ingest")
		printIngestStatus("Endpoint", state)
		return nil
	},
}

var ingestEndpointCmd = &cobra.Command{
	Use:   "endpoint",
	Short: "Manage endpoint telemetry ingest",
}

var ingestEndpointStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show endpoint telemetry upload status",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		state := endpointIngestStatus()
		if endpointOpts.jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(state)
		}
		printIngestStatus("Endpoint ingest", state)
		return nil
	},
}

var ingestEndpointUploadCmd = &cobra.Command{
	Use:          "upload",
	Short:        "Upload endpoint telemetry to managed ingest",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		res, err := uploadEndpointIngest(cmd.Context())
		if endpointOpts.jsonOutput {
			if encodeErr := json.NewEncoder(os.Stdout).Encode(res.State); encodeErr != nil {
				return encodeErr
			}
		} else {
			printIngestStatus("Endpoint ingest", res.State)
		}
		if err != nil {
			return err
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(ingestCmd)
	ingestCmd.AddCommand(ingestStatusCmd)
	ingestCmd.AddCommand(ingestEndpointCmd)
	ingestEndpointCmd.AddCommand(ingestEndpointStatusCmd)
	ingestEndpointCmd.AddCommand(ingestEndpointUploadCmd)

	for _, c := range []*cobra.Command{ingestStatusCmd, ingestEndpointStatusCmd, ingestEndpointUploadCmd} {
		c.Flags().BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		c.Flags().BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths and the system collector service")
		c.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
		c.Flags().BoolVar(&endpointOpts.jsonOutput, "json", false, "Print output as JSON")
	}
}

func endpointIngestStatus() ingest.State {
	cfg, effectiveUserMode, logPath := endpointIngestConfig()
	creds, _ := beaconauth.LoadCredentials()
	return ingest.Status(
		endpointingest.Settings(cfg, logPath, effectiveUserMode),
		endpointingest.Store(effectiveUserMode),
		creds,
	)
}

func uploadEndpointIngest(ctx context.Context) (ingest.Result, error) {
	cfg, effectiveUserMode, logPath := endpointIngestConfig()
	creds, _ := beaconauth.LoadCredentials()
	uploadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res := ingest.Upload(uploadCtx, ingest.Options{
		Settings: endpointingest.Settings(cfg, logPath, effectiveUserMode),
		Creds:    creds,
		Store:    endpointingest.Store(effectiveUserMode),
		Source:   endpointingest.NewSource(cfg, logPath, effectiveUserMode),
	})
	if res.State.Enabled && res.State.Managed && res.State.LastError != "" {
		return res, fmt.Errorf("%s", res.State.LastError)
	}
	return res, nil
}

func endpointIngestConfig() (endpointconfig.Config, bool, string) {
	cfg := loadOrDefaultConfig()
	runtimeLog := lifecycle.ResolveRuntimeLog(endpointUserMode(), endpointOpts.logPath)
	effectiveCfg := cfg
	effectiveCfg.UserMode = runtimeLog.EffectiveUserMode
	if runtimeLog.EffectiveUserMode != cfg.UserMode {
		effectiveCfg = loadConfigForMode(runtimeLog.EffectiveUserMode, runtimeLog.EffectiveLogPath)
	}
	effectiveCfg.LogPath = runtimeLog.EffectiveLogPath
	return effectiveCfg, effectiveCfg.UserMode, effectiveCfg.LogPath
}

func printIngestStatus(label string, state ingest.State) {
	fmt.Printf("%s: enabled=%t managed=%t logged_in=%t", label, state.Enabled, state.Managed, state.LoggedIn)
	if state.LastUploadAt != "" {
		fmt.Printf(" last_upload=%s", state.LastUploadAt)
	}
	if state.LastCursor.Offset > 0 {
		fmt.Printf(" cursor_offset=%d", state.LastCursor.Offset)
	}
	fmt.Printf(" accepted=%d rejected=%d", state.AcceptedCount, state.RejectedCount)
	if state.LastError != "" {
		fmt.Printf(" error=%s", state.LastError)
	}
	fmt.Println()
}
