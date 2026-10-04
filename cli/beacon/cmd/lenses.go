package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lensstore"
)

var lensesOpts struct {
	userMode   bool
	systemMode bool
	force      bool
	json       bool
}

func lensesUserMode() bool {
	if lensesOpts.systemMode {
		return false
	}
	return lensesOpts.userMode
}

var lensesCmd = &cobra.Command{
	Use:   "lenses",
	Short: "Manage lenses: single-file views of a trace in the local dashboard",
	Long: `Manage the local lens store (~/.beacon/endpoint/lenses).

A lens is one HTML file that renders one trace. The dashboard ('beacon endpoint dashboard')
shows lenses as tabs on a session's page, runs each in a sandboxed frame with no network,
and hands it the trace through window.beacon.getTrace(). Beacon ships built-in lenses; the
store holds the ones you add. The format is spec/lenses/SPEC.md.`,
}

// lensListEntry is one row of 'beacon lenses list --json'.
type lensListEntry struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Version     int    `json:"version"`
	Source      string `json:"source"`
	Path        string `json:"path,omitempty"`
}

var lensesListCmd = &cobra.Command{
	Use:          "list",
	Short:        "List built-in and installed lenses",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		builtins, err := dashboard.BuiltinLenses()
		if err != nil {
			return err
		}
		dir := lensesStoreDir()
		stored, problems, err := lensstore.List(dir)
		if err != nil {
			return err
		}
		entries := make([]lensListEntry, 0, len(builtins)+len(stored))
		for _, l := range builtins {
			entries = append(entries, lensListEntry{ID: l.ID, Title: l.Title, Description: l.Description, Version: l.Version, Source: l.Source})
		}
		for _, l := range stored {
			entries = append(entries, lensListEntry{ID: l.Manifest.ID, Title: l.Manifest.Title, Description: l.Manifest.Description, Version: l.Manifest.Version, Source: dashboard.LensSourceStore, Path: l.Path})
		}
		for _, p := range problems {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s is not a valid lens and is ignored: %v\n", p.Path, p.Err)
		}
		out := cmd.OutOrStdout()
		if lensesOpts.json {
			return writeIndentedJSON(out, map[string]interface{}{"lenses": entries, "store": dir})
		}
		for _, e := range entries {
			fmt.Fprintf(out, "%-24s v%-4d %-8s %s\n", e.ID, e.Version, e.Source, e.Title)
		}
		fmt.Fprintf(out, "\n%d lens(es). Store: %s\n", len(entries), dir)
		return nil
	},
}

var lensesAddCmd = &cobra.Command{
	Use:   "add <file>",
	Short: "Install a lens file into the store",
	Long: `Check a lens file against the spec and copy it into the store under its manifest id.

Replacing an installed lens needs a higher manifest version, or --force. A built-in lens's
id cannot be used. The dashboard picks the lens up on its next page load.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		reserved, err := dashboard.BuiltinLensIDs()
		if err != nil {
			return err
		}
		lens, replaced, err := lensstore.Install(lensesStoreDir(), args[0], lensstore.InstallOptions{Reserved: reserved, Force: lensesOpts.force})
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		switch {
		case replaced > 0:
			fmt.Fprintf(out, "updated %s v%d -> v%d (%s)\n", lens.Manifest.ID, replaced, lens.Manifest.Version, lens.Path)
		case replaced < 0:
			fmt.Fprintf(out, "replaced an invalid file with %s v%d (%s)\n", lens.Manifest.ID, lens.Manifest.Version, lens.Path)
		default:
			fmt.Fprintf(out, "installed %s v%d (%s)\n", lens.Manifest.ID, lens.Manifest.Version, lens.Path)
		}
		return nil
	},
}

var lensesRemoveCmd = &cobra.Command{
	Use:          "remove <id>",
	Short:        "Remove an installed lens by id",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := lensstore.Remove(lensesStoreDir(), args[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "removed %s (%s)\n", args[0], path)
		return nil
	},
}

var lensesShowCmd = &cobra.Command{
	Use:          "show <id>",
	Short:        "Print a lens's manifest and where it comes from",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		builtins, err := dashboard.BuiltinLenses()
		if err != nil {
			return err
		}
		for _, l := range builtins {
			if l.ID == id {
				return writeIndentedJSON(cmd.OutOrStdout(), map[string]interface{}{"manifest": l, "source": l.Source})
			}
		}
		_, manifest, err := lensstore.Read(lensesStoreDir(), id)
		if err != nil {
			return fmt.Errorf("no lens %q: %w", id, err)
		}
		path, _ := lensstore.PathFor(lensesStoreDir(), id)
		return writeIndentedJSON(cmd.OutOrStdout(), map[string]interface{}{"manifest": manifest, "source": dashboard.LensSourceStore, "path": path})
	},
}

func lensesStoreDir() string {
	return lensstore.Dir(lensesUserMode())
}

func init() {
	for _, c := range []*cobra.Command{lensesListCmd, lensesAddCmd, lensesRemoveCmd, lensesShowCmd} {
		c.Flags().BoolVar(&lensesOpts.userMode, "user", true, "Use per-user endpoint paths")
		c.Flags().BoolVar(&lensesOpts.systemMode, "system", false, "Use system endpoint paths")
	}
	lensesListCmd.Flags().BoolVar(&lensesOpts.json, "json", false, "Print JSON")
	lensesAddCmd.Flags().BoolVar(&lensesOpts.force, "force", false, "Replace an installed lens even when the new version is not higher")
	lensesCmd.AddCommand(lensesListCmd, lensesAddCmd, lensesRemoveCmd, lensesShowCmd)
	rootCmd.AddCommand(lensesCmd)
}
