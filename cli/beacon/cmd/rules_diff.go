package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/detect"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
)

var rulesDiffOpts struct {
	logPath    string
	jsonOutput bool
	summary    bool
	exitCode   bool
}

var rulesSnapshotCmd = &cobra.Command{
	Use:   "snapshot [path]",
	Short: "Record what a rule pack says about its fixture events, for `rules diff`",
	Long: `Evaluates every rule of the pack, and every step of its correlation rules, on each event
of the pack's fixtures and of its predicates' fixtures, matches the correlation rules over
each fixture as a scan does, and prints which of them match, and which could not be
evaluated, as JSON. With --log-path the events of that telemetry log are asked about as
well, and the correlation rules matched over the whole log.

A snapshot is the "before" of a change that two directories cannot show: a change to
Beacon itself. Take one with the old binary, then compare with the new one:

  beacon rules snapshot rules > before.json     # old beacon
  beacon rules diff before.json rules           # new beacon

A snapshot taken with --log-path holds the commands and paths of that log as labels.
With no path, snapshots ./rules.`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "rules"
		if len(args) == 1 {
			path = args[0]
		}
		rules, err := loadPackForDiff(cmd, path)
		if err != nil {
			return err
		}
		sequences, err := diffSequences(rules)
		if err != nil {
			return err
		}
		snapshot, err := threatrules.TakeSnapshot(rules, sequences)
		if err != nil {
			return err
		}
		warnSnapshotErrors(cmd.ErrOrStderr(), path, snapshot)
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", " ")
		return enc.Encode(snapshot)
	},
}

var rulesDiffCmd = &cobra.Command{
	Use:   "diff <before> <after>",
	Short: "Show which rule verdicts a change to a rule pack, or to Beacon, changes",
	Long: `Compares what two rule packs say about the same events, as a scan says it: each
single-event rule and each step of each correlation rule on each event, and what each
correlation rule finds over each fixture and over the log. For every one whose answer
changed it lists the events (for a correlation rule, its findings) it matches now and did
not (+), the ones it matched and does not (-), the ones it can no longer be evaluated on
(!, most often an expression stopped at the cost limit: its verdict there is unknown) and
the ones it can be again (?).

A rule's own fixtures show what its author thought of. A change to a predicate or a catalog
that many rules call, or to how Beacon reads a command line, reaches rules whose fixtures say
nothing about it; this shows all of it.

Each side is a pack directory, a rule file, or a snapshot written by "beacon rules
snapshot". The events asked about are the fixture events of both packs and of their
predicates and, with --log-path, the events of that telemetry log. Two sides are compared
on the events, and fixtures, both were asked about.

  beacon rules diff ../main/rules rules                       # a change to the rules
  beacon rules diff before.json rules                         # a change to Beacon
  beacon rules diff ../main/rules rules --log-path beacon.jsonl

Exits 0 whether or not anything changed, unless --exit-code is given.`,
	Args:         cobra.ExactArgs(2),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return diffRulesPaths(cmd, args[0], args[1])
	},
}

// diffSide is one side of a diff: a pack's rules, or a snapshot taken earlier.
type diffSide struct {
	path     string
	rules    []*threatrules.Rule
	snapshot *threatrules.Snapshot
}

func loadDiffSide(cmd *cobra.Command, path string) (diffSide, error) {
	side := diffSide{path: path}
	info, err := os.Stat(path)
	if err != nil {
		return side, err
	}
	if !info.IsDir() && strings.HasSuffix(path, ".json") {
		data, err := os.ReadFile(path)
		if err != nil {
			return side, err
		}
		side.snapshot = &threatrules.Snapshot{}
		if err := json.Unmarshal(data, side.snapshot); err != nil {
			return side, fmt.Errorf("%s: not a snapshot written by `beacon rules snapshot`: %w", path, err)
		}
		return side, nil
	}
	side.rules, err = loadPackForDiff(cmd, path)
	return side, err
}

func loadPackForDiff(cmd *cobra.Command, path string) ([]*threatrules.Rule, error) {
	rules, skipped, err := loadRuleFiles(path)
	if err != nil {
		return nil, err
	}
	detect.WarnSkipped(cmd.ErrOrStderr(), skipped)
	if len(rules) == 0 {
		return nil, fmt.Errorf("no rule files (*.rule.yaml) found in %s", path)
	}
	return rules, nil
}

// diffSequences returns what the given packs are asked about: their fixtures and their
// predicates' and, with --log-path, that log, in the order a scan reads it.
func diffSequences(packs ...[]*threatrules.Rule) ([]threatrules.EventSequence, error) {
	var all []*threatrules.Rule
	for _, rules := range packs {
		all = append(all, rules...)
	}
	sequences := threatrules.FixtureSequences(all)
	if path := strings.TrimSpace(rulesDiffOpts.logPath); path != "" {
		var events []asymptoteobserve.Event
		err := dashboard.StreamEvents(path, func(e schema.Event) error {
			events = append(events, e)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("read telemetry %s: %w", path, err)
		}
		threatrules.SortEvents(events)
		sequences = append(sequences, threatrules.EventSequence{Label: path, Events: events})
	}
	return sequences, nil
}

func warnSnapshotErrors(w io.Writer, path string, s *threatrules.Snapshot) {
	if s.Errors > 0 {
		fmt.Fprintf(w, "warning: %s: %d evaluation(s) failed; the first: %s\n", path, s.Errors, s.FirstError)
	}
}

func diffRulesPaths(cmd *cobra.Command, beforePath, afterPath string) error {
	before, err := loadDiffSide(cmd, beforePath)
	if err != nil {
		return err
	}
	after, err := loadDiffSide(cmd, afterPath)
	if err != nil {
		return err
	}
	sequences, err := diffSequences(before.rules, after.rules)
	if err != nil {
		return err
	}
	for _, side := range []*diffSide{&before, &after} {
		if side.snapshot == nil {
			if side.snapshot, err = threatrules.TakeSnapshot(side.rules, sequences); err != nil {
				return fmt.Errorf("%s: %w", side.path, err)
			}
		}
		warnSnapshotErrors(cmd.ErrOrStderr(), side.path, side.snapshot)
	}
	diff := before.snapshot.Diff(after.snapshot)

	out := cmd.OutOrStdout()
	if rulesDiffOpts.jsonOutput {
		enc := json.NewEncoder(out)
		enc.SetIndent("", " ")
		if err := enc.Encode(diff); err != nil {
			return err
		}
	} else {
		printSnapshotDiff(out, diff)
	}
	if rulesDiffOpts.exitCode && diff.Changed() {
		return fmt.Errorf("%d rule(s) or step(s) changed", len(diff.Changes))
	}
	return nil
}

func printSnapshotDiff(out io.Writer, diff threatrules.SnapshotDiff) {
	for _, c := range diff.Changes {
		note := ""
		switch {
		case c.Added:
			note = " (new)"
		case c.Removed:
			note = " (removed)"
		}
		fmt.Fprintf(out, "%s%s: +%d -%d", c.Matcher, note, len(c.Gained), len(c.Lost))
		if len(c.Failing) > 0 || len(c.Recovered) > 0 {
			fmt.Fprintf(out, " !%d ?%d", len(c.Failing), len(c.Recovered))
		}
		fmt.Fprintln(out)
		if rulesDiffOpts.summary {
			continue
		}
		printRefs(out, "+", c.Gained)
		printRefs(out, "-", c.Lost)
		printRefs(out, "!", c.Failing)
		printRefs(out, "?", c.Recovered)
	}
	if len(diff.Changes) > 0 {
		fmt.Fprintln(out)
	}
	fmt.Fprintf(out, "%d rule(s) or step(s) changed over %d event(s)", len(diff.Changes), diff.Compared)
	if diff.Sequences > 0 {
		fmt.Fprintf(out, " and %d fixture(s) or log(s)", diff.Sequences)
	}
	if diff.OnlyBefore > 0 || diff.OnlyAfter > 0 {
		fmt.Fprintf(out, "; %d event(s) only before and %d only after were not compared", diff.OnlyBefore, diff.OnlyAfter)
	}
	fmt.Fprintln(out)
}

// printRefs lists the events a matcher gained, lost, or fails or no longer fails on, sorted by
// label. Events that share a label (a long command line is cut) are listed once with their
// number.
func printRefs(out io.Writer, sign string, refs []threatrules.EventRef) {
	for i := 0; i < len(refs); {
		n := 1
		for i+n < len(refs) && refs[i+n].Label == refs[i].Label {
			n++
		}
		if n > 1 {
			fmt.Fprintf(out, "  %s %s (%d events)\n", sign, refs[i].Label, n)
		} else {
			fmt.Fprintf(out, "  %s %s\n", sign, refs[i].Label)
		}
		i += n
	}
}

func init() {
	for _, c := range []*cobra.Command{rulesSnapshotCmd, rulesDiffCmd} {
		c.Flags().StringVar(&rulesDiffOpts.logPath, "log-path", "", "Also ask about the events of this telemetry JSONL log")
	}
	rulesDiffCmd.Flags().BoolVar(&rulesDiffOpts.jsonOutput, "json", false, "Output the changes as JSON")
	rulesDiffCmd.Flags().BoolVar(&rulesDiffOpts.summary, "summary", false, "Print the counts per rule or step, without the events")
	rulesDiffCmd.Flags().BoolVar(&rulesDiffOpts.exitCode, "exit-code", false, "Exit non-zero when a verdict changed")
	rulesCmd.AddCommand(rulesSnapshotCmd, rulesDiffCmd)
}
