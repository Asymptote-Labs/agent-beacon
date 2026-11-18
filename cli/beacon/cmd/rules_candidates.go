package cmd

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/detect"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/concepts"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
)

var rulesCandidatesOpts struct {
	userMode   bool
	systemMode bool
	logPath    string
	rulesDir   string
	jsonOutput bool
}

// candidatesReport is what `rules candidates` prints.
type candidatesReport struct {
	Catalogs   []string             `json:"catalogs"`
	FilesRead  int                  `json:"files_read"`
	Candidates []concepts.Candidate `json:"candidates"`
}

var rulesCandidatesCmd = &cobra.Command{
	Use:   "candidates",
	Short: "List files agents read that look like credentials and are in no catalog",
	Long: `Reads the local telemetry log for the files agents read, with a file tool or by a shell
command, and lists the ones whose name looks like a credential's and that the active rules'
credential catalog does not have. A catalog is a reviewed list, so it is as complete as what
its reviewers have seen; this shows what your agents read that it has not.

The names are a guess, for a person to review: a file listed here may hold no credential,
and one that does may not be listed. To make the rules see one, add a store for it to a
catalog of your rule pack.

Nothing leaves the machine. The output holds file paths from your log.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		userMode := rulesCandidatesOpts.userMode && !rulesCandidatesOpts.systemMode
		loaded, skipped, err := detect.LoadActiveSkipping(userMode, strings.TrimSpace(rulesCandidatesOpts.rulesDir))
		if err != nil {
			return fmt.Errorf("load rules: %w", err)
		}
		detect.WarnSkipped(cmd.ErrOrStderr(), skipped)
		var catalogs []*concepts.Catalog
		report := candidatesReport{}
		for _, lr := range loaded {
			for _, catalog := range lr.Rule.Definitions().Catalogs() {
				if !slices.Contains(catalogs, catalog) {
					catalogs = append(catalogs, catalog)
					if !slices.Contains(report.Catalogs, catalog.Name) {
						report.Catalogs = append(report.Catalogs, catalog.Name)
					}
				}
			}
		}
		if len(catalogs) == 0 {
			return fmt.Errorf("the active rules ship no catalog")
		}

		runtimeLog := lifecycle.ResolveRuntimeLog(userMode, rulesCandidatesOpts.logPath)
		paths, err := pathsRead(runtimeLog.EffectiveLogPath)
		if err != nil {
			return fmt.Errorf("read telemetry %s: %w", runtimeLog.EffectiveLogPath, err)
		}
		report.FilesRead = len(paths)
		// A file is a candidate when no active catalog has it: the baseline's catalog and an
		// installed pack's may both be active, and one may know what the other does not.
		paths = slices.DeleteFunc(paths, func(path string) bool {
			return slices.ContainsFunc(catalogs[1:], func(c *concepts.Catalog) bool { _, ok := c.Lookup(path); return ok })
		})
		report.Candidates = catalogs[0].Candidates(paths)

		out := cmd.OutOrStdout()
		if rulesCandidatesOpts.jsonOutput {
			enc := json.NewEncoder(out)
			enc.SetIndent("", " ")
			return enc.Encode(report)
		}
		for _, candidate := range report.Candidates {
			fmt.Fprintf(out, "%4d  %-32s %s\n", candidate.Reads, candidate.Name, candidate.Example)
		}
		if len(report.Candidates) > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "%d candidate(s) for %s among %d file(s) read in %s\n", len(report.Candidates), strings.Join(report.Catalogs, ", "), report.FilesRead, runtimeLog.EffectiveLogPath)
		return nil
	},
}

// pathsRead returns the distinct paths a log's events read: the path of a file.read, and the
// files each command of a command line takes in.
func pathsRead(logPath string) ([]string, error) {
	seen := map[string]bool{}
	var paths []string
	add := func(path string) {
		if path != "" && !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	err := dashboard.StreamEvents(logPath, func(e schema.Event) error {
		switch {
		case e.Event.Action == "file.read" && e.File != nil:
			add(e.File.Path)
		case e.Command != nil && e.Command.Command != "":
			for _, command := range threatrules.ParsedCommands(e.Command.Command) {
				for _, read := range command.FileReads {
					add(read.Path)
				}
			}
		}
		return nil
	})
	return paths, err
}

func init() {
	rulesCandidatesCmd.Flags().BoolVar(&rulesCandidatesOpts.userMode, "user", true, "Use per-user endpoint paths")
	rulesCandidatesCmd.Flags().BoolVar(&rulesCandidatesOpts.systemMode, "system", false, "Use system endpoint paths")
	rulesCandidatesCmd.Flags().StringVar(&rulesCandidatesOpts.logPath, "log-path", "", "Runtime JSONL log path (default: resolved from config)")
	rulesCandidatesCmd.Flags().StringVar(&rulesCandidatesOpts.rulesDir, "rules", "", "Rule directory whose catalog to check against, instead of the store and baseline")
	rulesCandidatesCmd.Flags().BoolVar(&rulesCandidatesOpts.jsonOutput, "json", false, "Output as JSON")
	rulesCmd.AddCommand(rulesCandidatesCmd)
}
