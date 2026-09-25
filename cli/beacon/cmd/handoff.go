package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

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

// handoffSources is swapped by tests.
var handoffSources = func(dirs handoff.StoreDirs) []handoff.Source { return handoff.DefaultSources(dirs) }

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
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve current directory: %w", err)
		}
		filter.Directory = wd
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
	handoffCmd.AddCommand(handoffListCmd)
	addHandoffStoreFlags(handoffListCmd)
	f := handoffListCmd.Flags()
	f.BoolVar(&handoffOpts.jsonOutput, "json", false, "Print sessions as JSON")
	f.BoolVar(&handoffOpts.here, "here", false, "Only sessions that ran in the current directory or below it")
	f.StringVar(&handoffOpts.directory, "dir", "", "Only sessions that ran in this directory or below it")
	f.BoolVar(&handoffOpts.includeSubagents, "subagents", false, "Include subagent sessions")
	f.IntVar(&handoffOpts.limit, "limit", 25, "Maximum sessions to list (0 for all)")
}
