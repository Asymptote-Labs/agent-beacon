package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/mcpconnect"
)

// `beacon mcp connect` registers the Beacon Cloud MCP server in the harnesses on this machine.
// It is an explicit, interactive, per-user command: `beacon endpoint install`, onboarding and
// every unattended path (root, system/MDM installs, CI, piped stdin) never run it. It writes no
// secret -- OAuth harnesses get only the URL, and --token-env writes a reference to a variable --
// and it never reads the Beacon account token in ~/.beacon/auth/session.json. Its only network
// request is the resource-metadata check of the URL, which --dry-run skips.

// Seams for tests. Production uses the real discovery, terminal, uid, HTTP client and CLIs.
var (
	mcpDiscover             = harness.DiscoverAll
	mcpIsTTY                = defaultOnboardingIsTTY
	mcpIsRoot               = func() bool { return os.Geteuid() == 0 }
	mcpStdin      io.Reader = os.Stdin
	mcpHTTPClient           = &http.Client{Timeout: 10 * time.Second}
	mcpLookPath   func(string) (string, error)
	mcpRunCLI     func(ctx context.Context, name string, args ...string) ([]byte, error)
	mcpIngestURL  = defaultMCPIngestURL
)

var mcpConnectOpts struct {
	harnesses []string
	url       string
	tokenEnv  string
	dryRun    bool
	yes       bool
	force     bool
	check     bool
	json      bool
}

var mcpConnectCmd = &cobra.Command{
	Use:   "connect",
	Short: "Register Beacon Cloud MCP in the harnesses on this machine",
	Long: `Register the Beacon Cloud MCP server, as "beacon-managed", in every detected harness that
supports it: Claude Code, Codex CLI, Cursor, VS Code, Gemini CLI and OpenCode.

By default only the URL is written, and each harness signs in with OAuth the first time it
connects. With --token-env, each config references an environment variable holding a personal
MCP token instead; the token itself is never written. Harnesses without confirmed MCP OAuth
support get the steps to add the server by hand.

The URL is --url, or the Beacon Cloud URL recorded by ` + "`beacon endpoint connect`" + `. It is
checked against the server's OAuth resource metadata before anything is written; that check is
this command's only network request. Every changed file is backed up first, and
` + "`beacon mcp disconnect`" + ` removes exactly what connect added.`,
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	RunE:         runMCPConnect,
}

var mcpDisconnectCmd = &cobra.Command{
	Use:          "disconnect",
	Short:        "Remove the Beacon Cloud MCP entries that connect wrote",
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	RunE:         runMCPDisconnect,
}

var mcpStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show where Beacon Cloud MCP is configured",
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	RunE:         runMCPStatus,
}

func init() {
	mcpCmd.AddCommand(mcpConnectCmd, mcpDisconnectCmd, mcpStatusCmd)
	for _, c := range []*cobra.Command{mcpConnectCmd, mcpDisconnectCmd, mcpStatusCmd} {
		c.Flags().StringSliceVar(&mcpConnectOpts.harnesses, "harness", nil, "Harnesses to act on, comma-separated (default: every detected harness)")
	}
	for _, c := range []*cobra.Command{mcpConnectCmd, mcpStatusCmd} {
		c.Flags().StringVar(&mcpConnectOpts.url, "url", "", "Beacon Cloud MCP URL (default: the URL recorded by beacon endpoint connect)")
	}
	f := mcpConnectCmd.Flags()
	f.StringVar(&mcpConnectOpts.tokenEnv, "token-env", "", "Reference a personal MCP token in this environment variable instead of using OAuth")
	f.BoolVar(&mcpConnectOpts.dryRun, "dry-run", false, "Show the plan without checking the URL or writing anything")
	f.BoolVar(&mcpConnectOpts.yes, "yes", false, "Write without asking")
	f.BoolVar(&mcpConnectOpts.force, "force", false, "Replace a beacon-managed entry that Beacon did not write")
	mcpStatusCmd.Flags().BoolVar(&mcpConnectOpts.check, "check", false, "Also check the MCP URL against the server's resource metadata")
	mcpStatusCmd.Flags().BoolVar(&mcpConnectOpts.json, "json", false, "Print status as JSON")
}

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// validateTokenEnv takes the name of a variable. A value that looks like a token is refused, so
// a pasted secret never ends up in a config file.
func validateTokenEnv(name string) error {
	if name == "" {
		return nil
	}
	if strings.HasPrefix(strings.ToLower(name), "bcn_") || !envNamePattern.MatchString(name) {
		return errors.New("--token-env takes the name of an environment variable (for example BEACON_MCP_TOKEN), not a token")
	}
	return nil
}

// defaultMCPIngestURL is the ingest URL `beacon endpoint connect` recorded for this user.
func defaultMCPIngestURL() string {
	enrollment, err := asymptote.LoadEnrollment(true)
	if err != nil {
		return ""
	}
	return enrollment.IngestURL
}

func mcpHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not find the home directory: %w", err)
	}
	return home, nil
}

// resolveMCPTargets turns --harness values, or the detected harnesses, into targets. Detected
// harnesses Beacon has no MCP recipe for are returned by name so the plan can say so.
func resolveMCPTargets(values []string) (targets []mcpconnect.Target, unsupported []string, err error) {
	seen := map[string]bool{}
	add := func(t mcpconnect.Target) {
		if !seen[t.Name] {
			seen[t.Name] = true
			targets = append(targets, t)
		}
	}
	if len(values) > 0 {
		var unknown []string
		for _, v := range values {
			if strings.TrimSpace(v) == "" {
				continue
			}
			t, ok := lookupMCPTarget(v)
			if !ok {
				unknown = append(unknown, v)
				continue
			}
			add(t)
		}
		if len(unknown) > 0 {
			return nil, nil, fmt.Errorf("unknown harness %s; known: %s", strings.Join(unknown, ", "), strings.Join(mcpTargetNames(), ", "))
		}
		return targets, nil, nil
	}
	for _, h := range mcpDiscover() {
		if !h.Detected {
			continue
		}
		if t, ok := mcpconnect.Lookup(h.Name); ok {
			add(t)
			continue
		}
		unsupported = append(unsupported, h.DisplayName)
	}
	return targets, unsupported, nil
}

// lookupMCPTarget accepts every spelling `beacon endpoint install --harness` accepts, plus the
// discovery names and the aliases of the MCP-only targets.
func lookupMCPTarget(value string) (mcpconnect.Target, bool) {
	if t, ok := mcpconnect.Lookup(value); ok {
		return t, true
	}
	if target, ok := normalizeEndpointTarget(value); ok {
		return mcpconnect.Lookup(target.Name)
	}
	if name, ok := normalizeHookTarget(value); ok {
		return mcpconnect.Lookup(name)
	}
	return mcpconnect.Target{}, false
}

func mcpTargetNames() []string {
	var names []string
	for _, t := range mcpconnect.Targets() {
		names = append(names, t.Aliases[0])
	}
	sort.Strings(names)
	return names
}

func mcpOptions(home, url string) mcpconnect.Options {
	return mcpconnect.Options{
		Home: home, URL: url, TokenEnv: mcpConnectOpts.tokenEnv, Force: mcpConnectOpts.force,
		LookPath: mcpLookPath, Run: mcpRunCLI,
	}
}

// connectRefusal is why connect will not run here, or "" when it may.
func connectRefusal() string {
	switch {
	case mcpIsRoot():
		return "beacon mcp connect configures one person's harnesses; run it as that user, not as root"
	case mcpConnectOpts.dryRun, mcpConnectOpts.yes:
		return ""
	case isCIEnvironment():
		return "beacon mcp connect does not run in CI without --yes"
	case !mcpIsTTY():
		return "beacon mcp connect needs a terminal to confirm the changes; pass --yes to write without asking, or --dry-run to see the plan"
	}
	return ""
}

func runMCPConnect(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()
	if reason := connectRefusal(); reason != "" {
		return errors.New(reason)
	}
	if err := validateTokenEnv(mcpConnectOpts.tokenEnv); err != nil {
		return err
	}
	home, err := mcpHome()
	if err != nil {
		return err
	}
	targets, unsupported, err := resolveMCPTargets(mcpConnectOpts.harnesses)
	if err != nil {
		return err
	}
	url, derived, err := mcpconnect.ResolveURL(mcpConnectOpts.url, mcpIngestURL())
	if err != nil {
		return fmt.Errorf("%w (Beacon Cloud serves MCP at https://mcp.beacon.sh)", err)
	}
	checked := "not checked (--dry-run)"
	if !mcpConnectOpts.dryRun {
		canonical, err := mcpconnect.CheckURL(cmd.Context(), mcpHTTPClient, url, derived)
		if err != nil {
			return fmt.Errorf("the MCP URL check failed, so nothing was written: %w", err)
		}
		url, checked = canonical, "checked"
	}
	if len(targets) == 0 {
		fmt.Fprintln(out, "No harness Beacon can connect was detected. Pass --harness to name one.")
		printUnsupported(out, unsupported)
		return nil
	}
	opts := mcpOptions(home, url)
	plan, err := mcpconnect.Plan(opts, targets)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "Beacon Cloud MCP: %s (%s)\n", url, checked)
	fmt.Fprintf(out, "Server name: %s\n", mcpconnect.ServerName)
	if opts.TokenEnv != "" {
		fmt.Fprintf(out, "Auth: a personal MCP token read from $%s; Beacon writes only the variable's name\n", opts.TokenEnv)
	} else {
		fmt.Fprintln(out, "Auth: OAuth; Beacon writes only the URL, and each harness signs in on first use")
	}
	fmt.Fprintln(out)
	printPlan(out, home, plan)
	printManual(out, plan, opts)
	printUnsupported(out, unsupported)

	writes := 0
	for _, it := range plan {
		if it.Writes() {
			writes++
		}
	}
	if mcpConnectOpts.dryRun {
		fmt.Fprintln(out, "\nDry run: nothing was written.")
		return nil
	}
	if writes == 0 {
		fmt.Fprintln(out, "\nNothing to change.")
		printNextSteps(out, plan, opts)
		return nil
	}
	if !mcpConnectOpts.yes {
		fmt.Fprintf(out, "\nWrite %d change(s)? Each changed file is backed up first. [y/N] ", writes)
		if !confirmNo(mcpStdin) {
			fmt.Fprintln(out, "Nothing was written.")
			return nil
		}
	}
	applied, err := mcpconnect.Apply(cmd.Context(), opts, plan)
	if err != nil {
		return err
	}
	fmt.Fprintln(out)
	var failed []string
	for _, it := range applied {
		if !it.Writes() {
			continue
		}
		if it.Err != nil {
			failed = append(failed, it.Target.DisplayName)
			fmt.Fprintf(out, "✗ %s: %v\n", it.Target.DisplayName, it.Err)
			continue
		}
		fmt.Fprintf(out, "✓ %s: %s %s\n", it.Target.DisplayName, pastTense(it.Action), displayPath(home, it.Path))
	}
	printNextSteps(out, applied, opts)
	if len(failed) > 0 {
		return fmt.Errorf("could not connect %s", strings.Join(failed, ", "))
	}
	return nil
}

func confirmNo(in io.Reader) bool {
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

func pastTense(a mcpconnect.Action) string {
	switch a {
	case mcpconnect.ActionAdd:
		return "added to"
	case mcpconnect.ActionUpdate:
		return "updated in"
	case mcpconnect.ActionReplace:
		return "replaced in"
	}
	return string(a)
}

// displayPath shortens a path under the home directory to ~/...
func displayPath(home, path string) string {
	if path == "" {
		return "-"
	}
	if rel, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.Join("~", rel)
	}
	return path
}

func printPlan(out io.Writer, home string, plan []mcpconnect.Item) {
	tw := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "HARNESS\tFILE\tACTION\tAUTH\tNOTE")
	for _, it := range plan {
		if it.Action == mcpconnect.ActionManual {
			continue
		}
		note := it.Detail
		if it.Method != "" && it.Method != "file" && it.Writes() {
			note = strings.TrimSpace("via " + it.Method + " " + note)
		}
		auth := string(it.Auth)
		if it.Action == mcpconnect.ActionSkip || it.Action == mcpconnect.ActionConflict {
			auth = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", it.Target.DisplayName, displayPath(home, it.Path), it.Action, auth, note)
	}
	tw.Flush()
}

func printManual(out io.Writer, plan []mcpconnect.Item, opts mcpconnect.Options) {
	var manual []mcpconnect.Item
	for _, it := range plan {
		if it.Action == mcpconnect.ActionManual {
			manual = append(manual, it)
		}
	}
	if len(manual) == 0 {
		return
	}
	fmt.Fprintln(out, "\nAdd by hand (Beacon writes nothing for these; they have no confirmed MCP OAuth support):")
	for _, it := range manual {
		fmt.Fprintf(out, "\n  %s\n", it.Target.DisplayName)
		for _, line := range strings.Split(it.NextStep(opts), "\n") {
			fmt.Fprintf(out, "    %s\n", line)
		}
	}
}

func printUnsupported(out io.Writer, names []string) {
	if len(names) == 0 {
		return
	}
	fmt.Fprintf(out, "\nNot connected (Beacon does not know how these configure MCP servers): %s\n", strings.Join(names, ", "))
}

func printNextSteps(out io.Writer, items []mcpconnect.Item, opts mcpconnect.Options) {
	var lines []string
	for _, it := range items {
		if it.Err != nil || !(it.Writes() || it.Action == mcpconnect.ActionPresent) {
			continue
		}
		lines = append(lines, fmt.Sprintf("  %s: %s", it.Target.DisplayName, it.NextStep(opts)))
	}
	if len(lines) == 0 {
		return
	}
	fmt.Fprintln(out, "\nNext steps:")
	for _, line := range lines {
		fmt.Fprintln(out, line)
	}
	if opts.TokenEnv == "" {
		fmt.Fprintln(out, "  Restart any harness that is already running so it loads the new server.")
	} else {
		fmt.Fprintf(out, "  Create a token at %s and export it as %s, then restart any running harness.\n", mcpconnect.TokenPage, opts.TokenEnv)
	}
}

func runMCPDisconnect(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()
	home, err := mcpHome()
	if err != nil {
		return err
	}
	targets := mcpconnect.Targets()
	if len(mcpConnectOpts.harnesses) > 0 {
		if targets, _, err = resolveMCPTargets(mcpConnectOpts.harnesses); err != nil {
			return err
		}
	}
	items, err := mcpconnect.Disconnect(cmd.Context(), mcpOptions(home, ""), targets)
	if err != nil {
		return err
	}
	removed := 0
	leftAlone := 0
	var failed []string
	for _, it := range items {
		switch {
		case it.Err != nil:
			failed = append(failed, it.Target.DisplayName)
			fmt.Fprintf(out, "✗ %s: %v\n", it.Target.DisplayName, it.Err)
		case it.Action == mcpconnect.ActionRemove:
			removed++
			fmt.Fprintf(out, "✓ %s: %s (%s)\n", it.Target.DisplayName, it.Detail, displayPath(home, it.Path))
		case it.Action == mcpconnect.ActionAbsent && it.Path != "" && it.Detail != "nothing written by Beacon":
			leftAlone++
			fmt.Fprintf(out, "- %s: %s (%s)\n", it.Target.DisplayName, it.Detail, displayPath(home, it.Path))
		}
	}
	if removed == 0 && len(failed) == 0 && leftAlone == 0 {
		fmt.Fprintln(out, "Nothing to remove: no Beacon Cloud MCP entry written by Beacon was found.")
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not disconnect %s", strings.Join(failed, ", "))
	}
	return nil
}

type mcpStatusRow struct {
	Harness    string `json:"harness"`
	Name       string `json:"display_name"`
	Path       string `json:"path,omitempty"`
	Configured bool   `json:"configured"`
	URL        string `json:"url,omitempty"`
	Auth       string `json:"auth,omitempty"`
	ByBeacon   bool   `json:"written_by_beacon"`
	Detail     string `json:"detail,omitempty"`
}

type mcpStatusReport struct {
	ServerName string         `json:"server_name"`
	URL        string         `json:"url,omitempty"`
	Check      string         `json:"check,omitempty"`
	Harnesses  []mcpStatusRow `json:"harnesses"`
}

func runMCPStatus(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()
	home, err := mcpHome()
	if err != nil {
		return err
	}
	targets, _, err := resolveMCPTargets(mcpConnectOpts.harnesses)
	if err != nil {
		return err
	}
	shown := map[string]bool{}
	for _, t := range targets {
		shown[t.Name] = true
	}
	if len(mcpConnectOpts.harnesses) == 0 {
		// Also inspect every automatic harness, so an entry in one that is not detected any more
		// still shows up.
		for _, t := range mcpconnect.Targets() {
			if t.Automatic && !shown[t.Name] {
				targets = append(targets, t)
			}
		}
	}
	statuses, err := mcpconnect.Inspect(mcpOptions(home, ""), targets)
	if err != nil {
		return err
	}
	report := mcpStatusReport{ServerName: mcpconnect.ServerName}
	url, derived, err := mcpconnect.ResolveURL(mcpConnectOpts.url, mcpIngestURL())
	recorded := mcpconnect.RecordedURL(home)
	switch {
	case err != nil && recorded != "":
		// No --url and no enrollment, but connect has written a URL: that is the one to report
		// and, with --check, to check. It was given explicitly, so it must match exactly.
		url, derived, err = recorded, false, nil
	case err == nil && derived && recorded != "" && !mcpConnectOpts.check:
		// Without --check nothing is fetched, so a derived URL cannot be turned into the
		// canonical one; compare against the URL connect checked and wrote instead.
		url = recorded
	}
	if err == nil {
		report.URL = url
		if mcpConnectOpts.check {
			if canonical, err := mcpconnect.CheckURL(cmd.Context(), mcpHTTPClient, url, derived); err != nil {
				report.Check = "failed: " + err.Error()
			} else {
				report.URL, report.Check = canonical, "ok"
			}
		}
	} else if mcpConnectOpts.check {
		report.Check = "failed: " + err.Error()
	}
	for _, s := range statuses {
		if !shown[s.Target.Name] && !s.Configured {
			continue
		}
		report.Harnesses = append(report.Harnesses, mcpStatusRow{
			Harness: s.Target.Name, Name: s.Target.DisplayName, Path: s.Path, Configured: s.Configured,
			URL: s.URL, Auth: string(s.Auth), ByBeacon: s.ByBeacon, Detail: s.Detail,
		})
	}
	if mcpConnectOpts.json {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
	} else {
		printStatus(out, home, report)
	}
	if mcpConnectOpts.check && report.Check != "ok" {
		return errors.New("the MCP URL check failed")
	}
	return nil
}

func printStatus(out io.Writer, home string, report mcpStatusReport) {
	fmt.Fprintf(out, "Server name: %s\n", report.ServerName)
	if report.URL != "" {
		fmt.Fprintf(out, "Beacon Cloud MCP URL: %s\n", report.URL)
	} else {
		fmt.Fprintln(out, "Beacon Cloud MCP URL: unknown (run `beacon endpoint connect`, or pass --url)")
	}
	if report.Check != "" {
		fmt.Fprintf(out, "URL check: %s\n", report.Check)
	}
	fmt.Fprintln(out)
	if len(report.Harnesses) == 0 {
		fmt.Fprintln(out, "No harness Beacon can connect was detected.")
		return
	}
	tw := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "HARNESS\tCONFIGURED\tURL\tAUTH\tBY BEACON\tFILE")
	for _, r := range report.Harnesses {
		configured, byBeacon, url := "no", "-", "-"
		if r.Configured {
			configured, url = "yes", r.URL
			byBeacon = "no"
			if r.ByBeacon {
				byBeacon = "yes"
			}
			if report.URL != "" && !strings.EqualFold(strings.TrimRight(r.URL, "/"), strings.TrimRight(report.URL, "/")) {
				configured = "yes (other URL)"
			}
		}
		if r.Auth == string(mcpconnect.AuthManual) {
			configured = "unknown"
		}
		auth := r.Auth
		if auth == "" {
			auth = "-"
		}
		file := displayPath(home, r.Path)
		if r.Detail != "" && !r.Configured {
			file = r.Detail
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, configured, url, auth, byBeacon, file)
	}
	tw.Flush()
}
