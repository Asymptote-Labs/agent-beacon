package cmd

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/auth"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lensstore"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// The lens authoring loop: read the spec, look at real data, write the file, lint it, and preview
// it against a real session in the dashboard before installing it with `beacon lenses add`.

// lensPreviewAddr is one port above the dashboard's default, so a preview can run next to it.
const lensPreviewAddr = "127.0.0.1:8766"

var lensToolsOpts struct {
	example bool
	strict  bool
	json    bool
	session string
	trace   string
	logPath string
	addr    string
	open    bool
}

var lensesSpecCmd = &cobra.Command{
	Use:   "spec",
	Short: "Print the lens specification (or, with --example, a complete example lens)",
	Long: `Print spec/lenses/SPEC.md: the file format, the data window.beacon.getTrace() returns,
the sandbox rules and the style tokens. It is carried in the binary, so it works offline.
With --example, print a complete lens to start from (the built-in activity lens).`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if lensToolsOpts.example {
			_, err := cmd.OutOrStdout().Write(dashboard.ExampleLens())
			return err
		}
		_, err := fmt.Fprint(cmd.OutOrStdout(), dashboard.LensSpec())
		return err
	},
}

var lensesLintCmd = &cobra.Command{
	Use:   "lint <file>...",
	Short: "Check lens files against the spec",
	Long: `Check lens files against spec/lenses/SPEC.md. Errors (an oversized file, a missing or
invalid manifest) stop a lens from running. Warnings flag code the sandbox will block or the
spec forbids: HTML parsing of trace content, network requests, resources loaded by URL,
browser storage, navigation, viewport-height layouts and a lens that never calls getTrace().
Exits non-zero on errors, or on warnings with --strict.`,
	Args:         cobra.MinimumNArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := cmd.OutOrStdout()
		type fileResult struct {
			Path     string                  `json:"path"`
			Findings []lensstore.LintFinding `json:"findings"`
		}
		var results []fileResult
		errorsFound, warningsFound := 0, 0
		for _, path := range args {
			html, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			findings := lensstore.Lint(html)
			if findings == nil {
				findings = []lensstore.LintFinding{}
			}
			results = append(results, fileResult{Path: path, Findings: findings})
			for _, f := range findings {
				if f.Severity == lensstore.LintError {
					errorsFound++
				} else {
					warningsFound++
				}
				if lensToolsOpts.json {
					continue
				}
				if f.Line > 0 {
					fmt.Fprintf(out, "%s:%d: %s: %s\n", path, f.Line, f.Severity, f.Message)
				} else {
					fmt.Fprintf(out, "%s: %s: %s\n", path, f.Severity, f.Message)
				}
			}
		}
		if lensToolsOpts.json {
			if err := writeIndentedJSON(out, map[string]interface{}{"files": results, "errors": errorsFound, "warnings": warningsFound}); err != nil {
				return err
			}
		} else {
			fmt.Fprintf(out, "%d error(s), %d warning(s) in %d file(s).\n", errorsFound, warningsFound, len(args))
		}
		switch {
		case errorsFound > 0:
			return fmt.Errorf("%d lint error(s)", errorsFound)
		case lensToolsOpts.strict && warningsFound > 0:
			return fmt.Errorf("%d lint warning(s) with --strict", warningsFound)
		}
		return nil
	},
}

var lensesDataCmd = &cobra.Command{
	Use:   "data",
	Short: "Print the data a lens receives for a session or trace",
	Long: `Print the exact LensDataV1 that window.beacon.getTrace() resolves with, for one session
(the dashboard's session page) or one trace. With neither flag, the most recent session is
used. Reads only the local log; the output includes whatever content the log retained.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		logPath := lensesLogPath()
		opts := dashboard.LensDataOptions{UserMode: lensesUserMode()}
		var (
			data dashboard.LensDataV1
			ok   bool
			err  error
		)
		switch {
		case lensToolsOpts.session != "" && lensToolsOpts.trace != "":
			return errors.New("pass --session or --trace, not both")
		case lensToolsOpts.trace != "":
			data, ok, err = dashboard.BuildLensData(logPath, lensToolsOpts.trace, opts)
		default:
			session := lensToolsOpts.session
			if session == "" {
				if session, err = latestSession(logPath); err != nil {
					return err
				}
			}
			data, ok, err = dashboard.BuildSessionLensData(logPath, session, opts)
		}
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("no such session or trace in the runtime log")
		}
		return writeIndentedJSON(cmd.OutOrStdout(), data)
	},
}

var lensesPreviewCmd = &cobra.Command{
	Use:   "preview <file>",
	Short: "Serve a lens file in the dashboard against a real session, re-read on every reload",
	Long: `Lint a lens file, then run the dashboard with it added and print the session page that
shows it. The file is read again on every page load, so edit it and reload. Nothing is
installed; use 'beacon lenses add' when it is done. Runs until interrupted.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := cmd.OutOrStdout()
		file, err := filepath.Abs(args[0])
		if err != nil {
			return err
		}
		html, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		findings := lensstore.Lint(html)
		for _, f := range findings {
			fmt.Fprintf(out, "%s:%d: %s: %s\n", args[0], f.Line, f.Severity, f.Message)
		}
		if lensstore.HasLintErrors(findings) {
			return errors.New("fix the lint errors before previewing")
		}
		manifest, _ := asymptoteobserve.ParseLensManifest(html) // lint already proved it valid
		reserved, err := dashboard.BuiltinLensIDs()
		if err != nil {
			return err
		}
		if reserved[manifest.ID] {
			return fmt.Errorf("%q is a built-in lens id; give this lens another id", manifest.ID)
		}
		if err := dashboard.ValidateLoopbackAddr(lensToolsOpts.addr); err != nil {
			return err
		}
		logPath := lensesLogPath()
		session := lensToolsOpts.session
		if session == "" {
			session, _ = latestSession(logPath)
		}
		page := dashboard.URL(lensToolsOpts.addr)
		if session != "" {
			page = strings.TrimRight(page, "/") + "/session.html?" + url.Values{"id": {session}, "lens": {manifest.ID}}.Encode()
		}
		fmt.Fprintf(out, "Previewing %s (%s) from %s\n", manifest.Title, manifest.ID, file)
		if session == "" {
			fmt.Fprintln(out, "No sessions in the runtime log yet; open a session in the dashboard and add the lens from its + menu.")
		}
		fmt.Fprintf(out, "Open: %s\nEdits to the file show on reload. Press Ctrl-C to stop.\n", page)
		if lensToolsOpts.open {
			if err := dashboard.OpenBrowser(page); err != nil && !errors.Is(err, auth.ErrNoDisplay) {
				return err
			}
		}
		return dashboardListenAndServe(dashboard.Options{
			Addr:      lensToolsOpts.addr,
			LogPath:   logPath,
			UserMode:  lensesUserMode(),
			LensFiles: []string{file},
		})
	},
}

func lensesLogPath() string {
	return lifecycle.ResolveRuntimeLog(lensesUserMode(), lensToolsOpts.logPath).EffectiveLogPath
}

// latestSession returns the most recently active session in the log.
func latestSession(logPath string) (string, error) {
	sessions, err := dashboard.ReadSessions(logPath, dashboard.EventQuery{Limit: 1})
	if err != nil {
		return "", err
	}
	if len(sessions.Sessions) == 0 {
		return "", errors.New("the runtime log has no sessions yet; pass --session or --trace")
	}
	return sessions.Sessions[0].ID, nil
}

func init() {
	lensesSpecCmd.Flags().BoolVar(&lensToolsOpts.example, "example", false, "Print a complete example lens instead of the spec")
	lensesLintCmd.Flags().BoolVar(&lensToolsOpts.strict, "strict", false, "Fail on warnings too")
	lensesLintCmd.Flags().BoolVar(&lensToolsOpts.json, "json", false, "Print JSON")
	for _, c := range []*cobra.Command{lensesDataCmd, lensesPreviewCmd} {
		c.Flags().StringVar(&lensToolsOpts.session, "session", "", "Session ID (default: the most recent session)")
		c.Flags().StringVar(&lensToolsOpts.logPath, "log-path", "", "Runtime JSONL log path")
		c.Flags().BoolVar(&lensesOpts.userMode, "user", true, "Use per-user endpoint paths")
		c.Flags().BoolVar(&lensesOpts.systemMode, "system", false, "Use system endpoint paths")
	}
	lensesDataCmd.Flags().StringVar(&lensToolsOpts.trace, "trace", "", "Trace ID instead of a session")
	lensesPreviewCmd.Flags().StringVar(&lensToolsOpts.addr, "addr", lensPreviewAddr, "Loopback address to serve the preview on")
	lensesPreviewCmd.Flags().BoolVar(&lensToolsOpts.open, "open", false, "Open the preview in a browser")
	lensesCmd.AddCommand(lensesSpecCmd, lensesLintCmd, lensesDataCmd, lensesPreviewCmd)
}
