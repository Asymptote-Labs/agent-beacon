package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/agentskills"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/mcpconnect"
)

// Agent tools are what onboarding sets up for the person's agents, whichever destination they
// chose: the local Beacon MCP server registered in each detected harness, and the Beacon Agent
// Skills installed user-wide. Both are selected by default in the wizard and turned off there;
// `beacon mcp install|uninstall` and `beacon skills install|uninstall` redo or undo them later.
// Neither touches the network.

// Seams for tests.
var (
	agentToolsHome       = mcpHome
	agentToolsExecutable = beaconExecutable
)

var mcpInstallCmd = &cobra.Command{
	Use:          "install",
	Short:        "Register the local Beacon MCP server in detected harnesses",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return installLocalMCP(cmd.OutOrStdout(), cmd)
	},
}

var mcpUninstallCmd = &cobra.Command{
	Use:          "uninstall",
	Short:        "Remove the local Beacon MCP server entries Beacon registered",
	SilenceUsage: true,
	RunE:         runMCPUninstall,
}

var skillsCmd = &cobra.Command{
	Use:   "skills",
	Short: "Install the Beacon Agent Skills for every project",
	Long: `Install the Beacon Agent Skills for every project on this machine.

The skills go to ~/.agents/skills, which Codex CLI, Gemini CLI, OpenCode, Pi and other runtimes
read, and to ~/.claude/skills when Claude Code is set up. A skill directory of the same name that
Beacon did not write is left alone.`,
}

var skillsInstallCmd = &cobra.Command{
	Use:          "install",
	Short:        "Install or update the Beacon Agent Skills",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return installAgentSkills(cmd.OutOrStdout())
	},
}

var skillsUninstallCmd = &cobra.Command{
	Use:          "uninstall",
	Short:        "Remove the Beacon Agent Skills Beacon installed",
	SilenceUsage: true,
	RunE:         runSkillsUninstall,
}

func init() {
	mcpCmd.AddCommand(mcpInstallCmd, mcpUninstallCmd)
	mcpInstallCmd.Flags().StringSliceVar(&mcpConnectOpts.harnesses, "harness", nil, "Harness to configure (repeatable; default: every detected harness)")
	rootCmd.AddCommand(skillsCmd)
	skillsCmd.AddCommand(skillsInstallCmd, skillsUninstallCmd)
	mcpInstallCmd.PreRunE = refuseRoot("beacon mcp install")
	mcpUninstallCmd.PreRunE = refuseRoot("beacon mcp uninstall")
	skillsInstallCmd.PreRunE = refuseRoot("beacon skills install")
	skillsUninstallCmd.PreRunE = refuseRoot("beacon skills uninstall")
}

func localMCPOptions(home string) mcpconnect.LocalOptions {
	return mcpconnect.LocalOptions{Home: home, Command: agentToolsExecutable(), LookPath: mcpLookPath, Run: mcpRunCLI}
}

// installAgentTools carries out the agent tools the wizard kept selected. It runs after the local
// install has succeeded, and a failure is a warning: the endpoint works without them.
func installAgentTools(cmd *cobra.Command, decided onboardingOutcome) {
	if !decided.InstallMCP && !decided.InstallSkills {
		return
	}
	out := cmd.OutOrStdout()
	if decided.InstallMCP {
		if err := installLocalMCP(out, cmd); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "beacon: the Beacon MCP server was not registered (%v). Run `beacon mcp install` to try again.\n", err)
		}
	}
	if decided.InstallSkills {
		if err := installAgentSkills(out); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "beacon: the Beacon agent skills were not installed (%v). Run `beacon skills install` to try again.\n", err)
		}
	}
}

func installLocalMCP(out io.Writer, cmd *cobra.Command) error {
	home, err := agentToolsHome()
	if err != nil {
		return err
	}
	targets, _, err := resolveMCPTargets(mcpConnectOpts.harnesses)
	if err != nil {
		return err
	}
	var local []mcpconnect.Target
	for _, t := range targets {
		if t.LocalSupported() {
			local = append(local, t)
		}
	}
	if len(local) == 0 {
		fmt.Fprintln(out, "Beacon MCP server: no harness Beacon can register it in was detected.")
		return nil
	}
	items, err := mcpconnect.InstallLocal(commandContext(cmd), localMCPOptions(home), local)
	if err != nil {
		return err
	}
	var failed int
	for _, it := range items {
		where := displayPath(home, it.Path)
		switch {
		case it.Err != nil:
			failed++
			fmt.Fprintf(out, "Beacon MCP server: %s not registered: %v\n", it.Target.DisplayName, it.Err)
		case it.Action == mcpconnect.ActionAdd:
			fmt.Fprintf(out, "Beacon MCP server: registered for %s (%s)\n", it.Target.DisplayName, where)
		case it.Action == mcpconnect.ActionPresent:
			fmt.Fprintf(out, "Beacon MCP server: %s already has a %q entry; left as is\n", it.Target.DisplayName, mcpconnect.LocalServerName)
		default:
			fmt.Fprintf(out, "Beacon MCP server: %s skipped: %s\n", it.Target.DisplayName, it.Detail)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d harness(es) could not be configured", failed)
	}
	return nil
}

func runMCPUninstall(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()
	home, err := agentToolsHome()
	if err != nil {
		return err
	}
	items, err := mcpconnect.UninstallLocal(commandContext(cmd), localMCPOptions(home))
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintln(out, "No local Beacon MCP server entries written by Beacon.")
		return nil
	}
	var failed int
	for _, it := range items {
		where := displayPath(home, it.Path)
		switch {
		case it.Err != nil:
			failed++
			fmt.Fprintf(out, "%s: %v\n", it.Target.DisplayName, it.Err)
		case it.Action == mcpconnect.ActionRemove:
			fmt.Fprintf(out, "Removed from %s (%s)\n", it.Target.DisplayName, where)
		default:
			fmt.Fprintf(out, "%s: %s\n", it.Target.DisplayName, it.Detail)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d entr(ies) could not be removed", failed)
	}
	return nil
}

func installAgentSkills(out io.Writer) error {
	home, err := agentToolsHome()
	if err != nil {
		return err
	}
	return reportSkills(out, home, agentskills.Install(agentskills.Roots(home)), "installed in")
}

func runSkillsUninstall(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()
	home, err := agentToolsHome()
	if err != nil {
		return err
	}
	results := agentskills.Uninstall(agentskills.Roots(home))
	if len(results) == 0 {
		fmt.Fprintln(out, "No Beacon agent skills installed by Beacon.")
		return nil
	}
	return reportSkills(out, home, results, "removed from")
}

// refreshAgentSkills updates skills Beacon already installed to this binary's copies, so an
// upgrade never leaves an agent following instructions for commands that changed. It installs
// nothing new and stays quiet unless something failed.
func refreshAgentSkills(errOut io.Writer) {
	if !endpointUserMode() || onboardingIsRoot() {
		return
	}
	home, err := agentToolsHome()
	if err != nil {
		return
	}
	for _, r := range agentskills.Refresh(agentskills.Roots(home)) {
		if r.Err != nil {
			fmt.Fprintf(errOut, "beacon: could not update the %s skill in %s: %v\n", r.Skill, displayPath(home, r.Root), r.Err)
		}
	}
}

func reportSkills(out io.Writer, home string, results []agentskills.Result, verb string) error {
	type rootSummary struct{ written, current, skipped []string }
	byRoot := map[string]*rootSummary{}
	var roots []string
	var failed int
	for _, r := range results {
		s, ok := byRoot[r.Root]
		if !ok {
			s = &rootSummary{}
			byRoot[r.Root] = s
			roots = append(roots, r.Root)
		}
		switch {
		case r.Err != nil:
			failed++
			fmt.Fprintf(out, "Beacon agent skills: %s in %s: %v\n", r.Skill, displayPath(home, r.Root), r.Err)
		case r.Action == agentskills.ActionAdd, r.Action == agentskills.ActionUpdate, r.Action == agentskills.ActionRemove:
			s.written = append(s.written, r.Skill)
		case r.Action == agentskills.ActionPresent:
			s.current = append(s.current, r.Skill)
		case r.Action == agentskills.ActionSkip:
			s.skipped = append(s.skipped, r.Skill)
		}
	}
	for _, root := range roots {
		s := byRoot[root]
		where := displayPath(home, root)
		if len(s.written) > 0 {
			fmt.Fprintf(out, "Beacon agent skills: %d %s %s\n", len(s.written), verb, where)
		}
		if len(s.current) > 0 {
			fmt.Fprintf(out, "Beacon agent skills: %d already current in %s\n", len(s.current), where)
		}
		for _, name := range s.skipped {
			fmt.Fprintf(out, "Beacon agent skills: %s in %s was not written by Beacon; left as is\n", name, where)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d skill(s) could not be written", failed)
	}
	return nil
}

// refuseRoot keeps root from writing into its own home on behalf of a person.
func refuseRoot(command string) func(*cobra.Command, []string) error {
	return func(*cobra.Command, []string) error {
		if mcpIsRoot() {
			return fmt.Errorf("%s configures one person's agents; run it as that user, not as root", command)
		}
		return nil
	}
}
