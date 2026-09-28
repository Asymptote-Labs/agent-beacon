package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/gitlink"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

type gitOptions struct {
	dir         string
	logPath     string
	jsonOutput  bool
	dryRun      bool
	minLookback time.Duration
	maxLookback time.Duration
	limit       int
}

var gitOpts gitOptions

var gitCmd = &cobra.Command{
	Use:   "git",
	Short: "Link git commits to the agent sessions that wrote them",
	Long: `Link git commits to the agent sessions that wrote them.

A link is a line in a git note under ` + gitlink.NotesRef + ` naming the session's harness and id.
Which sessions to link is inferred from the local runtime log: a session is linked to a commit when
it wrote, shortly before the commit, a file the commit changes. The note carries identifiers only;
the session itself stays in the runtime log, and ` + "`beacon handoff export <session>`" + ` reads it back.`,
}

var gitLinkCmd = &cobra.Command{
	Use:   "link [<commit>]",
	Short: "Link a commit (default HEAD) to the sessions that wrote its files",
	Long: `Link a commit (default HEAD) to the sessions that wrote its files.

The runtime log is read from the later of the parent commit and --max-lookback before the commit,
but never less than --min-lookback before it, up to the commit itself. Every session that wrote a
file the commit changes in that span is linked, most files first. Links already on the commit are
kept, and running link again adds only what is new.`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         runGitLink,
}

var gitNotesCmd = &cobra.Command{
	Use:          "notes [<revision>]",
	Short:        "List recent commits linked to agent sessions",
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         runGitNotes,
}

func gitRepo(ctx context.Context) (gitlink.Repo, error) {
	return gitlink.OpenRepo(ctx, gitlink.Git{Dir: gitOpts.dir})
}

func gitRuntimeLogPath() string {
	return lifecycle.ResolveRuntimeLog(true, gitOpts.logPath).EffectiveLogPath
}

func runGitLink(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	repo, err := gitRepo(ctx)
	if err != nil {
		return err
	}
	rev := ""
	if len(args) == 1 {
		rev = args[0]
	}
	result, err := gitlink.Attribute(ctx, repo, gitlink.Options{
		Rev:         rev,
		LogPath:     gitRuntimeLogPath(),
		MinLookback: gitOpts.minLookback,
		MaxLookback: gitOpts.maxLookback,
		DryRun:      gitOpts.dryRun,
	})
	if err != nil {
		return err
	}
	if err := recordLinkEvents(ctx, repo, result); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: the note was written, but the runtime log was not: %v\n", err)
	}
	out := cmd.OutOrStdout()
	if gitOpts.jsonOutput {
		return writeIndentedJSON(out, result)
	}
	printGitLinkResult(out, result)
	return nil
}

// recordLinkEvents writes a session.commit_linked event to the runtime log for each link this run
// added. A dry run, or a run that found every link already on the note, records nothing, so the log
// holds one event per link however often a commit is linked.
func recordLinkEvents(ctx context.Context, repo gitlink.Repo, result gitlink.Result) error {
	if result.DryRun || !result.NoteUpdated || len(result.Added) == 0 {
		return nil
	}
	cc := gitlink.ResolveCommitContext(ctx, repo, result.Commit)
	logPath := gitRuntimeLogPath()
	for _, event := range gitlink.LinkEvents(result, cc, version.GetVersion()) {
		if _, err := writer.AppendEvent(event, writer.Options{Path: logPath, UserMode: true}); err != nil {
			return err
		}
	}
	return nil
}

func printGitLinkResult(out io.Writer, result gitlink.Result) {
	fmt.Fprintf(out, "commit %s  %s\n", shortSHA(result.Commit.SHA), result.Commit.Subject)
	fmt.Fprintf(out, "window %s .. %s\n", result.Since.Local().Format(time.DateTime), result.Until.Local().Format(time.DateTime))
	if result.ChangedFiles == 0 {
		fmt.Fprintln(out, "The commit changes no files; nothing to link.")
		return
	}
	if len(result.Candidates) == 0 {
		fmt.Fprintf(out, "No session in the runtime log wrote any of the %d file(s) this commit changes.\n", result.ChangedFiles)
		return
	}
	for _, c := range result.Candidates {
		state := "linked"
		switch {
		case c.AlreadyLinked:
			state = "already linked"
		case !containsLink(result.Added, c.Link):
			state = "skipped (limit)"
		case result.DryRun:
			state = "would link"
		}
		fmt.Fprintf(out, "  %-15s %s/%s  %d of %d file(s): %s\n", state, c.Harness, c.SessionID,
			len(c.Files), result.ChangedFiles, summarizeFiles(c.Files, 3))
	}
	switch {
	case result.DryRun:
		fmt.Fprintln(out, "Dry run: the note was not written.")
	case result.NoteUpdated:
		fmt.Fprintf(out, "Wrote %d link(s) to %s.\n", len(result.Added), gitlink.NotesRef)
	default:
		fmt.Fprintln(out, "The note already had every link.")
	}
}

func runGitNotes(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	repo, err := gitRepo(ctx)
	if err != nil {
		return err
	}
	rev := ""
	if len(args) == 1 {
		rev = args[0]
	}
	commits, err := gitlink.ListLinked(ctx, repo.Git, rev, gitOpts.limit)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if gitOpts.jsonOutput {
		if commits == nil {
			commits = []gitlink.LinkedCommit{}
		}
		return writeIndentedJSON(out, commits)
	}
	if len(commits) == 0 {
		fmt.Fprintf(out, "No linked commits in the last %d commit(s). `beacon git link` links one.\n", gitOpts.limit)
		return nil
	}
	for _, c := range commits {
		fmt.Fprintf(out, "%s  %s  %s\n", shortSHA(c.SHA), c.CommitTime.Local().Format(time.DateTime), c.Subject)
		for _, link := range c.Links {
			fmt.Fprintf(out, "    %s/%s\n", link.Harness, link.SessionID)
		}
	}
	return nil
}

func writeIndentedJSON(out io.Writer, value interface{}) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func containsLink(links []gitlink.Link, link gitlink.Link) bool {
	for _, l := range links {
		if l == link {
			return true
		}
	}
	return false
}

func summarizeFiles(files []string, max int) string {
	if len(files) <= max {
		return strings.Join(files, ", ")
	}
	return fmt.Sprintf("%s, +%d more", strings.Join(files[:max], ", "), len(files)-max)
}

func init() {
	rootCmd.AddCommand(gitCmd)
	gitCmd.AddCommand(gitLinkCmd, gitNotesCmd)
	for _, c := range []*cobra.Command{gitLinkCmd, gitNotesCmd} {
		c.Flags().StringVarP(&gitOpts.dir, "cwd", "C", "", "Repository to operate on (default the current directory)")
		c.Flags().BoolVar(&gitOpts.jsonOutput, "json", false, "Print the result as JSON")
	}
	lf := gitLinkCmd.Flags()
	lf.StringVar(&gitOpts.logPath, "log-path", "", "Runtime JSONL log to read sessions from (default the local runtime log)")
	lf.BoolVar(&gitOpts.dryRun, "dry-run", false, "Show the links without writing the note")
	lf.DurationVar(&gitOpts.minLookback, "min-lookback", gitlink.DefaultMinLookback, "Always read at least this far before the commit")
	lf.DurationVar(&gitOpts.maxLookback, "max-lookback", gitlink.DefaultMaxLookback, "Never read further than this before the commit")
	gitNotesCmd.Flags().IntVar(&gitOpts.limit, "limit", 20, "Number of commits to walk")
}
