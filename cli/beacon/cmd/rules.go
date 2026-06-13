package cmd

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/detect"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
)

var rulesOpts struct {
	userMode   bool
	systemMode bool
	force      bool
	markdown   bool
}

func rulesUserMode() bool {
	if rulesOpts.systemMode {
		return false
	}
	return rulesOpts.userMode
}

var rulesCmd = &cobra.Command{
	Use:   "rules",
	Short: "Manage and author Beacon threat-detection rules",
	Long: `Manage the local threat-rule store (~/.beacon/endpoint/rules) that 'beacon scan'
runs, and author rules.

The detection engine ships in the binary; rules are external data loaded from the store,
so a growing rule corpus never enlarges the binary. A small baseline is built in and used
until you install your own rules.`,
}

var rulesListCmd = &cobra.Command{
	Use:          "list",
	Short:        "List the active threat-detection rules",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		loaded, err := detect.LoadActive(rulesUserMode(), "")
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if len(loaded) == 0 {
			fmt.Fprintln(out, "No rules.")
			return nil
		}
		for _, lr := range loaded {
			fmt.Fprintf(out, "%-32s %-8s %-12s %s\n", lr.Rule.ID, lr.Rule.Severity, lr.Rule.Status, lr.Source)
		}
		fmt.Fprintf(out, "\n%d rule(s). Store: %s\n", len(loaded), detect.StoreDir(rulesUserMode()))
		return nil
	},
}

var rulesAddCmd = &cobra.Command{
	Use:          "add <path>",
	Short:        "Install rule file(s) from a local path into the store",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		installed, err := detect.InstallFiles(rulesUserMode(), args[0], rulesOpts.force)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		for _, in := range installed {
			fmt.Fprintf(out, "installed %s -> %s\n", in.ID, in.Path)
		}
		fmt.Fprintf(out, "%d rule(s) installed.\n", len(installed))
		return nil
	},
}

var rulesRemoveCmd = &cobra.Command{
	Use:          "remove <id>",
	Short:        "Remove a rule from the store by id",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := detect.Remove(rulesUserMode(), args[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "removed %s (%s)\n", args[0], path)
		return nil
	},
}

var rulesPullCmd = &cobra.Command{
	Use:   "pull <url>",
	Short: "Fetch a rule pack from a URL into the store (reaches the network)",
	Long: `Downloads a rule file (.rule.yaml) or a gzipped tarball (.tar.gz/.tgz) of rules
from an explicit URL and installs the valid rules into the store.

This is the only Beacon command that reaches the network, and only when you run it. The
agent never fetches rules on its own. The URL is whatever you supply; there is no hosted
default.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runRulesPull,
}

func runRulesPull(cmd *cobra.Command, args []string) error {
	url := args[0]
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("url must start with http:// or https://")
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Fetching %s (network)…\n", url)

	tmp, err := os.MkdirTemp("", "beacon-rules-pull-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}

	var src string
	switch {
	case strings.HasSuffix(url, ".tar.gz"), strings.HasSuffix(url, ".tgz"):
		if err := extractRuleTarball(resp.Body, tmp); err != nil {
			return err
		}
		src = tmp
	case strings.HasSuffix(url, ".rule.yaml"):
		dest := filepath.Join(tmp, filepath.Base(url))
		if err := writeReaderToFile(resp.Body, dest); err != nil {
			return err
		}
		src = dest
	default:
		return fmt.Errorf("unsupported url: expected a .rule.yaml or .tar.gz/.tgz pack")
	}

	installed, err := detect.InstallFiles(rulesUserMode(), src, rulesOpts.force)
	if err != nil {
		return err
	}
	for _, in := range installed {
		fmt.Fprintf(out, "installed %s -> %s\n", in.ID, in.Path)
	}
	fmt.Fprintf(out, "%d rule(s) installed.\n", len(installed))
	return nil
}

// extractRuleTarball extracts *.rule.yaml entries from a gzipped tarball into dir. Entry
// paths are flattened to their base name; path-traversal entries are rejected.
func extractRuleTarball(r io.Reader, dir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	cleanDir := filepath.Clean(dir)
	tr := tar.NewReader(gz)
	count := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || !strings.HasSuffix(hdr.Name, ".rule.yaml") {
			continue
		}
		// Flatten archive directory structure to a base name, then confirm the joined
		// destination stays within cleanDir before any file operation. The same `dest`
		// value is both validated and written, guarding against archive path traversal
		// ("Zip Slip").
		dest := filepath.Join(cleanDir, filepath.Base(filepath.ToSlash(hdr.Name)))
		if dest != cleanDir && !strings.HasPrefix(dest, cleanDir+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry %q resolves outside the destination directory", hdr.Name)
		}
		if err := writeReaderToFile(io.LimitReader(tr, 1<<20), dest); err != nil {
			return err
		}
		count++
	}
	if count == 0 {
		return fmt.Errorf("tarball contained no .rule.yaml files")
	}
	return nil
}

func writeReaderToFile(r io.Reader, dest string) error {
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	return nil
}

// --- authoring commands (moved from the retired `beacon threatrules` group) ---

var rulesLintCmd = &cobra.Command{
	Use:   "lint [path]",
	Short: "Validate rule files and run their embedded conformance fixtures",
	Long: `Loads a rule file or a directory of *.rule.yaml files, validates each (structure
plus CEL compilation against the event schema), enforces the maturity gate, and runs every
embedded test fixture. Exits non-zero if anything fails. With no path, lints ./rules.`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "rules"
		if len(args) == 1 {
			path = args[0]
		}
		return lintRulesPath(cmd, path)
	},
}

var rulesFieldsCmd = &cobra.Command{
	Use:          "fields",
	Short:        "Print the event fields a rule's CEL expression can match on",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if rulesOpts.markdown {
			fmt.Fprint(cmd.OutOrStdout(), threatrules.RenderFieldsMarkdown())
			return nil
		}
		for _, f := range threatrules.EventFields() {
			fmt.Fprintf(cmd.OutOrStdout(), "e.%-44s %s\n", f.Path, f.Type)
		}
		return nil
	},
}

func loadRuleFiles(path string) ([]*threatrules.Rule, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return threatrules.LoadDir(path)
	}
	rule, err := threatrules.LoadRule(path)
	if err != nil {
		return nil, err
	}
	return []*threatrules.Rule{rule}, nil
}

func lintRulesPath(cmd *cobra.Command, path string) error {
	out := cmd.OutOrStdout()
	rules, err := loadRuleFiles(path)
	if err != nil {
		return fmt.Errorf("load rules: %w", err)
	}
	if len(rules) == 0 {
		return fmt.Errorf("no *.rule.yaml files found under %s", filepath.Clean(path))
	}

	failures, fixtures := 0, 0
	for _, rule := range rules {
		results, err := threatrules.CheckRule(rule)
		if err != nil {
			failures++
			fmt.Fprintf(out, "FAIL %s: %v\n", rule.ID, err)
			continue
		}
		ruleFailed := false
		for _, res := range results {
			fixtures++
			if !res.OK() {
				ruleFailed = true
				failures++
				fmt.Fprintf(out, "FAIL %s\n", res.String())
			}
		}
		if !ruleFailed {
			fmt.Fprintf(out, "ok   %s (%d fixtures)\n", rule.ID, len(results))
		}
	}
	fmt.Fprintf(out, "\n%d rule(s), %d fixture(s), %d failure(s)\n", len(rules), fixtures, failures)
	if failures > 0 {
		return fmt.Errorf("%d failure(s)", failures)
	}
	return nil
}

func init() {
	rulesFieldsCmd.Flags().BoolVar(&rulesOpts.markdown, "markdown", false, "Render as the FIELDS.md reference table")
	for _, c := range []*cobra.Command{rulesListCmd, rulesAddCmd, rulesRemoveCmd, rulesPullCmd} {
		c.Flags().BoolVar(&rulesOpts.userMode, "user", true, "Use per-user endpoint paths")
		c.Flags().BoolVar(&rulesOpts.systemMode, "system", false, "Use system endpoint paths")
	}
	for _, c := range []*cobra.Command{rulesAddCmd, rulesPullCmd} {
		c.Flags().BoolVar(&rulesOpts.force, "force", false, "Overwrite an existing rule with the same id")
	}
	rulesCmd.AddCommand(rulesListCmd, rulesAddCmd, rulesRemoveCmd, rulesPullCmd, rulesLintCmd, rulesFieldsCmd)
	rootCmd.AddCommand(rulesCmd)
}
