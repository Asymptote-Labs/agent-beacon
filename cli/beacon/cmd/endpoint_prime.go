package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/primesession"
	"github.com/spf13/cobra"
)

var endpointPrimeCmd = &cobra.Command{
	Use:   "prime",
	Short: "Collect telemetry from Prime Agent session files",
	Long: `Collect endpoint telemetry from Prime Agent's local session store.

Prime Agent has a live Beacon extension for hook-time telemetry. This command is
the complementary poll path: it reads committed JSONL transcripts under
~/.prime/agent/sessions and converts records that were not already synced into
endpoint events. Every event is marked harness.collection_method=poll, because
Beacon is reading what Prime Agent wrote after the fact rather than observing the
agent as it works.`,
}

var endpointPrimeSyncCmd = &cobra.Command{
	Use:          "sync",
	Short:        "Read new Prime Agent session records into the runtime log",
	SilenceUsage: true,
	RunE:         runEndpointPrimeSync,
}

var endpointPrimeStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show Prime Agent sessions and how much Beacon has collected",
	SilenceUsage: true,
	RunE:         runEndpointPrimeStatus,
}

var endpointPrimeOpts struct {
	sessionsDir  string
	artifactsDir string
	statePath    string
	logPath      string
	print        bool
	watch        bool
	interval     time.Duration
}

func init() {
	endpointCmd.AddCommand(endpointPrimeCmd)
	endpointPrimeCmd.AddCommand(endpointPrimeSyncCmd)
	endpointPrimeCmd.AddCommand(endpointPrimeStatusCmd)

	for _, c := range []*cobra.Command{endpointPrimeSyncCmd, endpointPrimeStatusCmd} {
		f := c.Flags()
		f.StringVar(&endpointPrimeOpts.sessionsDir, "sessions-dir", "", "Prime Agent sessions directory (default ~/.prime/agent/sessions)")
		f.StringVar(&endpointPrimeOpts.artifactsDir, "artifacts-dir", "", "Prime Agent session artifacts directory (default ~/.prime/agent/session-artifacts)")
		f.StringVar(&endpointPrimeOpts.statePath, "state", "", "Collector cursor file (default ~/.beacon/endpoint/state/prime.json)")
		f.BoolVar(&endpointOpts.jsonOutput, "json", false, "Print the result as JSON")
		f.BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		f.BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths")
	}

	sync := endpointPrimeSyncCmd.Flags()
	sync.StringVar(&endpointPrimeOpts.logPath, "log-path", "", "Runtime JSONL log path (default resolved endpoint log)")
	sync.BoolVar(&endpointPrimeOpts.print, "print", false, "Print mapped events as JSON without writing them or advancing the cursor (dry run)")
	sync.BoolVar(&endpointPrimeOpts.watch, "watch", false, "Sweep continuously on --interval (default: one sweep then exit)")
	sync.DurationVar(&endpointPrimeOpts.interval, "interval", time.Minute, "Sweep interval for --watch")
}

func runEndpointPrimeSync(cmd *cobra.Command, args []string) error {
	userMode := endpointUserMode()
	opts := primesession.CollectOptions{
		SessionsDir:  endpointPrimeOpts.sessionsDir,
		ArtifactsDir: endpointPrimeOpts.artifactsDir,
		Print:        endpointPrimeOpts.print,
		Out:          cmd.OutOrStdout(),
		Write:        !endpointPrimeOpts.print,
		UserMode:     userMode,
	}
	if !endpointPrimeOpts.print {
		opts.StatePath = resolvePrimeStatePath(endpointPrimeOpts.statePath, userMode)
		opts.LogPath = lifecycle.ResolveRuntimeLog(userMode, endpointPrimeOpts.logPath).EffectiveLogPath
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if endpointPrimeOpts.watch && !endpointPrimeOpts.print {
		return watchPrime(ctx, cmd, opts)
	}
	summary, err := primesession.CollectOnce(opts)
	reportPrimeSweep(cmd, summary)
	return err
}

func watchPrime(ctx context.Context, cmd *cobra.Command, opts primesession.CollectOptions) error {
	interval := endpointPrimeOpts.interval
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	for {
		summary, err := primesession.CollectOnce(opts)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Prime Agent sync error: %v\n", err)
		}
		reportPrimeSweep(cmd, summary)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func reportPrimeSweep(cmd *cobra.Command, summary primesession.Summary) {
	if endpointPrimeOpts.print {
		return
	}
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Prime Agent sync: %d sessions, %d changed, %d events, %d errors\n",
		summary.Sessions, summary.SessionsChanged, summary.EventsEmitted, summary.Errors)
	if summary.MalformedLines > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  %d unreadable line(s) in Prime Agent session logs\n", summary.MalformedLines)
	}
}

type primeSessionStatus struct {
	SessionID     string `json:"session_id"`
	SourcePath    string `json:"source_path"`
	Kind          string `json:"kind"`
	Workspace     string `json:"workspace,omitempty"`
	CollectedLine int    `json:"collected_line"`
	Collected     bool   `json:"collected"`
	SizeBytes     int64  `json:"size_bytes"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

type primeStatusReport struct {
	SessionsDir  string               `json:"sessions_dir"`
	ArtifactsDir string               `json:"artifacts_dir"`
	Present      bool                 `json:"present"`
	StatePath    string               `json:"state_path,omitempty"`
	Sessions     []primeSessionStatus `json:"sessions"`
}

func runEndpointPrimeStatus(cmd *cobra.Command, args []string) error {
	store, err := primesession.NewStore(endpointPrimeOpts.sessionsDir, endpointPrimeOpts.artifactsDir)
	if err != nil {
		return err
	}
	statePath := resolvePrimeStatePath(endpointPrimeOpts.statePath, endpointUserMode())
	state, err := primesession.LoadState(statePath)
	if err != nil {
		return err
	}
	refs, err := store.List()
	if err != nil {
		return err
	}
	report := primeStatusReport{
		SessionsDir:  store.SessionsDir,
		ArtifactsDir: store.ArtifactsDir,
		Present:      store.Exists(),
		StatePath:    statePath,
		Sessions:     make([]primeSessionStatus, 0, len(refs)),
	}
	for _, ref := range refs {
		cursor := state.Files[primeStatusKey(ref.Path)]
		status := primeSessionStatus{
			SessionID:  ref.ID,
			SourcePath: ref.Path,
			Kind:       ref.Kind,
			Workspace:  stringFromMap(ref.Header, "cwd"),
			SizeBytes:  ref.SizeBytes,
		}
		if ref.ModTimeMS > 0 {
			status.UpdatedAt = time.UnixMilli(ref.ModTimeMS).UTC().Format(time.RFC3339)
		}
		if cursor != nil {
			status.CollectedLine = cursor.LastLine
			status.Collected = cursor.ModTimeMS == ref.ModTimeMS && cursor.SizeBytes == ref.SizeBytes
		}
		report.Sessions = append(report.Sessions, status)
	}

	if endpointOpts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
	}
	out := cmd.OutOrStdout()
	if !report.Present {
		fmt.Fprintf(out, "Prime Agent sessions: none (%s does not exist)\n", report.SessionsDir)
		return nil
	}
	fmt.Fprintf(out, "Prime Agent sessions: %d in %s\n", len(report.Sessions), report.SessionsDir)
	for _, session := range report.Sessions {
		state := "pending"
		if session.Collected {
			state = "collected"
		}
		fmt.Fprintf(out, "  %s  %s  line %d  %s\n",
			session.SessionID, state, session.CollectedLine, strings.TrimSpace(session.Workspace))
	}
	return nil
}

func resolvePrimeStatePath(override string, userMode bool) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	if !userMode {
		if dir := filepath.Dir(lifecycle.ResolveRuntimeLog(false, "").EffectiveLogPath); dir != "" && dir != "." {
			return filepath.Join(dir, "prime-state.json")
		}
	}
	return primesession.DefaultStatePath()
}

func primeStatusKey(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

func stringFromMap(values map[string]interface{}, key string) string {
	if values == nil {
		return ""
	}
	value, _ := values[key].(string)
	return value
}
