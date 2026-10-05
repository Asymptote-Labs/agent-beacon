package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/agentskills"
)

// Agent tools are what onboarding sets up for the person's agents once the install succeeds:
// Beacon Cloud MCP, offered only when Beacon Cloud was chosen, and the Beacon Agent Skills,
// offered whichever destination was chosen. Both start selected in the wizard and are turned
// off there. `beacon mcp connect|disconnect` and `beacon skills install|uninstall` redo or
// undo them later. Installing the skills never touches the network.

// Seams for tests.
var (
	agentToolsHome            = mcpHome
	agentToolsConnectCloudMCP = connectCloudMCP
)

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
	rootCmd.AddCommand(skillsCmd)
	skillsCmd.AddCommand(skillsInstallCmd, skillsUninstallCmd)
	skillsInstallCmd.PreRunE = refuseRoot("beacon skills install")
	skillsUninstallCmd.PreRunE = refuseRoot("beacon skills uninstall")
}

// installAgentSkillsFromOnboarding installs the skills the wizard kept selected. It runs after
// the local install has succeeded, and a failure is a warning: the endpoint works without them.
func installAgentSkillsFromOnboarding(cmd *cobra.Command, decided onboardingOutcome) {
	if !decided.InstallSkills {
		return
	}
	if err := installAgentSkills(cmd.OutOrStdout()); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "beacon: the Beacon agent skills were not installed (%v). Run `beacon skills install` to try again.\n", err)
	}
}

// connectCloudMCPFromOnboarding registers Beacon Cloud MCP in the detected harnesses, as
// `beacon mcp connect --yes` would, once this endpoint forwards to Beacon Cloud. Leaving the
// row selected on the wizard's agent tools screen is the confirmation connect would otherwise
// ask for. It writes only the URL (each harness signs in with OAuth on first use) and never a
// token. A failure is a warning naming the retry.
func connectCloudMCPFromOnboarding(cmd *cobra.Command) {
	fmt.Fprintln(cmd.OutOrStdout())
	err := agentToolsConnectCloudMCP(commandContext(cmd), cmd.OutOrStdout(), cloudMCPRequest{yes: true})
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "beacon: Beacon Cloud MCP was not registered (%v). Run `beacon mcp connect` to try again.\n", err)
	}
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
