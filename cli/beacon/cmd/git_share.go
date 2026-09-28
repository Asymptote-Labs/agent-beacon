package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/gitlink"
)

// gitShareBudget bounds the pre-push hook's own push of the notes ref. It is network time, so it
// is longer than the post-commit budget, and it still ends well before a person would think their
// push had hung.
var gitShareBudget = 10 * time.Second

var gitNotesPushCmd = &cobra.Command{
	Use:   "push [<remote>]",
	Short: "Share this repository's commit links with a remote (default the branch's remote, or origin)",
	Long: `Share this repository's commit links with a remote.

Fetches the remote's ` + gitlink.NotesRef + `, merges it with the local one (keeping every line of both), and
pushes the result. Nothing but the notes ref is pushed, and the repository's own pre-push hooks do not
run for it.`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         runGitNotesPush,
}

var gitNotesFetchCmd = &cobra.Command{
	Use:   "fetch [<remote>]",
	Short: "Fetch a remote's commit links so `beacon git notes` shows them",
	Long: `Fetch a remote's commit links so ` + "`beacon git notes`" + ` shows them.

The links land in ` + gitlink.RemoteNotesPrefix + `<remote>, a read-only copy that ` + "`beacon git notes`" + ` reads
alongside the local links. The local ` + gitlink.NotesRef + ` is not changed; ` + "`beacon git notes push`" + ` merges.`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         runGitNotesFetch,
}

var gitHookPrePushCmd = &cobra.Command{
	Use:          "pre-push <remote> [<url>]",
	Short:        "Share commit links with the remote being pushed to (called by the pre-push hook)",
	Args:         cobra.RangeArgs(1, 2),
	SilenceUsage: true,
	RunE:         runGitHookPrePush,
}

func defaultRemote(ctx context.Context, repo gitlink.Repo) string {
	if branch, err := repo.Git.Run(ctx, nil, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil && branch != "" {
		if remote, err := repo.Git.Run(ctx, nil, "config", "--get", "branch."+branch+".remote"); err == nil && remote != "" && remote != "." {
			return remote
		}
	}
	return "origin"
}

func runGitNotesPush(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	repo, err := gitRepo(ctx)
	if err != nil {
		return err
	}
	remote := defaultRemote(ctx, repo)
	if len(args) == 1 {
		remote = args[0]
	}
	result, err := gitlink.PushNotes(ctx, repo, remote)
	if err != nil {
		return fmt.Errorf("share links with %s: %w", remote, err)
	}
	out := cmd.OutOrStdout()
	if gitOpts.jsonOutput {
		return writeIndentedJSON(out, result)
	}
	switch {
	case !result.Pushed:
		fmt.Fprintln(out, "No commit links here to share.")
	case result.Merged:
		fmt.Fprintf(out, "Merged %s's links and pushed %s to %s.\n", remote, gitlink.NotesRef, remote)
	default:
		fmt.Fprintf(out, "Pushed %s to %s.\n", gitlink.NotesRef, remote)
	}
	return nil
}

func runGitNotesFetch(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	repo, err := gitRepo(ctx)
	if err != nil {
		return err
	}
	remote := defaultRemote(ctx, repo)
	if len(args) == 1 {
		remote = args[0]
	}
	found, err := gitlink.FetchNotes(ctx, repo, remote)
	if err != nil {
		return fmt.Errorf("fetch links from %s: %w", remote, err)
	}
	out := cmd.OutOrStdout()
	if gitOpts.jsonOutput {
		return writeIndentedJSON(out, map[string]interface{}{"remote": remote, "found": found, "ref": gitlink.TrackingRef(remote)})
	}
	if !found {
		fmt.Fprintf(out, "%s has no commit links yet.\n", remote)
		return nil
	}
	fmt.Fprintf(out, "Fetched %s's links into %s.\n", remote, gitlink.TrackingRef(remote))
	return nil
}

// runGitHookPrePush is the pre-push hook. It never fails the push: sharing links is a courtesy to
// teammates, and the person's code reaching the remote matters more. A failure is one line on
// stderr, which git shows alongside its own output.
func runGitHookPrePush(cmd *cobra.Command, args []string) error {
	if os.Getenv(gitlink.DisableEnv) == "0" {
		return nil
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), gitShareBudget)
	defer cancel()
	repo, err := gitRepo(ctx)
	if err != nil {
		return nil
	}
	// A hook has no terminal to ask for credentials on; git's own push has already authenticated,
	// and a credential helper answers the same way for this one.
	repo.Git = repo.Git.WithExtraEnv("GIT_TERMINAL_PROMPT=0")
	if _, err := gitlink.PushNotes(ctx, repo, args[0]); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "beacon: commit links were not shared with %s: %s\n", args[0], gitlink.ErrorSummary(err))
	}
	return nil
}

func init() {
	gitNotesCmd.AddCommand(gitNotesPushCmd, gitNotesFetchCmd)
	gitHookCmd.AddCommand(gitHookPrePushCmd)
	for _, c := range []*cobra.Command{gitNotesPushCmd, gitNotesFetchCmd, gitHookPrePushCmd} {
		c.Flags().StringVarP(&gitOpts.dir, "cwd", "C", "", "Repository to operate on (default the current directory)")
		if c != gitHookPrePushCmd {
			c.Flags().BoolVar(&gitOpts.jsonOutput, "json", false, "Print the result as JSON")
		}
	}
}
