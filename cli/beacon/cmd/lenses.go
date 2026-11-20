package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/dashboard"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lensstore"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

var lensesOpts struct {
	userMode   bool
	systemMode bool
	force      bool
	yes        bool
	json       bool
}

// Test seams for the install confirmation.
var (
	lensesIsTTY           = func() bool { return isTerminal(os.Stdin) }
	lensesStdin io.Reader = os.Stdin
)

// lensTrustWarning is what `beacon lenses add` says before installing a lens. The sandbox keeps a
// lens away from Beacon and the dashboard, but browsers have no control for every way a page can
// send data out, so installing a lens is trusting its code with the traces it is shown.
const lensTrustWarning = `A lens sees the full content of every trace it is shown: prompts, responses, command
output and diffs. The dashboard's sandbox keeps it away from Beacon and the dashboard and blocks
ordinary network requests, but it cannot stop every way a browser can send data out (WebRTC,
resource hints). Install only lenses whose code you trust.
`

func confirmLensInstall(errOut io.Writer, src string, manifest asymptoteobserve.LensManifestV1) error {
	if !lensesIsTTY() || isCIEnvironment() {
		return errors.New("a lens is code trusted with the full content of the traces it is shown; pass --yes to install it without a terminal to confirm")
	}
	fmt.Fprint(errOut, lensTrustWarning)
	fmt.Fprintf(errOut, "Install %s (%s v%d) from %s? [y/N] ", manifest.Title, manifest.ID, manifest.Version, src)
	if !confirmNo(lensesStdin) {
		return errors.New("not installed")
	}
	return nil
}

func lensesUserMode() bool {
	return flagsUserMode(lensesOpts.userMode, lensesOpts.systemMode)
}

var lensesCmd = &cobra.Command{
	Use:   "lenses",
	Short: "Manage lenses: single-file views of a trace in the local dashboard",
	Long: `Manage the local lens store (~/.beacon/endpoint/lenses).

A lens is one HTML file that renders one trace. The dashboard ('beacon endpoint dashboard')
shows lenses as tabs on a session's page, runs each in a sandboxed frame with no access to
Beacon or the dashboard and no ordinary network APIs, and hands it the trace through
window.beacon.getTrace(). A lens is code you trust with the traces it is shown. Beacon ships built-in lenses; the
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

A lens sees the full content of every trace it is shown and is trusted code: the sandbox keeps
it away from Beacon and the dashboard, but cannot stop every way a browser can send data out.
Add asks for confirmation on a terminal; without one it needs --yes.

Replacing an installed lens needs a higher manifest version, or --force. A built-in lens's
id cannot be used. The dashboard picks the lens up on its next page load.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		reserved, err := dashboard.BuiltinLensIDs()
		if err != nil {
			return err
		}
		opts := lensstore.InstallOptions{Reserved: reserved, Force: lensesOpts.force}
		if !lensesOpts.yes {
			opts.Confirm = func(manifest asymptoteobserve.LensManifestV1) error {
				return confirmLensInstall(cmd.ErrOrStderr(), args[0], manifest)
			}
		}
		lens, replaced, err := lensstore.Install(lensesStoreDir(), args[0], opts)
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
		if manifest, ok := dashboard.BuiltinLensManifest(id); ok {
			return writeIndentedJSON(cmd.OutOrStdout(), map[string]interface{}{"manifest": manifest, "source": dashboard.LensSourceBuiltin})
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
	lensesAddCmd.Flags().BoolVar(&lensesOpts.yes, "yes", false, "Install without asking; you trust this lens with the traces it is shown")
	lensesCmd.AddCommand(lensesListCmd, lensesAddCmd, lensesRemoveCmd, lensesShowCmd)
	rootCmd.AddCommand(lensesCmd)
}
