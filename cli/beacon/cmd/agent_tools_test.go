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
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/onboarding"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestOnboardingAgentToolsOutcomeAndRecord(t *testing.T) {
	for _, tc := range []struct {
		name          string
		destination   string
		cloudMCP      bool
		skills        bool
		wantCloudMCP  bool
		wantMCPAnswer string
		wantSkills    string
	}{
		{"cloud keeps both", onboarding.DestinationAsymptote, true, true, true, onboarding.AgentToolInstalled, onboarding.AgentToolInstalled},
		{"cloud declines MCP", onboarding.DestinationAsymptote, false, true, false, onboarding.AgentToolDeclined, onboarding.AgentToolInstalled},
		// Local is never asked about Beacon Cloud MCP, so nothing is acted on or recorded for it.
		{"local", onboarding.DestinationLocal, true, false, false, "", onboarding.AgentToolDeclined},
	} {
		h := newOnboardingHarness(t)
		var offered bool
		onboardingRunWizard = func(_ io.Reader, _ io.Writer, opts onboarding.WizardOptions) (onboarding.WizardResult, error) {
			offered = opts.OfferAgentTools
			return onboarding.WizardResult{
				Completed:       true,
				Destination:     tc.destination,
				PrivacyMode:     "standard",
				ConnectCloudMCP: tc.cloudMCP,
				InstallSkills:   tc.skills,
			}, nil
		}
		outcome, err := maybeRunOnboarding(h.cmd)
		if err != nil {
			t.Fatal(err)
		}
		if !offered {
			t.Fatalf("%s: the wizard was not offered the agent tools", tc.name)
		}
		if outcome.ConnectCloudMCP != tc.wantCloudMCP || outcome.InstallSkills != tc.skills {
			t.Fatalf("%s: outcome = %+v", tc.name, outcome)
		}
		if err := outcome.Persist(); err != nil {
			t.Fatal(err)
		}
		got := h.saved[len(h.saved)-1].Onboarding
		if got.CloudMCP != tc.wantMCPAnswer || got.AgentSkills != tc.wantSkills {
			t.Fatalf("%s: recorded cloud_mcp=%q agent_skills=%q", tc.name, got.CloudMCP, got.AgentSkills)
		}
	}
}

func agentToolsCommand(t *testing.T) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	cmd := &cobra.Command{}
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetContext(context.Background())
	return cmd, &out, &errOut
}

// Onboarding registers Beacon Cloud MCP through the same flow as `beacon mcp connect --yes`:
// detected harnesses, OAuth (only the URL is written), and no question on stdin.
func TestOnboardingRegistersCloudMCPWithoutAsking(t *testing.T) {
	fx := newMCPFixture(t, "cursor")
	mcpIngestURL = func() string { return strings.TrimSuffix(fx.url, "/mcp") }
	mcpStdin = strings.NewReader("n\n") // would decline if it were asked
	cmd, out, errOut := agentToolsCommand(t)

	connectCloudMCPFromOnboarding(cmd)

	if errOut.Len() != 0 {
		t.Fatalf("stderr: %s\nstdout: %s", errOut, out)
	}
	data, err := os.ReadFile(filepath.Join(fx.home, ".cursor", "mcp.json"))
	if err != nil {
		t.Fatalf("cursor config not written: %v\n%s", err, out)
	}
	if !strings.Contains(string(data), `"beacon-managed"`) || !strings.Contains(string(data), fx.url) {
		t.Fatalf("cursor config:\n%s", data)
	}
	if strings.Contains(string(data), fakeAccountToken) || strings.Contains(out.String(), fakeAccountToken) {
		t.Fatal("the account token leaked")
	}
	if strings.Contains(out.String(), "[y/N]") {
		t.Fatalf("onboarding asked again:\n%s", out)
	}
}

func TestOnboardingCloudMCPFailureIsAWarning(t *testing.T) {
	prev := agentToolsConnectCloudMCP
	t.Cleanup(func() { agentToolsConnectCloudMCP = prev })
	var got cloudMCPRequest
	agentToolsConnectCloudMCP = func(_ context.Context, _ io.Writer, req cloudMCPRequest) error {
		got = req
		return errors.New("offline")
	}
	cmd, _, errOut := agentToolsCommand(t)
	connectCloudMCPFromOnboarding(cmd)
	if !got.yes || got.dryRun || got.force || got.tokenEnv != "" || len(got.harnesses) != 0 || got.url != "" {
		t.Fatalf("request = %+v", got)
	}
	if !strings.Contains(errOut.String(), "offline") || !strings.Contains(errOut.String(), "beacon mcp connect") {
		t.Fatalf("stderr: %s", errOut)
	}
}

func newSkillsHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	prev := agentToolsHome
	agentToolsHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { agentToolsHome = prev })
	return home
}

func TestOnboardingInstallsSkillsWhenKept(t *testing.T) {
	home := newSkillsHome(t)
	cmd, out, errOut := agentToolsCommand(t)
	installAgentSkillsFromOnboarding(cmd, onboardingOutcome{InstallSkills: true})
	if errOut.Len() != 0 {
		t.Fatalf("stderr: %s", errOut)
	}
	for _, root := range []string{".agents/skills", ".claude/skills"} {
		for _, name := range agentskills.Names() {
			if _, err := os.Stat(filepath.Join(home, root, name, "SKILL.md")); err != nil {
				t.Fatalf("%s/%s not installed: %v", root, name, err)
			}
		}
	}
	for _, want := range []string{"installed in ~/.agents/skills", "installed in ~/.claude/skills"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if err := runSkillsUninstall(cmd, nil); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{".agents/skills", ".claude/skills"} {
		if entries, _ := os.ReadDir(filepath.Join(home, root)); len(entries) != 0 {
			t.Fatalf("%s not empty after uninstall: %v", root, entries)
		}
	}
}

func TestDeclinedSkillsWriteNothing(t *testing.T) {
	home := newSkillsHome(t)
	cmd, out, errOut := agentToolsCommand(t)
	installAgentSkillsFromOnboarding(cmd, onboardingOutcome{})
	for _, rel := range []string{".agents", ".claude/skills"} {
		if _, err := os.Stat(filepath.Join(home, rel)); !os.IsNotExist(err) {
			t.Fatalf("%s written although the skills were turned off (err=%v)", rel, err)
		}
	}
	if out.Len() != 0 || errOut.Len() != 0 {
		t.Fatalf("output: %q %q", out, errOut)
	}
}

func TestRefreshAgentSkillsOnlyUpdatesInstalledCopies(t *testing.T) {
	home := newSkillsHome(t)
	prevOpts := endpointOpts
	prevRoot := onboardingIsRoot
	endpointOpts.userMode, endpointOpts.systemMode = true, false
	onboardingIsRoot = func() bool { return false }
	t.Cleanup(func() { endpointOpts, onboardingIsRoot = prevOpts, prevRoot })
	var out, errOut bytes.Buffer

	refreshAgentSkills(&errOut)
	if _, err := os.Stat(filepath.Join(home, ".agents")); !os.IsNotExist(err) {
		t.Fatalf("refresh installed skills nobody asked for (err=%v)", err)
	}
	if err := installAgentSkills(&out); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(home, ".agents", "skills", "beacon-memory-recall", "SKILL.md")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	refreshAgentSkills(&errOut)
	if data, _ := os.ReadFile(stale); string(data) == "old" {
		t.Fatal("refresh left a stale skill")
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr: %s", errOut.String())
	}
}

func TestFinishCloudSetupRegistersMCPOnlyAfterConnectSucceeds(t *testing.T) {
	prev := agentToolsConnectCloudMCP
	t.Cleanup(func() { agentToolsConnectCloudMCP = prev })
	for _, tc := range []struct {
		name       string
		outcome    onboardingOutcome
		connect    bool // connectAfterInstall
		connectErr error
		wantMCP    bool
		wantStderr string
		wantStdout string
	}{
		{"connect then MCP", onboardingOutcome{Connect: true, ConnectCloudMCP: true}, true, nil, true, "", ""},
		{"failed connect skips MCP", onboardingOutcome{Connect: true, ConnectCloudMCP: true}, true, errors.New("enroll failed"), false, "Beacon Cloud MCP was not registered either", ""},
		{"MCP turned off suggests it", onboardingOutcome{Connect: true}, true, nil, false, "", "beacon mcp connect"},
		{"already connected endpoint", onboardingOutcome{ConnectCloudMCP: true}, false, nil, true, "", ""},
		{"local", onboardingOutcome{}, false, nil, false, "", ""},
	} {
		h := newOnboardingHarness(t)
		h.loaded = onboarding.Profile{Onboarding: onboarding.Onboarding{CompletedAt: "2026-10-05T00:00:00Z"}}
		ran := false
		agentToolsConnectCloudMCP = func(context.Context, io.Writer, cloudMCPRequest) error {
			ran = true
			return nil
		}
		connected := false
		finishCloudSetup(h.cmd, tc.outcome, tc.connect, func() error {
			connected = true
			return tc.connectErr
		})
		if connected != tc.connect {
			t.Fatalf("%s: connect ran = %t", tc.name, connected)
		}
		if ran != tc.wantMCP {
			t.Fatalf("%s: Beacon Cloud MCP ran = %t, want %t", tc.name, ran, tc.wantMCP)
		}
		if tc.wantStderr != "" && !strings.Contains(h.stderr.String(), tc.wantStderr) {
			t.Fatalf("%s: stderr = %q", tc.name, h.stderr)
		}
		if tc.wantStdout != "" && !strings.Contains(h.stdout.String(), tc.wantStdout) {
			t.Fatalf("%s: stdout = %q", tc.name, h.stdout)
		}
		if tc.wantStdout == "" && strings.Contains(h.stdout.String(), "run `beacon mcp connect`") {
			t.Fatalf("%s: suggested mcp connect although it was registered or not applicable: %q", tc.name, h.stdout)
		}
	}
}
