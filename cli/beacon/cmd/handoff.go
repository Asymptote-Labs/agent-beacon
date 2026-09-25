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
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/handoff"
)

type handoffOptions struct {
	jsonOutput       bool
	harness          string
	here             bool
	directory        string
	includeSubagents bool
	limit            int
	claudeDir        string
	codexDir         string
	openCodeDir      string
	clineDir         string
	storeDirs        []string
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

// handoffSubject is the session a handoff command acts on, found in its runtime's store or, when
// the store no longer has it, in Beacon's runtime log.
type handoffSubject struct {
	session   handoff.Session
	fromLog   bool
	logEvents []schema.Event
}

func resolveHandoffSession(id string) (handoffSubject, error) {
	harness, err := handoff.ParseHarness(handoffOpts.harness)
	if err != nil {
		return handoffSubject{}, err
	}
	dirs, err := handoffStoreDirs()
	if err != nil {
		return handoffSubject{}, err
	}
	session, findErr := handoff.Find(handoffSources(dirs), harness, id)
	if findErr == nil {
		return handoffSubject{session: session}, nil
	}
	var notFound *handoff.NotFoundError
	if !errors.As(findErr, &notFound) {
		return handoffSubject{}, findErr
	}
	return resolveHandoffLogSession(id, harness, notFound)
}

// resolveHandoffLogSession finds id in the runtime log. It refuses a session whose own store could
// not be read: that session may well still be there, and a brief from the log would keep less of it
// than the store does.
func resolveHandoffLogSession(id, harness string, notFound *handoff.NotFoundError) (handoffSubject, error) {
	logPath := handoffRuntimeLogPath()
	session, events, found, err := handoff.LogSession(logPath, id, harness)
	var ambiguous *handoff.AmbiguousError
	if errors.As(err, &ambiguous) {
		return handoffSubject{}, fmt.Errorf("in the runtime log %s: %w", logPath, err)
	}
	if err != nil {
		return handoffSubject{}, fmt.Errorf("read runtime log %s: %w", logPath, err)
	}
	if !found || (harness != "" && session.Harness != harness) {
		if notFound == nil {
			notFound = &handoff.NotFoundError{ID: id}
		}
		return handoffSubject{}, fmt.Errorf("%w; not in the runtime log %s either (`beacon handoff list` shows what is available)", notFound, logPath)
	}
	if notFound != nil {
		if storeErr := notFound.UnreadableStore(session.Harness); storeErr != nil {
			return handoffSubject{}, fmt.Errorf("the %s session store could not be read (%v); fix that rather than build the brief from the runtime log, which keeps less", session.Harness, storeErr.Err)
		}
	}
	return handoffSubject{session: session, fromLog: true, logEvents: events}, nil
}

// brief builds the subject's brief. A session its store loses between lookup and read falls back to
// the runtime log.
func (subject handoffSubject) brief() (handoff.Brief, error) {
	if subject.fromLog {
		return handoff.BuildBrief(subject.session, subject.logEvents, handoff.FromRuntimeLog, handoffNow()), nil
	}
	session := subject.session
	dirs, err := handoffStoreDirs()
	if err != nil {
		return handoff.Brief{}, err
	}
	events, err := handoff.Events(handoffSources(dirs), session)
	if err == nil {
		return handoff.BuildBrief(session, events, handoff.FromSessionStore, handoffNow()), nil
	}
	if !errors.Is(err, handoff.ErrNotFound) {
		return handoff.Brief{}, fmt.Errorf("read %s session %s: %w", session.Harness, session.ID, err)
	}
	logSubject, logErr := resolveHandoffLogSession(session.ID, session.Harness, nil)
	if logErr != nil {
		return handoff.Brief{}, logErr
	}
	return logSubject.brief()
}

func loadHandoffBrief(id string) (handoff.Brief, error) {
	subject, err := resolveHandoffSession(id)
	if err != nil {
		return handoff.Brief{}, err
	}
	return subject.brief()
}

// handoffLogReason says why a session was read from the runtime log.
func handoffLogReason(session handoff.Session) string {
	for _, harness := range handoff.Harnesses {
		if harness == session.Harness {
			return fmt.Sprintf("%s session %s is no longer in its runtime's store", session.Harness, session.ID)
		}
	}
	return fmt.Sprintf("beacon handoff does not read %s session stores", session.Harness)
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
		fmt.Fprintf(cmd.ErrOrStderr(), "note: %s; the brief comes from Beacon's runtime log and keeps less of each step\n", handoffLogReason(brief.Session))
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
	dirs, err := handoffStoreDirs()
	if err != nil {
		return err
	}
	sessions, listErr := handoff.List(handoffSources(dirs), filter)
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

// handoffStoreDirs collects the store directory overrides. --store-dir names any runtime; the
// per-runtime flags predate it and win over it for their runtime.
func handoffStoreDirs() (handoff.StoreDirs, error) {
	dirs := handoff.StoreDirs{}
	for _, entry := range handoffOpts.storeDirs {
		name, dir, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(name) == "" || dir == "" {
			return nil, fmt.Errorf("--store-dir %q: want <runtime>=<path>", entry)
		}
		harness, err := handoff.ParseHarness(name)
		if err != nil {
			return nil, fmt.Errorf("--store-dir %q: %w", entry, err)
		}
		dirs[harness] = dir
	}
	for harness, dir := range map[string]string{
		handoff.HarnessClaude:   handoffOpts.claudeDir,
		handoff.HarnessCodex:    handoffOpts.codexDir,
		handoff.HarnessOpenCode: handoffOpts.openCodeDir,
		handoff.HarnessCline:    handoffOpts.clineDir,
	} {
		if dir != "" {
			dirs[harness] = dir
		}
	}
	// Session file paths come from these directories, and a runtime is started in the session's
	// own directory, so a relative store path would name another file there.
	for harness, dir := range dirs {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, fmt.Errorf("resolve the %s store directory %s: %w", harness, dir, err)
		}
		dirs[harness] = abs
	}
	return dirs, nil
}

func addHandoffStoreFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&handoffOpts.harness, "harness", "", "Only this runtime: "+strings.Join(handoff.RuntimeNames(), ", "))
	f.StringArrayVar(&handoffOpts.storeDirs, "store-dir", nil, "Read a runtime's sessions from this directory, as <runtime>=<path> (repeatable)")
	f.StringVar(&handoffOpts.claudeDir, "claude-projects-dir", "", "Claude projects directory (default ~/.claude/projects)")
	f.StringVar(&handoffOpts.codexDir, "codex-dir", "", "Codex directory (default ~/.codex)")
	f.StringVar(&handoffOpts.openCodeDir, "opencode-dir", "", "OpenCode data directory or opencode.db path")
	f.StringVar(&handoffOpts.clineDir, "cline-dir", "", "Cline directory (default ~/.cline)")
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
