package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/agentskills"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/onboarding"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestOnboardingOffersAgentToolsWhicheverDestination(t *testing.T) {
	for _, tc := range []struct {
		destination   string
		mcp, skills   bool
		wantMCPAnswer string
		wantSkills    string
	}{
		{onboarding.DestinationLocal, true, true, onboarding.AgentToolInstalled, onboarding.AgentToolInstalled},
		{onboarding.DestinationAsymptote, true, false, onboarding.AgentToolInstalled, onboarding.AgentToolDeclined},
		{onboarding.DestinationLocal, false, false, onboarding.AgentToolDeclined, onboarding.AgentToolDeclined},
	} {
		h := newOnboardingHarness(t)
		var offered bool
		onboardingRunWizard = func(_ io.Reader, _ io.Writer, opts onboarding.WizardOptions) (onboarding.WizardResult, error) {
			offered = opts.OfferAgentTools
			return onboarding.WizardResult{
				Completed:     true,
				Destination:   tc.destination,
				PrivacyMode:   "standard",
				InstallMCP:    tc.mcp,
				InstallSkills: tc.skills,
			}, nil
		}
		outcome, err := maybeRunOnboarding(h.cmd)
		if err != nil {
			t.Fatal(err)
		}
		if !offered {
			t.Fatalf("%s: the wizard was not offered the agent tools", tc.destination)
		}
		if outcome.InstallMCP != tc.mcp || outcome.InstallSkills != tc.skills {
			t.Fatalf("%s: outcome = %+v", tc.destination, outcome)
		}
		if err := outcome.Persist(); err != nil {
			t.Fatal(err)
		}
		got := h.saved[len(h.saved)-1].Onboarding
		if got.MCPServer != tc.wantMCPAnswer || got.AgentSkills != tc.wantSkills {
			t.Fatalf("%s: recorded mcp=%q skills=%q", tc.destination, got.MCPServer, got.AgentSkills)
		}
	}
}

type agentToolsFixture struct {
	home string
	cmd  *cobra.Command
	out  *bytes.Buffer
	err  *bytes.Buffer
}

func newAgentToolsFixture(t *testing.T) agentToolsFixture {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	prevHome, prevExe := agentToolsHome, agentToolsExecutable
	prevDiscover, prevLook, prevRun := mcpDiscover, mcpLookPath, mcpRunCLI
	prevHarnesses := mcpConnectOpts.harnesses
	agentToolsHome = func() (string, error) { return home, nil }
	agentToolsExecutable = func() string { return "/opt/beacon/bin/beacon" }
	mcpDiscover = func() []harness.Harness {
		return []harness.Harness{
			{Name: "cursor", DisplayName: "Cursor", Detected: true},
			{Name: "gemini_cli", DisplayName: "Gemini CLI", Detected: true},
			{Name: "codex_cli", DisplayName: "Codex CLI", Detected: false},
		}
	}
	mcpLookPath = func(string) (string, error) { return "", errors.New("not on PATH") }
	mcpRunCLI = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("no harness CLI may run here")
	}
	mcpConnectOpts.harnesses = nil
	t.Cleanup(func() {
		agentToolsHome, agentToolsExecutable = prevHome, prevExe
		mcpDiscover, mcpLookPath, mcpRunCLI = prevDiscover, prevLook, prevRun
		mcpConnectOpts.harnesses = prevHarnesses
	})
	f := agentToolsFixture{home: home, cmd: &cobra.Command{}, out: &bytes.Buffer{}, err: &bytes.Buffer{}}
	f.cmd.SetOut(f.out)
	f.cmd.SetErr(f.err)
	f.cmd.SetContext(context.Background())
	return f
}

func TestInstallAgentToolsRegistersMCPAndInstallsSkills(t *testing.T) {
	f := newAgentToolsFixture(t)
	installAgentTools(f.cmd, onboardingOutcome{InstallMCP: true, InstallSkills: true})
	if f.err.Len() != 0 {
		t.Fatalf("stderr: %s", f.err)
	}
	for _, rel := range []string{".cursor/mcp.json", ".gemini/settings.json"} {
		data, err := os.ReadFile(filepath.Join(f.home, rel))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"beacon"`) || !strings.Contains(string(data), "/opt/beacon/bin/beacon") {
			t.Fatalf("%s:\n%s", rel, data)
		}
	}
	// A harness that was not detected is not configured.
	if _, err := os.Stat(filepath.Join(f.home, ".codex", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("codex config written for an undetected harness (err=%v)", err)
	}
	for _, root := range []string{".agents/skills", ".claude/skills"} {
		for _, name := range agentskills.Names() {
			if _, err := os.Stat(filepath.Join(f.home, root, name, "SKILL.md")); err != nil {
				t.Fatalf("%s/%s not installed: %v", root, name, err)
			}
		}
	}
	out := f.out.String()
	for _, want := range []string{"registered for Cursor", "registered for Gemini CLI", "installed in ~/.agents/skills", "installed in ~/.claude/skills"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}

	// Both are undone by their uninstall commands.
	if err := runMCPUninstall(f.cmd, nil); err != nil {
		t.Fatal(err)
	}
	if err := runSkillsUninstall(f.cmd, nil); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".cursor/mcp.json", ".gemini/settings.json", ".agents/skills/beacon-memory-recall", ".claude/skills/beacon-memory-recall"} {
		if _, err := os.Stat(filepath.Join(f.home, rel)); !os.IsNotExist(err) {
			t.Fatalf("%s still present after uninstall (err=%v)", rel, err)
		}
	}
}

func TestDeclinedAgentToolsWriteNothing(t *testing.T) {
	f := newAgentToolsFixture(t)
	installAgentTools(f.cmd, onboardingOutcome{})
	for _, rel := range []string{".cursor", ".gemini", ".agents", ".claude/skills"} {
		if _, err := os.Stat(filepath.Join(f.home, rel)); !os.IsNotExist(err) {
			t.Fatalf("%s written although the agent tools were turned off (err=%v)", rel, err)
		}
	}
	if f.out.Len() != 0 || f.err.Len() != 0 {
		t.Fatalf("output: %q %q", f.out, f.err)
	}
}

func TestRefreshAgentSkillsOnlyUpdatesInstalledCopies(t *testing.T) {
	f := newAgentToolsFixture(t)
	prevOpts := endpointOpts
	prevRoot := onboardingIsRoot
	endpointOpts.userMode, endpointOpts.systemMode = true, false
	onboardingIsRoot = func() bool { return false }
	t.Cleanup(func() { endpointOpts, onboardingIsRoot = prevOpts, prevRoot })

	refreshAgentSkills(f.err)
	if _, err := os.Stat(filepath.Join(f.home, ".agents")); !os.IsNotExist(err) {
		t.Fatalf("refresh installed skills nobody asked for (err=%v)", err)
	}
	if err := installAgentSkills(f.out); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(f.home, ".agents", "skills", "beacon-memory-recall", "SKILL.md")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	refreshAgentSkills(f.err)
	if data, _ := os.ReadFile(stale); string(data) == "old" {
		t.Fatal("refresh left a stale skill")
	}
	if f.err.Len() != 0 {
		t.Fatalf("stderr: %s", f.err)
	}
}
