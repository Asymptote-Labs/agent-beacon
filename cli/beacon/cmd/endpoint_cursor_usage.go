package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/cursorsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/cursorusage"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
	"github.com/spf13/cobra"
)

// defaultCursorAdminKeyEnv is the environment variable the Cursor Admin API key is read from.
//
// An environment variable and nothing else, on purpose. Beacon does not store third-party service
// credentials, and an admin key is a team-wide credential: it can read every member's usage. So it
// is never written to Beacon's config, state, or log, never accepted as a flag value (where it
// would land in shell history and the process table), and never printed. Whoever runs the sync
// supplies it for that run.
const defaultCursorAdminKeyEnv = "CURSOR_ADMIN_API_KEY"

// `beacon endpoint cursor usage` collects Cursor's token usage from the Cursor Admin API.
//
// It is the one Cursor path that reaches the network, and only when someone runs it. Cursor's hooks
// and local session records carry no token counts, so without it Cursor contributes nothing to
// `beacon token-usage`; with it, each model request Cursor reports becomes a token.usage event in
// the runtime log, beside everything else Beacon recorded.
var endpointCursorUsageCmd = &cobra.Command{
	Use:   "usage",
	Short: "Collect Cursor token usage from the Cursor Admin API",
	Long: `Collect Cursor token usage and charges from the Cursor Admin API.

Cursor's hooks and local session records carry no token counts. The Cursor Admin
API does: it reports every model request with its input, output, and cache tokens
and what Cursor charged for it. 'sync' reads those events and writes one
token.usage event per request to the runtime log, so 'beacon token-usage' and the
dashboard token view include Cursor.

This is the one Cursor command that uses the network. It calls
https://api.cursor.com only when you run it; hooks never do.

Set the key in your environment before running it. Create one in the Cursor
dashboard under Settings > Advanced > Admin API Keys (team admins only):

  export CURSOR_ADMIN_API_KEY=key_...
  beacon endpoint cursor usage sync

The key is read from the environment for that run only. Beacon never writes it to
its config, state, or logs, and never prints it.

By default the sync collects only the usage of the Cursor account signed in on
this machine, read from Cursor's local storage. Use --email or --user-id to choose
a member, or --team for the whole team.`,
}

var endpointCursorUsageSyncCmd = &cobra.Command{
	Use:          "sync",
	Short:        "Read new Cursor Admin API usage events into the runtime log",
	SilenceUsage: true,
	RunE:         runEndpointCursorUsageSync,
}

var endpointCursorUsageStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show the Cursor usage collector's progress and configuration",
	SilenceUsage: true,
	RunE:         runEndpointCursorUsageStatus,
}

var endpointCursorUsageOpts struct {
	apiKeyEnv string
	baseURL   string
	email     string
	userID    string
	team      bool
	since     string
	lookback  time.Duration
	pageSize  int
	statePath string
	logPath   string
	globalDB  string
	print     bool
}

// Seams for tests: the client the sync talks to and the clock it reads.
var (
	newCursorUsageFetcher = func(apiKey, baseURL string) (cursorusage.Fetcher, error) {
		base, err := cursorusage.ValidateBaseURL(baseURL)
		if err != nil {
			return nil, err
		}
		client := cursorusage.NewClient(apiKey)
		client.BaseURL = base
		client.UserAgent = "beacon/" + version.GetVersion()
		return client, nil
	}
	cursorUsageNow = time.Now
)

func init() {
	endpointCursorCmd.AddCommand(endpointCursorUsageCmd)
	endpointCursorUsageCmd.AddCommand(endpointCursorUsageSyncCmd)
	endpointCursorUsageCmd.AddCommand(endpointCursorUsageStatusCmd)

	for _, c := range []*cobra.Command{endpointCursorUsageSyncCmd, endpointCursorUsageStatusCmd} {
		f := c.Flags()
		f.StringVar(&endpointCursorUsageOpts.apiKeyEnv, "api-key-env", defaultCursorAdminKeyEnv, "Environment variable holding the Cursor Admin API key")
		f.StringVar(&endpointCursorUsageOpts.email, "email", "", "Collect usage for this team member (default: the Cursor account signed in on this machine)")
		f.StringVar(&endpointCursorUsageOpts.userID, "user-id", "", "Collect usage for this numeric Cursor user ID")
		f.BoolVar(&endpointCursorUsageOpts.team, "team", false, "Collect usage for every member of the team")
		f.StringVar(&endpointCursorUsageOpts.globalDB, "global-db", "", "Cursor global storage DB the signed-in account is read from (default OS-specific Cursor state.vscdb)")
		f.StringVar(&endpointCursorUsageOpts.statePath, "state", "", "Collector state file (default ~/.beacon/endpoint/state/cursor-usage.json)")
		f.BoolVar(&endpointOpts.jsonOutput, "json", false, "Print the result as JSON")
		f.BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
		f.BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths")
	}

	sync := endpointCursorUsageSyncCmd.Flags()
	sync.StringVar(&endpointCursorUsageOpts.since, "since", "", "Collect from this date (YYYY-MM-DD or RFC 3339); default: 7 days back on the first sync, then from the last sync")
	sync.DurationVar(&endpointCursorUsageOpts.lookback, "lookback", cursorusage.DefaultLookback, "How far behind the last sync each sync re-reads, to catch late-posted events")
	sync.IntVar(&endpointCursorUsageOpts.pageSize, "page-size", 100, "Events per Admin API request")
	sync.StringVar(&endpointCursorUsageOpts.baseURL, "base-url", cursorusage.DefaultBaseURL, "Cursor Admin API base URL (https; plain http only to a loopback address)")
	sync.StringVar(&endpointCursorUsageOpts.logPath, "log-path", "", "Runtime JSONL log path (default resolved endpoint log)")
	sync.BoolVar(&endpointCursorUsageOpts.print, "print", false, "Print mapped events as JSON without writing them or advancing the collector state (dry run)")
}

func runEndpointCursorUsageSync(cmd *cobra.Command, args []string) error {
	opts := endpointCursorUsageOpts
	keyEnv := strings.TrimSpace(opts.apiKeyEnv)
	if keyEnv == "" {
		return errors.New("--api-key-env must name an environment variable")
	}
	apiKey := strings.TrimSpace(os.Getenv(keyEnv))
	if apiKey == "" {
		return fmt.Errorf("no Cursor Admin API key: set %s in your environment (create a key in the Cursor dashboard under Settings > Advanced > Admin API Keys)", keyEnv)
	}

	query, err := resolveCursorUsageQuery(opts.email, opts.userID, opts.team, opts.globalDB)
	if err != nil {
		return err
	}
	query.PageSize = opts.pageSize

	since, err := parseCursorUsageSince(opts.since)
	if err != nil {
		return err
	}
	if opts.lookback <= 0 {
		return errors.New("--lookback must be positive")
	}

	fetcher, err := newCursorUsageFetcher(apiKey, opts.baseURL)
	if err != nil {
		return err
	}

	userMode := endpointUserMode()
	collect := cursorusage.CollectOptions{
		Fetcher:  fetcher,
		Query:    query,
		Since:    since,
		Lookback: opts.lookback,
		Now:      cursorUsageNow,
		Print:    opts.print,
		Out:      cmd.OutOrStdout(),
		Write:    !opts.print,
		UserMode: userMode,
	}
	// --print is a dry run in both directions, like the other sync commands: no log write and no
	// state advance, so running it twice shows the same events.
	if !opts.print {
		collect.StatePath = resolveCursorUsageStatePath(opts.statePath, userMode)
		collect.LogPath = lifecycle.ResolveRuntimeLog(userMode, opts.logPath).EffectiveLogPath
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	summary, err := cursorusage.CollectOnce(ctx, collect)
	if !opts.print {
		reportCursorUsageSync(cmd.OutOrStdout(), summary)
	}
	return err
}

func reportCursorUsageSync(out io.Writer, summary cursorusage.Summary) {
	if endpointOpts.jsonOutput {
		_ = json.NewEncoder(out).Encode(summary)
		return
	}
	fmt.Fprintf(out, "cursor usage sync (%s): %d fetched, %d written, %d already collected, %d without usage\n",
		summary.Scope, summary.Fetched, summary.Emitted, summary.Duplicates, summary.NoUsage)
	if summary.WindowStart != "" {
		fmt.Fprintf(out, "  window %s .. %s\n", summary.WindowStart, summary.WindowEnd)
	}
}

// resolveCursorUsageQuery picks whose usage to collect. Exactly one of the explicit choices may be
// given; with none, it is the account signed in to Cursor on this machine.
//
// The default matters because an admin key reads the whole team. A sync on one person's laptop
// that silently collected everyone's usage would put every colleague's spend in that laptop's log,
// so the whole team is something you ask for with --team, never what you get by omission.
func resolveCursorUsageQuery(email, userID string, team bool, globalDB string) (cursorusage.Query, error) {
	email = strings.TrimSpace(email)
	userID = strings.TrimSpace(userID)
	chosen := 0
	for _, set := range []bool{email != "", userID != "", team} {
		if set {
			chosen++
		}
	}
	if chosen > 1 {
		return cursorusage.Query{}, errors.New("choose one of --email, --user-id, or --team")
	}
	switch {
	case email != "":
		return cursorusage.Query{Email: email}, nil
	case userID != "":
		return cursorusage.Query{UserID: userID}, nil
	case team:
		return cursorusage.Query{}, nil
	}
	signedIn, err := cursorsession.CachedAccountEmail(globalDB)
	if err != nil {
		return cursorusage.Query{}, fmt.Errorf("find the Cursor account signed in on this machine: %w (pass --email, --user-id, or --team)", err)
	}
	if signedIn == "" {
		return cursorusage.Query{}, errors.New("no Cursor account is signed in on this machine: pass --email, --user-id, or --team to choose whose usage to collect")
	}
	return cursorusage.Query{Email: signedIn}, nil
}

func parseCursorUsageSince(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.ParseInLocation("2006-01-02", raw, time.Local); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("invalid --since %q: use YYYY-MM-DD or RFC 3339", raw)
}

// resolveCursorUsageStatePath mirrors the other collectors: a system-mode sync keeps its state
// beside the system runtime log, everything else uses the per-user default.
func resolveCursorUsageStatePath(override string, userMode bool) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	if !userMode {
		if dir := filepath.Dir(lifecycle.ResolveRuntimeLog(false, "").EffectiveLogPath); dir != "" && dir != "." {
			return filepath.Join(dir, "cursor-usage-state.json")
		}
	}
	return cursorusage.DefaultStatePath()
}

type cursorUsageScopeStatus struct {
	Scope       string `json:"scope"`
	CoveredFrom string `json:"covered_from,omitempty"`
	CollectedTo string `json:"collected_to,omitempty"`
	LastSync    string `json:"last_sync,omitempty"`
	TrackedKeys int    `json:"tracked_keys"`
}

type cursorUsageStatusReport struct {
	APIKeyEnv string `json:"api_key_env"`
	// APIKeySet says whether the variable is set. The value itself is never read into the report.
	APIKeySet bool `json:"api_key_set"`
	// DefaultScope is the scope a sync with the same flags would use, or empty when none resolves.
	DefaultScope string                   `json:"default_scope,omitempty"`
	ScopeError   string                   `json:"scope_error,omitempty"`
	StatePath    string                   `json:"state_path"`
	Scopes       []cursorUsageScopeStatus `json:"scopes"`
}

func runEndpointCursorUsageStatus(cmd *cobra.Command, args []string) error {
	opts := endpointCursorUsageOpts
	keyEnv := strings.TrimSpace(opts.apiKeyEnv)
	if keyEnv == "" {
		keyEnv = defaultCursorAdminKeyEnv
	}
	statePath := resolveCursorUsageStatePath(opts.statePath, endpointUserMode())
	state, err := cursorusage.LoadState(statePath)
	if err != nil {
		return err
	}
	report := cursorUsageStatusReport{
		APIKeyEnv: keyEnv,
		APIKeySet: strings.TrimSpace(os.Getenv(keyEnv)) != "",
		StatePath: statePath,
		Scopes:    []cursorUsageScopeStatus{},
	}
	if query, err := resolveCursorUsageQuery(opts.email, opts.userID, opts.team, opts.globalDB); err != nil {
		report.ScopeError = err.Error()
	} else {
		report.DefaultScope = cursorusage.ScopeKey(query)
	}
	names := make([]string, 0, len(state.Scopes))
	for name := range state.Scopes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c := state.Scopes[name]
		report.Scopes = append(report.Scopes, cursorUsageScopeStatus{
			Scope:       name,
			CoveredFrom: cursorUsageTime(c.CoveredFromMS),
			CollectedTo: cursorUsageTime(c.HighWaterMS),
			LastSync:    cursorUsageTime(c.LastSyncMS),
			TrackedKeys: len(c.Seen),
		})
	}

	out := cmd.OutOrStdout()
	if endpointOpts.jsonOutput {
		return json.NewEncoder(out).Encode(report)
	}
	keyState := "not set"
	if report.APIKeySet {
		keyState = "set"
	}
	fmt.Fprintf(out, "Cursor Admin API key: %s is %s\n", report.APIKeyEnv, keyState)
	if report.DefaultScope != "" {
		fmt.Fprintf(out, "Scope: %s\n", report.DefaultScope)
	} else {
		fmt.Fprintf(out, "Scope: none (%s)\n", report.ScopeError)
	}
	fmt.Fprintf(out, "State: %s\n", report.StatePath)
	if len(report.Scopes) == 0 {
		fmt.Fprintln(out, "No Cursor usage collected yet.")
		return nil
	}
	for _, s := range report.Scopes {
		fmt.Fprintf(out, "  %s  collected %s .. %s  last sync %s\n", s.Scope, cursorUsageOrDash(s.CoveredFrom), cursorUsageOrDash(s.CollectedTo), cursorUsageOrDash(s.LastSync))
	}
	return nil
}

func cursorUsageTime(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

func cursorUsageOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
