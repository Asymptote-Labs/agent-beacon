package cmd

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/handoff"
)

type handoffResumeOptions struct {
	agent    string
	newOnly  bool
	cwd      string
	print    bool
	yes      bool
	jsonPlan bool
}

var handoffResumeOpts handoffResumeOptions

var handoffResumeCmd = &cobra.Command{
	Use:   "resume <session-id>",
	Short: "Pick a session up again, in its own runtime or another one",
	Long: `Pick a session up again, in its own runtime or another one.

When the session's own runtime can reopen it, it is reopened with its full context:
  claude --resume <id>, codex resume <id>, opencode --session <id>, cline --tui --id <id>

Otherwise, or with --agent naming another runtime or --new, Beacon writes a handoff brief of the
session and starts a new session whose first message points at it. The brief is passed by path,
never inlined into the command line. Beacon cannot start DeepSeek Harness, so its sessions continue
in another runtime named with --agent.

The runtime runs in the session's directory, in this terminal. Beacon asks before launching unless
--yes is given; --print shows what would run without writing or launching anything. Cline is always
started with --auto-approve false.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runHandoffResume,
}

// Swapped by tests.
var (
	handoffLookPath             = exec.LookPath
	handoffLaunch               = handoff.Launch
	handoffIsTerminal           = func() bool { return isTerminal(os.Stdin) && isTerminal(os.Stdout) }
	handoffStdin      io.Reader = os.Stdin
	handoffExit                 = os.Exit
)

func runHandoffResume(cmd *cobra.Command, args []string) error {
	opts := handoffResumeOpts
	if opts.jsonPlan && !opts.print {
		return errors.New("--json describes the plan and needs --print")
	}
	target, err := handoff.ParseHarness(opts.agent)
	if err != nil {
		return fmt.Errorf("--agent: %w", err)
	}
	subject, err := resolveHandoffSession(args[0])
	if err != nil {
		return err
	}
	session := subject.session
	now := handoffNow()
	// The runtime starts in the session's directory, not Beacon's, so every path handed to it is
	// absolute.
	briefDir, err := filepath.Abs(handoffBriefDir())
	if err != nil {
		return fmt.Errorf("resolve brief directory: %w", err)
	}
	cwd := strings.TrimSpace(opts.cwd)
	if cwd != "" {
		if cwd, err = filepath.Abs(cwd); err != nil {
			return fmt.Errorf("resolve --cwd: %w", err)
		}
	}
	plan, err := handoff.PlanResume(session, handoff.PlanOptions{
		Target:         target,
		ForceNew:       opts.newOnly,
		Dir:            cwd,
		FromRuntimeLog: subject.fromLog,
		BriefPath:      filepath.Join(briefDir, handoff.BriefFileName(session, now)),
		LookPath:       handoffLookPath,
	})
	if err != nil {
		return err
	}

	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	if opts.print {
		if opts.jsonPlan {
			encoder := json.NewEncoder(out)
			encoder.SetIndent("", "  ")
			return encoder.Encode(plan)
		}
		describeHandoffPlan(out, plan)
		return nil
	}
	describeHandoffPlan(errOut, plan)
	if !opts.yes {
		if !handoffIsTerminal() {
			return errors.New("not running in a terminal; pass --yes to launch, or --print to see the command")
		}
		if !confirmHandoff(handoffStdin, errOut) {
			fmt.Fprintln(errOut, "Cancelled.")
			return nil
		}
	}
	if plan.Mode == handoff.ModeNewSession {
		brief, err := subject.brief()
		if err != nil {
			return err
		}
		brief.GeneratedAt = now.UTC()
		if err := handoff.WriteBriefAt(plan.BriefPath, brief); err != nil {
			return err
		}
	}
	code, err := handoffLaunch(plan, handoffStdin, out, errOut)
	if err != nil {
		return fmt.Errorf("start %s: %w", plan.Executable, err)
	}
	if code != 0 {
		// The runtime has already reported whatever went wrong; Beacon passes its status through.
		if code < 0 {
			code = 1
		}
		handoffExit(code)
	}
	return nil
}

func describeHandoffPlan(w io.Writer, plan handoff.Plan) {
	source := plan.Source
	switch plan.Mode {
	case handoff.ModeNative:
		fmt.Fprintf(w, "Reopening %s session %s in %s\n", handoff.RuntimeLabel(source.Harness), source.ID, plan.Dir)
	default:
		fmt.Fprintf(w, "Starting a new %s session from %s session %s (%s)\n",
			handoff.RuntimeLabel(plan.Target), handoff.RuntimeLabel(source.Harness), source.ID, handoff.ReasonText(plan.Reason))
		fmt.Fprintf(w, "  brief:     %s\n", plan.BriefPath)
		fmt.Fprintf(w, "  directory: %s\n", plan.Dir)
	}
	if env := describeHandoffEnv(plan.Env); env != "" {
		fmt.Fprintf(w, "  env:       %s\n", env)
	}
	fmt.Fprintf(w, "  command:   %s\n", shellCommand(append([]string{plan.Executable}, plan.Args...)...))
}

// describeHandoffEnv lists what the runtime's environment differs by, in name order.
func describeHandoffEnv(env map[string]string) string {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		if env[name] == "" {
			parts = append(parts, "unset "+name)
		} else {
			parts = append(parts, name+"="+env[name])
		}
	}
	return strings.Join(parts, ", ")
}

func confirmHandoff(in io.Reader, w io.Writer) bool {
	fmt.Fprint(w, "Continue? [Y/n] ")
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(w)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes":
		return true
	}
	return false
}

func init() {
	handoffCmd.AddCommand(handoffResumeCmd)
	addHandoffStoreFlags(handoffResumeCmd)
	f := handoffResumeCmd.Flags()
	f.StringVar(&handoffResumeOpts.agent, "agent", "", "Runtime to continue in: "+strings.Join(handoff.StartableNames(), ", ")+" (default the session's own)")
	f.BoolVar(&handoffResumeOpts.newOnly, "new", false, "Start a new session from a brief even when the session could be reopened")
	f.StringVar(&handoffResumeOpts.cwd, "cwd", "", "Directory to start the runtime in (default the session's directory)")
	f.BoolVar(&handoffResumeOpts.print, "print", false, "Show what would run without writing a brief or launching anything")
	f.BoolVar(&handoffResumeOpts.yes, "yes", false, "Launch without asking")
	f.BoolVar(&handoffResumeOpts.jsonPlan, "json", false, "With --print, describe the plan as JSON")
	f.StringVar(&handoffOpts.outputDir, "output-dir", "", "Directory to write the brief into (default ~/.beacon/endpoint/handoffs)")
	f.StringVar(&handoffOpts.logPath, "log-path", "", "Runtime JSONL log to fall back to (default the local runtime log)")
}
