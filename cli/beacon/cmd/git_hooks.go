package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/gitlink"
)

// gitHookBudget bounds the post-commit hook. Linking reads recent runtime log and writes one
// note, which takes milliseconds on a quiet endpoint and well under a second on a busy one; the
// bound is for the case nothing anticipated, because a person is waiting on `git commit`.
var gitHookBudget = 3 * time.Second

var gitSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Install git hooks that link each new commit to the sessions that wrote it",
	Long: `Install git hooks that link each new commit to the sessions that wrote it.

Beacon adds post-commit and post-rewrite hooks to the repository. A hook already there keeps working: Beacon adds a
marked block to it and a separate beacon-post-commit script beside it, and ` + "`beacon git remove`" + `
takes out exactly that. It also sets notes.rewriteRef, so ` + "`git commit --amend`" + ` and ` + "`git rebase`" + `
carry links to the rewritten commits.

The hook reads the local runtime log and writes a git note. It never touches the network, never
fails a commit, and skips rebases, cherry-picks and reverts. BEACON_GIT_HOOKS=0 turns it off for one
command.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runGitSetup,
}

var gitRemoveCmd = &cobra.Command{
	Use:          "remove",
	Short:        "Remove Beacon's git hooks from the repository (links already written stay)",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runGitRemove,
}

var gitStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show whether this repository links commits automatically",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runGitStatus,
}

var gitHookCmd = &cobra.Command{
	Use:    "hook",
	Short:  "Entry points the installed git hooks call",
	Hidden: true,
}

var gitHookPostCommitCmd = &cobra.Command{
	Use:          "post-commit",
	Short:        "Link HEAD to the sessions that wrote it (called by the post-commit hook)",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runGitHookPostCommit,
}

var gitHookPostRewriteCmd = &cobra.Command{
	Use:          "post-rewrite [amend|rebase]",
	Short:        "Tidy the notes on rewritten commits (called by the post-rewrite hook)",
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         runGitHookPostRewrite,
}

var gitSetupOpts struct {
	allowHooksPath bool
}

func runGitSetup(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	repo, err := gitRepo(ctx)
	if err != nil {
		return err
	}
	result, err := gitlink.InstallHooks(ctx, repo, gitlink.InstallOptions{
		BeaconPath:     beaconExecutable(),
		AllowHooksPath: gitSetupOpts.allowHooksPath,
	})
	out := cmd.OutOrStdout()
	if err != nil {
		switch {
		case errors.Is(err, gitlink.ErrHooksPathSet):
			return fmt.Errorf("%w; git runs this repository's hooks from %s, which a hook manager usually owns.\n"+
				"Add this line to its post-commit hook:\n\n    %s\n\n"+
				"or run `beacon git setup --hooks-path` to install into that directory anyway", err, result.Hooks.Dir, gitlink.HookCall("post-commit"))
		case errors.Is(err, gitlink.ErrForeignHook):
			return fmt.Errorf("%w; Beacon only edits shell hooks. Call Beacon from that hook instead:\n\n    %s", err, gitlink.HookCall("post-commit"))
		}
		return err
	}
	if gitOpts.jsonOutput {
		return writeIndentedJSON(out, result)
	}
	for _, r := range result.Reports {
		fmt.Fprintf(out, "%-9s %s (calls %s)\n", r.Action, r.Path, filepath.Base(r.Script))
	}
	if result.RewriteRefSet {
		fmt.Fprintf(out, "set       notes.rewriteRef = %s\n", gitlink.NotesRef)
	}
	fmt.Fprintln(out, "New commits in this repository are linked to the agent sessions that wrote them.")
	fmt.Fprintln(out, "See them with `beacon git notes`; link an older commit with `beacon git link <commit>`.")
	return nil
}

func runGitRemove(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	repo, err := gitRepo(ctx)
	if err != nil {
		return err
	}
	result, err := gitlink.RemoveHooks(ctx, repo)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if gitOpts.jsonOutput {
		return writeIndentedJSON(out, result)
	}
	for _, r := range result.Reports {
		fmt.Fprintf(out, "%-9s %s\n", r.Action, r.Path)
	}
	if result.RewriteRefRemoved {
		fmt.Fprintf(out, "unset     notes.rewriteRef = %s\n", gitlink.NotesRef)
	}
	fmt.Fprintf(out, "Links already written stay in %s.\n", gitlink.NotesRef)
	return nil
}

func runGitStatus(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	repo, err := gitRepo(ctx)
	if err != nil {
		return err
	}
	status, err := gitlink.HookStatus(ctx, repo)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if gitOpts.jsonOutput {
		return writeIndentedJSON(out, struct {
			gitlink.Status
			Installed bool `json:"installed"`
		}{status, status.Installed()})
	}
	fmt.Fprintf(out, "repository  %s\n", repo.Root)
	fmt.Fprintf(out, "hooks       %s\n", status.Hooks.Dir)
	if status.Hooks.HooksPath != "" {
		fmt.Fprintf(out, "            (core.hooksPath = %s)\n", status.Hooks.HooksPath)
	}
	for _, st := range status.States {
		state := "not installed"
		switch {
		case st.Installed:
			state = "installed"
		case st.BlockPresent:
			state = "broken: the hook calls a script that is missing (run `beacon git setup`)"
		case st.ScriptExists:
			state = "broken: the script exists but the hook does not call it (run `beacon git setup`)"
		}
		fmt.Fprintf(out, "%-11s %s\n", st.Hook, state)
	}
	rewrite := "not set"
	if status.RewriteRef {
		rewrite = "set"
	}
	fmt.Fprintf(out, "rewriteRef  %s\n", rewrite)
	return nil
}

// runGitHookPostCommit is the post-commit hook. It never returns an error: git ignores a
// post-commit hook's exit status anyway, and a hook that printed a stack of errors on every commit
// in a repository with no agent activity would be worse than one that stayed quiet.
func runGitHookPostCommit(cmd *cobra.Command, _ []string) error {
	if os.Getenv(gitlink.DisableEnv) == "0" {
		return nil
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), gitHookBudget)
	defer cancel()
	result, ran := gitPostCommit(ctx)
	if gitOpts.jsonOutput && ran {
		_ = writeIndentedJSON(cmd.OutOrStdout(), result)
	}
	return nil
}

func gitPostCommit(ctx context.Context) (gitlink.Result, bool) {
	repo, err := gitRepo(ctx)
	if err != nil || gitlink.ReplayInProgress(ctx, repo) {
		return gitlink.Result{}, false
	}
	result, err := gitlink.Attribute(ctx, repo, gitlink.Options{LogPath: gitRuntimeLogPath()})
	if err != nil {
		return gitlink.Result{}, false
	}
	return result, true
}

// runGitHookPostRewrite reads git's `<old-sha> <new-sha> [<extra>]` lines and normalizes the note
// on each new commit. Like the post-commit hook, it never returns an error.
func runGitHookPostRewrite(cmd *cobra.Command, _ []string) error {
	if os.Getenv(gitlink.DisableEnv) == "0" {
		return nil
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), gitHookBudget)
	defer cancel()
	repo, err := gitRepo(ctx)
	if err != nil {
		return nil
	}
	scanner := bufio.NewScanner(cmd.InOrStdin())
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		if _, err := gitlink.NormalizeNote(ctx, repo.Git, fields[1]); err != nil && ctx.Err() != nil {
			return nil
		}
	}
	return nil
}

// beaconExecutable is the path hook scripts call first: beacon on PATH when that is this binary
// (a Homebrew symlink survives upgrades; the versioned Cellar path it points to does not), and
// otherwise the running executable.
func beaconExecutable() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	if onPath, err := exec.LookPath("beacon"); err == nil {
		if abs, err := filepath.Abs(onPath); err == nil && sameFile(abs, self) {
			return abs
		}
	}
	return self
}

func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

func init() {
	gitCmd.AddCommand(gitSetupCmd, gitRemoveCmd, gitStatusCmd, gitHookCmd)
	gitHookCmd.AddCommand(gitHookPostCommitCmd, gitHookPostRewriteCmd)
	for _, c := range []*cobra.Command{gitSetupCmd, gitRemoveCmd, gitStatusCmd, gitHookPostCommitCmd, gitHookPostRewriteCmd} {
		c.Flags().StringVarP(&gitOpts.dir, "cwd", "C", "", "Repository to operate on (default the current directory)")
		c.Flags().BoolVar(&gitOpts.jsonOutput, "json", false, "Print the result as JSON")
	}
	gitHookPostCommitCmd.Flags().StringVar(&gitOpts.logPath, "log-path", "", "Runtime JSONL log to read sessions from (default the local runtime log)")
	gitSetupCmd.Flags().BoolVar(&gitSetupOpts.allowHooksPath, "hooks-path", false, "Install into core.hooksPath's directory when it is set")
}
