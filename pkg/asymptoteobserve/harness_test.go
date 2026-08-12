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
		"openclaw_gateway",
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
