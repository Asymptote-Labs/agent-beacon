package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/handoff"
)

type handoffOptions struct {
	jsonOutput       bool
	harness          string
	here             bool
	directory        string
	includeSubagents bool
	limit            int
	dirs             handoff.StoreDirs
	print            bool
	outputDir        string
	logPath          string
}

var handoffOpts handoffOptions

var handoffCmd = &cobra.Command{
	Use:   "handoff",
	Short: "Pick up a local agent session again, in the same runtime or another one",
	Long: `Pick up a local agent session again, in the same runtime or another one.

Sessions are read from each runtime's own local session store (Claude Code, Codex CLI, OpenCode
and Cline). Nothing is uploaded and no runtime file is modified.`,
}

var handoffListCmd = &cobra.Command{
	Use:          "list",
	Short:        "List resumable local agent sessions, newest first",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runHandoffList,
}

var handoffExportCmd = &cobra.Command{
	Use:   "export <session-id>",
	Short: "Write a handoff brief for a session",
	Long: `Write a handoff brief for a session: a Markdown summary of what it asked for, what it changed and
where it stopped, for another agent or person to continue from.

The brief is built from the runtime's own session store. A session its runtime no longer has is
read from Beacon's runtime log instead, which keeps less of each step. Briefs are written under
~/.beacon/endpoint/handoffs (0700, files 0600); --print writes to stdout instead.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runHandoffExport,
}

// handoffSources and handoffNow are swapped by tests.
var (
	handoffSources = func(dirs handoff.StoreDirs) []handoff.Source { return handoff.DefaultSources(dirs) }
	handoffNow     = time.Now
)

// loadHandoffBrief builds the brief for id from the runtime's session store, or from the runtime log
// when the store no longer has the session.
func loadHandoffBrief(id string) (handoff.Brief, error) {
	harness, err := handoff.ParseHarness(handoffOpts.harness)
	if err != nil {
		return handoff.Brief{}, err
	}
	sources := handoffSources(handoffOpts.dirs)
	session, findErr := handoff.Find(sources, harness, id)
	if findErr == nil {
		events, err := handoff.Events(sources, session)
		if err == nil {
			return handoff.BuildBrief(session, events, handoff.FromSessionStore, handoffNow()), nil
		}
		if !errors.Is(err, handoff.ErrNotFound) {
			return handoff.Brief{}, fmt.Errorf("read %s session %s: %w", session.Harness, session.ID, err)
		}
		id = session.ID
	} else if !errors.Is(findErr, handoff.ErrNotFound) {
		return handoff.Brief{}, findErr
	}
	logPath := handoffRuntimeLogPath()
	logSession, events, found, err := handoff.LogSession(logPath, id)
	if err != nil {
		return handoff.Brief{}, fmt.Errorf("read runtime log %s: %w", logPath, err)
	}
	if !found || (harness != "" && logSession.Harness != harness) {
		return handoff.Brief{}, fmt.Errorf("%w: %s (not in any runtime session store or in %s; `beacon handoff list` shows what is available)", handoff.ErrNotFound, id, logPath)
	}
	return handoff.BuildBrief(logSession, events, handoff.FromRuntimeLog, handoffNow()), nil
}

func handoffRuntimeLogPath() string {
	return lifecycle.ResolveRuntimeLog(true, handoffOpts.logPath).EffectiveLogPath
}

func handoffBriefDir() string {
	if dir := strings.TrimSpace(handoffOpts.outputDir); dir != "" {
		return dir
	}
	return filepath.Join(endpointconfig.BaseDir(true), "handoffs")
}

type handoffExportResult struct {
	Path    string          `json:"path"`
	From    string          `json:"from"`
	Session handoff.Session `json:"session"`
}

func runHandoffExport(cmd *cobra.Command, args []string) error {
	if handoffOpts.print && strings.TrimSpace(handoffOpts.outputDir) != "" {
		return fmt.Errorf("--print and --output-dir cannot be combined")
	}
	brief, err := loadHandoffBrief(args[0])
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if handoffOpts.print {
		_, err := io.WriteString(out, brief.Render())
		return err
	}
	path, err := handoff.WriteBrief(handoffBriefDir(), brief)
	if err != nil {
		return err
	}
	if handoffOpts.jsonOutput {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(handoffExportResult{Path: path, From: brief.From, Session: brief.Session})
	}
	if brief.From == handoff.FromRuntimeLog {
		fmt.Fprintf(cmd.ErrOrStderr(), "note: %s session %s is no longer in its runtime's store; the brief comes from Beacon's runtime log and keeps less of each step\n", brief.Session.Harness, brief.Session.ID)
	}
	fmt.Fprintln(out, path)
	return nil
}

func runHandoffList(cmd *cobra.Command, args []string) error {
	harness, err := handoff.ParseHarness(handoffOpts.harness)
	if err != nil {
		return err
	}
	filter := handoff.Filter{
		Harness:          harness,
		Directory:        strings.TrimSpace(handoffOpts.directory),
		IncludeSubagents: handoffOpts.includeSubagents,
		Limit:            handoffOpts.limit,
	}
	if handoffOpts.here {
		if filter.Directory != "" {
			return fmt.Errorf("--here and --dir cannot be combined")
		}
		filter.Directory = "."
	}
	if filter.Directory != "" {
		// Recorded session directories are absolute; a relative --dir means relative to here.
		abs, err := filepath.Abs(filter.Directory)
		if err != nil {
			return fmt.Errorf("resolve --dir %s: %w", filter.Directory, err)
		}
		filter.Directory = abs
	}
	sessions, listErr := handoff.List(handoffSources(handoffOpts.dirs), filter)
	out := cmd.OutOrStdout()
	if handoffOpts.jsonOutput {
		result := handoffListResult{Sessions: sessions}
		if result.Sessions == nil {
			result.Sessions = []handoff.Session{}
		}
		if listErr != nil {
			result.Warnings = strings.Split(listErr.Error(), "\n")
		}
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	if listErr != nil {
		for _, line := range strings.Split(listErr.Error(), "\n") {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not read %s\n", line)
		}
	}
	printHandoffSessions(out, sessions, time.Now())
	return nil
}

type handoffListResult struct {
	Sessions []handoff.Session `json:"sessions"`
	Warnings []string          `json:"warnings,omitempty"`
}

func printHandoffSessions(out io.Writer, sessions []handoff.Session, now time.Time) {
	if len(sessions) == 0 {
		fmt.Fprintln(out, "No resumable sessions found.")
		return
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tRUNTIME\tUPDATED\tDIRECTORY\tTITLE")
	for _, s := range sessions {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.ID, handoffRuntimeLabel(s), handoffAge(s.UpdatedAt, now), s.Directory, truncateRunes(s.Title, 60))
	}
	w.Flush()
}

func handoffRuntimeLabel(s handoff.Session) string {
	if s.Subagent {
		return s.Harness + " (subagent)"
	}
	return s.Harness
}

func handoffAge(when, now time.Time) string {
	if when.IsZero() {
		return "-"
	}
	d := now.Sub(when)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return when.Local().Format("2006-01-02")
	}
}

func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}

func addHandoffStoreFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&handoffOpts.harness, "harness", "", "Only this runtime: claude, codex, opencode or cline")
	f.StringVar(&handoffOpts.dirs.ClaudeProjects, "claude-projects-dir", "", "Claude projects directory (default ~/.claude/projects)")
	f.StringVar(&handoffOpts.dirs.Codex, "codex-dir", "", "Codex directory (default ~/.codex)")
	f.StringVar(&handoffOpts.dirs.OpenCode, "opencode-dir", "", "OpenCode data directory or opencode.db path")
	f.StringVar(&handoffOpts.dirs.Cline, "cline-dir", "", "Cline directory (default ~/.cline)")
}

func init() {
	rootCmd.AddCommand(handoffCmd)
	handoffCmd.AddCommand(handoffListCmd, handoffExportCmd)
	addHandoffStoreFlags(handoffExportCmd)
	ef := handoffExportCmd.Flags()
	ef.BoolVar(&handoffOpts.print, "print", false, "Write the brief to stdout instead of a file")
	ef.StringVar(&handoffOpts.outputDir, "output-dir", "", "Directory to write the brief into (default ~/.beacon/endpoint/handoffs)")
	ef.StringVar(&handoffOpts.logPath, "log-path", "", "Runtime JSONL log to fall back to (default the local runtime log)")
	ef.BoolVar(&handoffOpts.jsonOutput, "json", false, "Print the written path and session as JSON")

	addHandoffStoreFlags(handoffListCmd)
	f := handoffListCmd.Flags()
	f.BoolVar(&handoffOpts.jsonOutput, "json", false, "Print sessions as JSON")
	f.BoolVar(&handoffOpts.here, "here", false, "Only sessions that ran in the current directory or below it")
	f.StringVar(&handoffOpts.directory, "dir", "", "Only sessions that ran in this directory or below it")
	f.BoolVar(&handoffOpts.includeSubagents, "subagents", false, "Include subagent sessions")
	f.IntVar(&handoffOpts.limit, "limit", 25, "Maximum sessions to list (0 for all)")
}
