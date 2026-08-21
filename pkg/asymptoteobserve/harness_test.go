package asymptoteobserve

import "testing"

// The property this function exists for: two independent writers must produce the same name for
// the same session. The hook path installs with --platform <runtime> and the OTLP path derives a
// name from resource attributes; before these were normalized through one function, a single
// Claude Code session was recorded as both "claude" and "claude_code", which splits it for any
// query, dashboard, or SIEM detection that groups by harness.name.
func TestHookPlatformsConvergeOnCanonicalNames(t *testing.T) {
	// Keys are the --platform values the hook installers use (see
	// cli/beacon/internal/endpoint/hooks/*.go); values are what the OTLP path reports.
	for platform, want := range map[string]string{
		"claude":      "claude_code",
		"codex":       "codex_cli",
		"gemini":      "gemini_cli",
		"antigravity": "antigravity_cli",
		"vscode":      "vscode_copilot",
		"pi":          "pi_cli",
	} {
		t.Run(platform, func(t *testing.T) {
			if got := NormalizeHarnessName(platform); got != want {
				t.Errorf("NormalizeHarnessName(%q) = %q, want %q -- the hook and OTLP paths would "+
					"record one session under two names", platform, got, want)
			}
		})
	}
}

// Normalizing an already-canonical name must return it unchanged. Without this, a value that has
// been through the function once changes meaning when it goes through again -- and vscode_copilot
// did exactly that, collapsing into copilot_cli, which is a different product. Anything that
// re-reads and re-writes an event would have silently reassigned VS Code activity to the CLI.
func TestCanonicalNamesAreStableUnderRenormalization(t *testing.T) {
	for _, canonical := range []string{
		"claude_code", "codex_cli", "gemini_cli", "antigravity_cli", "vscode_copilot",
		"copilot_cli", "claude_web", "chatgpt_web", "claude_cowork", "claude_agent_sdk",
		"openclaw_gateway", "pi_cli",
	} {
		t.Run(canonical, func(t *testing.T) {
			if got := NormalizeHarnessName(canonical); got != canonical {
				t.Errorf("NormalizeHarnessName(%q) = %q; a canonical name must survive being "+
					"normalized again", canonical, got)
			}
		})
	}
}

// Ordering is load-bearing: the browser and VS Code cases sit before broader rules that would
// otherwise swallow them. These are the pairs where one name is a substring of another's rule.
func TestNarrowerRuntimesWinOverBroaderRules(t *testing.T) {
	for input, want := range map[string]string{
		"claude.ai":        "claude_web",     // not claude_code
		"claude_web":       "claude_web",     // not claude_code
		"claude-code":      "claude_code",    // not claude_web
		"copilot-chat":     "vscode_copilot", // not copilot_cli
		"github-copilot":   "copilot_cli",    // not vscode_copilot
		"claude_agent_sdk": "claude_agent_sdk",
		"claude_cowork":    "claude_cowork",
	} {
		t.Run(input, func(t *testing.T) {
			if got := NormalizeHarnessName(input); got != want {
				t.Errorf("NormalizeHarnessName(%q) = %q, want %q", input, got, want)
			}
		})
	}
}

// Pi's canonical name is reachable from the spellings the two write paths actually produce: the
// hook path installs with --platform pi, and the OTLP path would read a service.name.
func TestPiSpellingsConvergeOnPiCLI(t *testing.T) {
	for _, in := range []string{
		"pi", "PI", " pi ", "pi.dev", "pi_cli", "pi-cli", "pi_agent", "pi-agent", "pi agent",
	} {
		t.Run(in, func(t *testing.T) {
			if got := NormalizeHarnessName(in); got != "pi_cli" {
				t.Errorf("NormalizeHarnessName(%q) = %q, want %q", in, got, "pi_cli")
			}
		})
	}
}

// "pi" is a two-character name that appears inside other runtimes' names, so matching it by
// substring would attribute their sessions to Pi. "copilot" contains "pi" -- every Copilot and VS
// Code Copilot session is the concrete blast radius of getting this wrong, and both are runtimes
// Beacon already supports. This test is what keeps the Pi case an equality match.
func TestPiDoesNotSwallowNamesContainingPi(t *testing.T) {
	for input, want := range map[string]string{
		"copilot":        "copilot_cli",
		"github-copilot": "copilot_cli",
		"copilot_cli":    "copilot_cli",
		"copilot-chat":   "vscode_copilot",
		"vscode_copilot": "vscode_copilot",
	} {
		t.Run(input, func(t *testing.T) {
			if got := NormalizeHarnessName(input); got != want {
				t.Errorf("NormalizeHarnessName(%q) = %q, want %q -- a substring rule for Pi would "+
					"reassign this runtime's sessions to pi_cli", input, got, want)
			}
		})
	}
}

// The Pi spelling set is closed, not a prefix rule. A future runtime whose name merely starts with
// "pi" must keep its own name rather than being recorded as Pi activity.
func TestNamesMerelyStartingWithPiAreNotPi(t *testing.T) {
	for _, name := range []string{"pip-agent", "pipeline", "pixel-cli", "pied-piper"} {
		if got := NormalizeHarnessName(name); got != name {
			t.Errorf("NormalizeHarnessName(%q) = %q, want it preserved", name, got)
		}
	}
}

// An unrecognized runtime keeps its own name. Coercing it to "unknown" would erase the one clue
// available when a new harness starts reporting, and these names reach customer dashboards.
func TestUnknownRuntimesKeepTheirName(t *testing.T) {
	for _, name := range []string{"cursor", "hermes", "opencode", "factory", "grok"} {
		if got := NormalizeHarnessName(name); got != name {
			t.Errorf("NormalizeHarnessName(%q) = %q, want it preserved", name, got)
		}
	}
}

func TestEmptyNameStaysEmpty(t *testing.T) {
	for _, in := range []string{"", "   ", "\t"} {
		if got := NormalizeHarnessName(in); got != "" {
			t.Errorf("NormalizeHarnessName(%q) = %q, want empty", in, got)
		}
	}
}
