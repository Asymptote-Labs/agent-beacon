package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/mcpmanaged"
)

// `beacon mcp managed` pushes administrator-chosen remote MCP servers to every account on the
// machine, for Claude Code and Codex CLI, through each runtime's own machine-wide configuration
// (internal/mcpmanaged). It is for MDM: a recurring root script hands it the complete server
// list, and each run makes the machine match it. People keep every server they add themselves.
// It makes no network request and reads no account token.

// Seams for tests.
var (
	mcpManagedPaths                = mcpmanaged.DefaultPaths
	mcpManagedPrivileged           = lifecycle.HasSystemPrivileges
	mcpManagedStdin      io.Reader = os.Stdin
)

var mcpManagedOpts struct {
	file      string
	harnesses []string
	dryRun    bool
	json      bool
}

var mcpManagedCmd = &cobra.Command{
	Use:   "managed",
	Short: "Push MCP servers to every account on this machine (for MDM)",
	Long: `Push administrator-chosen remote MCP servers to every account on this machine, for Claude
Code and Codex CLI, on macOS and Linux.

Servers go into each runtime's machine-wide configuration, which adds to what people configure
themselves rather than replacing it:

  Claude Code  managedMcpServers in a Beacon-owned managed-settings.d drop-in
               (Claude Code 2.1.259 or later)
  Codex CLI    [mcp_servers.<name>] tables in /etc/codex/config.toml, marked as Beacon's

The server list is JSON:

  {"servers": {"linear": {"url": "https://mcp.linear.app/mcp"},
               "docs":   {"url": "https://mcp.example.com/mcp", "headers": {"X-Team": "eng"}}}}

Only https servers are accepted, with literal header values: Claude Code takes nothing else for
a managed server. Both files are readable by every account on the machine, so never put a
personal credential in a header; use servers that sign in with OAuth or a shared, low-privilege
value.`,
}

var mcpManagedApplyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Make the pushed MCP servers exactly the given list",
	Long: `Make the pushed MCP servers exactly the given list. A server left out of the list is removed
from the machine-wide configuration; servers people added themselves are never touched. Running
it again with the same list changes nothing, so it is safe in a recurring MDM script.`,
	Example: `  sudo beacon mcp managed apply --file /etc/beacon/mcp-servers.json
  printf '%s' "$SERVERS_JSON" | sudo beacon mcp managed apply --file -`,
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	RunE:         runMCPManagedApply,
}

var mcpManagedRemoveCmd = &cobra.Command{
	Use:          "remove",
	Short:        "Remove every MCP server Beacon pushed",
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	RunE:         runMCPManagedRemove,
}

var mcpManagedStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show the MCP servers Beacon pushed",
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	RunE:         runMCPManagedStatus,
}

func init() {
	mcpCmd.AddCommand(mcpManagedCmd)
	mcpManagedCmd.AddCommand(mcpManagedApplyCmd, mcpManagedRemoveCmd, mcpManagedStatusCmd)
	for _, c := range []*cobra.Command{mcpManagedApplyCmd, mcpManagedRemoveCmd, mcpManagedStatusCmd} {
		c.Flags().StringSliceVar(&mcpManagedOpts.harnesses, "harness", nil, "Runtimes to act on: claude, codex (default: both)")
		c.Flags().BoolVar(&mcpManagedOpts.json, "json", false, "Print the result as JSON")
	}
	for _, c := range []*cobra.Command{mcpManagedApplyCmd, mcpManagedRemoveCmd} {
		c.Flags().BoolVar(&mcpManagedOpts.dryRun, "dry-run", false, "Show what would change without writing anything")
	}
	mcpManagedApplyCmd.Flags().StringVar(&mcpManagedOpts.file, "file", "", "JSON server list to push, or - for standard input (required)")
}

func mcpManagedRuntimes(names []string) ([]string, error) {
	var out []string
	for _, raw := range names {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "claude", "claude-code", "claude_code":
			out = append(out, mcpmanaged.RuntimeClaude)
		case "codex", "codex-cli", "codex_cli":
			out = append(out, mcpmanaged.RuntimeCodex)
		case "":
		default:
			return nil, fmt.Errorf("unsupported --harness %q; managed MCP servers are pushed to claude and codex", raw)
		}
	}
	return out, nil
}

func mcpManagedOptions(write bool) (mcpmanaged.Options, error) {
	paths, err := mcpManagedPaths()
	if err != nil {
		return mcpmanaged.Options{}, err
	}
	runtimes, err := mcpManagedRuntimes(mcpManagedOpts.harnesses)
	if err != nil {
		return mcpmanaged.Options{}, err
	}
	if write && !mcpManagedOpts.dryRun && !mcpManagedPrivileged() {
		return mcpmanaged.Options{}, fmt.Errorf("pushing MCP servers writes machine-wide configuration; %s", lifecycle.SystemPrivilegeHint())
	}
	return mcpmanaged.Options{Paths: paths, Runtimes: runtimes, DryRun: mcpManagedOpts.dryRun}, nil
}

func readMCPManagedDeclaration(path string) (mcpmanaged.Declaration, error) {
	if path == "" {
		return mcpmanaged.Declaration{}, errors.New("--file is required: a JSON server list, or - for standard input")
	}
	const limit = 1 << 20
	var r io.Reader
	if path == "-" {
		r = mcpManagedStdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return mcpmanaged.Declaration{}, err
		}
		defer f.Close()
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return mcpmanaged.Declaration{}, err
	}
	if len(data) > limit {
		return mcpmanaged.Declaration{}, errors.New("the server list is larger than 1 MiB")
	}
	return mcpmanaged.ParseDeclaration(data)
}

func runMCPManagedApply(cmd *cobra.Command, args []string) error {
	d, err := readMCPManagedDeclaration(mcpManagedOpts.file)
	if err != nil {
		return err
	}
	opts, err := mcpManagedOptions(true)
	if err != nil {
		return err
	}
	return printMCPManagedResults(cmd, mcpmanaged.Apply(opts, d))
}

func runMCPManagedRemove(cmd *cobra.Command, args []string) error {
	opts, err := mcpManagedOptions(true)
	if err != nil {
		return err
	}
	return printMCPManagedResults(cmd, mcpmanaged.Remove(opts))
}

type mcpManagedResultJSON struct {
	mcpmanaged.Result
	Error string `json:"error,omitempty"`
}

// printMCPManagedResults reports each runtime and fails when any runtime failed, so an MDM
// script's exit status says whether the machine matches the list. A same-named server Beacon did
// not write is reported but is not a failure: that person or tool's entry wins.
func printMCPManagedResults(cmd *cobra.Command, results []mcpmanaged.Result) error {
	out := cmd.OutOrStdout()
	var failed []string
	for _, r := range results {
		if r.Err != nil {
			failed = append(failed, r.Runtime)
		}
	}
	if mcpManagedOpts.json {
		rows := make([]mcpManagedResultJSON, len(results))
		for i, r := range results {
			rows[i] = mcpManagedResultJSON{Result: r}
			if r.Err != nil {
				rows[i].Error = r.Err.Error()
			}
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(map[string]any{"dry_run": mcpManagedOpts.dryRun, "results": rows}); err != nil {
			return err
		}
	} else {
		if mcpManagedOpts.dryRun {
			fmt.Fprintln(out, "Dry run: nothing was written.")
		}
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "RUNTIME\tACTION\tSERVERS\tPATH")
		for _, r := range results {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", mcpManagedDisplay(r.Runtime), r.Action, mcpManagedList(r.Servers), r.Path)
		}
		tw.Flush()
		for _, r := range results {
			if r.Detail != "" {
				fmt.Fprintf(out, "%s: %s\n", mcpManagedDisplay(r.Runtime), r.Detail)
			}
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not update %s", strings.Join(failed, ", "))
	}
	return nil
}

func runMCPManagedStatus(cmd *cobra.Command, args []string) error {
	opts, err := mcpManagedOptions(false)
	if err != nil {
		return err
	}
	statuses := mcpmanaged.Status(opts)
	out := cmd.OutOrStdout()
	if mcpManagedOpts.json {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"runtimes": statuses})
	}
	for i, st := range statuses {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "%s (%s)\n", mcpManagedDisplay(st.Runtime), st.Path)
		if st.Error != "" {
			fmt.Fprintf(out, "  error: %s\n", st.Error)
			continue
		}
		if len(st.Servers) == 0 {
			fmt.Fprintln(out, "  no servers pushed by Beacon")
		}
		for _, s := range st.Servers {
			fmt.Fprintf(out, "  %s  %s\n", s.Name, s.URL)
		}
		for _, w := range st.Warnings {
			fmt.Fprintf(out, "  warning: %s\n", w)
		}
	}
	return nil
}

func mcpManagedDisplay(rt string) string {
	switch rt {
	case mcpmanaged.RuntimeClaude:
		return "Claude Code"
	case mcpmanaged.RuntimeCodex:
		return "Codex CLI"
	}
	return rt
}

func mcpManagedList(names []string) string {
	if len(names) == 0 {
		return "-"
	}
	return strings.Join(names, ",")
}
