package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/devincloud"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/spf13/cobra"
)

var cloudDevinCmd = &cobra.Command{
	Use:   "devin",
	Short: "Devin Cloud agent telemetry",
}

var devinPullOpts struct {
	org        string
	baseURL    string
	state      string
	logPath    string
	print      bool
	watch      bool
	fullResync bool
	interval   time.Duration
	noUpload   bool
	userMode   bool
	systemMode bool
}

var cloudDevinPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Pull all org Devin Cloud sessions and forward them as Beacon telemetry",
	Long: `Pull Devin Cloud (autonomous agent) telemetry org-wide via the Devin v3 API
and convert it into Beacon endpoint events.

Authenticates with an organization service key (cog_ prefix) so a single
centralized run captures every user's cloud sessions — no per-user or
per-sandbox setup. Set DEVIN_API_KEY (the service key) and DEVIN_ORG_ID
(or --org). When BEACON_CLOUD_GCS_* are set, each session's snapshot is also
uploaded to GCS, matching the layout used by the in-sandbox hook providers.

This command reaches the Devin API over the network; it is explicit and opt-in.`,
	SilenceUsage: true,
	RunE:         runCloudDevinPull,
}

func init() {
	cloudCmd.AddCommand(cloudDevinCmd)
	cloudDevinCmd.AddCommand(cloudDevinPullCmd)

	f := cloudDevinPullCmd.Flags()
	f.StringVar(&devinPullOpts.org, "org", "", "Devin organization id (or DEVIN_ORG_ID)")
	f.StringVar(&devinPullOpts.baseURL, "base-url", devincloud.DefaultBaseURL, "Devin API base URL")
	f.StringVar(&devinPullOpts.state, "state", "", "Dedup state file path (default ~/.beacon/cloud/devin/<org>/state.json)")
	f.StringVar(&devinPullOpts.logPath, "log-path", "", "Runtime JSONL log path (default resolved endpoint log)")
	f.BoolVar(&devinPullOpts.print, "print", false, "Print mapped events as JSON without writing or uploading (dry run)")
	f.BoolVar(&devinPullOpts.watch, "watch", false, "Poll continuously on --interval (default: a single sweep then exit)")
	f.BoolVar(&devinPullOpts.fullResync, "full-resync", false, "Re-fetch every session's messages, ignoring the unchanged-session skip (dedup still applies)")
	f.DurationVar(&devinPullOpts.interval, "interval", time.Minute, "Poll interval for --watch")
	f.BoolVar(&devinPullOpts.noUpload, "no-upload", false, "Never upload to GCS even when BEACON_CLOUD_GCS_* is set")
	f.BoolVar(&devinPullOpts.userMode, "user", true, "Use per-user endpoint log paths")
	f.BoolVar(&devinPullOpts.systemMode, "system", false, "Use system endpoint log paths")
}

func runCloudDevinPull(cmd *cobra.Command, args []string) error {
	apiKey := strings.TrimSpace(os.Getenv("DEVIN_API_KEY"))
	if apiKey == "" {
		return fmt.Errorf("DEVIN_API_KEY is required (organization service key, cog_ prefix)")
	}
	org := strings.TrimSpace(devinPullOpts.org)
	if org == "" {
		org = strings.TrimSpace(os.Getenv("DEVIN_ORG_ID"))
	}
	if org == "" {
		return fmt.Errorf("--org or DEVIN_ORG_ID is required")
	}

	client := devincloud.New(org, apiKey, devincloud.WithBaseURL(devinPullOpts.baseURL))

	userMode := endpointDevinUserMode()
	opts := devincloud.PullOptions{
		Print:        devinPullOpts.print,
		Out:          cmd.OutOrStdout(),
		Write:        !devinPullOpts.print,
		UserMode:     userMode,
		UploadPrefix: devincloud.GCSPrefixFromEnv(),
		ForceRefresh: devinPullOpts.fullResync,
	}
	// --print is a dry run: do not read or write dedup state (so every event is
	// shown on each run) and do not resolve a log path to write to.
	if !devinPullOpts.print {
		opts.StatePath = resolveDevinStatePath(devinPullOpts.state, org)
		opts.LogPath = lifecycle.ResolveRuntimeLog(userMode, devinPullOpts.logPath).EffectiveLogPath
	}

	// Upload is optional: enabled when GCS env is configured and not in dry-run / --no-upload.
	if !devinPullOpts.print && !devinPullOpts.noUpload {
		uploader, ok, err := devincloud.NewGCSUploaderFromEnv()
		if err != nil {
			return err
		}
		if ok {
			opts.Upload = uploader
		}
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	if devinPullOpts.watch && !devinPullOpts.print {
		return watchDevin(ctx, cmd, client, opts)
	}

	sum, err := devincloud.PullOnce(ctx, client, opts)
	if err != nil {
		return err
	}
	reportDevinSweep(cmd, sum)
	return nil
}

func watchDevin(ctx context.Context, cmd *cobra.Command, client *devincloud.Client, opts devincloud.PullOptions) error {
	interval := devinPullOpts.interval
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	for {
		sum, err := devincloud.PullOnce(ctx, client, opts)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "devin pull error: %v\n", err)
		} else {
			reportDevinSweep(cmd, sum)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func reportDevinSweep(cmd *cobra.Command, sum devincloud.Summary) {
	if devinPullOpts.print {
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "devin pull: %d sessions, %d changed, %d events, %d uploaded, %d errors\n",
		sum.Sessions, sum.SessionsChanged, sum.EventsEmitted, sum.Uploaded, sum.Errors)
}

func endpointDevinUserMode() bool {
	if devinPullOpts.systemMode {
		return false
	}
	return devinPullOpts.userMode
}

// resolveDevinStatePath always returns a non-empty path so dedup state is
// persisted across runs. It prefers ~/.beacon but falls back to the OS temp dir
// when $HOME is unavailable (e.g. some cron environments) — without persistence
// every scheduled run would re-append the full event set.
func resolveDevinStatePath(override, org string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	base := filepath.Join(os.TempDir(), "beacon")
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		base = filepath.Join(home, ".beacon")
	}
	return filepath.Join(base, "cloud", "devin", org, "state.json")
}
