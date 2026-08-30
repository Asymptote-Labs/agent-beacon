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
		"cline":       "cline",
		"qwen":        "qwen_code",
		"prime":       "prime_agent",
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
		"openclaw_gateway", "pi_cli", "cline", "qwen_code", "prime_agent",
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

// Cline's canonical name has to be reachable from every spelling its hosts produce. It runs as a
// VS Code extension, a JetBrains plugin and a CLI over one agent core, so the hook path
// (--platform cline) and an OTLP service.name can each report a different capitalization or
// suffix for the same session.
func TestClineSpellingsConvergeOnCline(t *testing.T) {
	for _, in := range []string{
		"cline", "Cline", "CLINE", " cline ", "cline_cli", "cline-cli", "cline cli", "cline.bot",
	} {
		t.Run(in, func(t *testing.T) {
			if got := NormalizeHarnessName(in); got != "cline" {
				t.Errorf("NormalizeHarnessName(%q) = %q, want %q", in, got, "cline")
			}
		})
	}
}

// "cline" appears inside names Cline itself puts on disk: every Cline user has a `.clinerules`
// directory, and a harness reporting a name derived from one of those paths must keep it rather
// than being recorded as Cline activity. This is what keeps the Cline case an equality match
// rather than a Contains rule.
func TestClineDoesNotSwallowNamesMentioningCline(t *testing.T) {
	for _, name := range []string{".clinerules", "clinerules", "clinerules-sync", "decline-bot"} {
		if got := NormalizeHarnessName(name); got != name {
			t.Errorf("NormalizeHarnessName(%q) = %q, want it preserved", name, got)
		}
	}
}

// Cline is not a Claude surface. The generic `Contains(lower, "claude")` rule above returns
// claude_code for anything it can see the word "claude" in, and the two product names are close
// enough in writing that a future edit could plausibly route one through the other -- which would
// file Cline sessions as Claude Code sessions in every dashboard and detection that groups by
// harness.name.
func TestClineIsNotAttributedToClaudeCode(t *testing.T) {
	for _, in := range []string{"cline", "Cline", "cline_cli", "cline.bot"} {
		if got := NormalizeHarnessName(in); got == "claude_code" {
			t.Errorf("NormalizeHarnessName(%q) = %q, want Cline to keep its own harness", in, got)
		}
	}
}

// Qwen Code reaches Beacon under more than one spelling: the hook path installs with
// --platform qwen, while the OTLP path reports whatever the runtime puts in its resource
// attributes. Both must land on the same canonical name or one session is recorded as two.
func TestQwenSpellingsConvergeOnQwenCode(t *testing.T) {
	for _, in := range []string{
		"qwen", "Qwen", "QWEN", " qwen ", "qwen_code", "qwen-code", "Qwen Code", "qwencode",
		"qwen_cli", "qwen-cli", "qwen cli", "qwen-coder", "Qwen Coder",
	} {
		t.Run(in, func(t *testing.T) {
			if got := NormalizeHarnessName(in); got != "qwen_code" {
				t.Errorf("NormalizeHarnessName(%q) = %q, want %q", in, got, "qwen_code")
			}
		})
	}
}

// The reason the Qwen case is an equality match rather than a Contains rule. Every Qwen model id
// starts with the same four letters as the harness, so a substring rule would relabel any name
// carrying a model string as a Qwen Code session -- turning a model into a runtime in every
// dashboard grouped by harness.name.
func TestQwenModelNamesAreNotTreatedAsTheHarness(t *testing.T) {
	for _, name := range []string{
		"qwen3-coder-plus", "qwen3-coder", "qwen-max", "qwen-turbo", "qwen2.5-coder-32b-instruct",
	} {
		if got := NormalizeHarnessName(name); got != name {
			t.Errorf("NormalizeHarnessName(%q) = %q, want the model name preserved rather than "+
				"reported as the Qwen Code harness", name, got)
		}
	}
}

// Qwen Code is a Gemini CLI fork, which is exactly why this needs a test: the generic
// Contains(lower, "gemini") rule sits above the Qwen case, and Qwen Code keeps some Gemini-derived
// paths on disk. A Qwen session attributed to gemini_cli would be filed under a runtime the user
// does not have installed.
func TestQwenIsNotAttributedToGeminiCLI(t *testing.T) {
	for _, in := range []string{"qwen", "qwen-code", "qwen_code", "Qwen Code"} {
		if got := NormalizeHarnessName(in); got != "qwen_code" {
			t.Errorf("NormalizeHarnessName(%q) = %q, want qwen_code", in, got)
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

// Prime Agent reaches Beacon under more than one spelling for the same reason Qwen Code does: the
// hook path installs with --platform prime, while an OTLP resource attribute carries whatever the
// runtime calls itself, which is prime-agent.
func TestPrimeAgentSpellingsConvergeOnPrimeAgent(t *testing.T) {
	for _, in := range []string{
		"prime", "Prime", "PRIME", " prime ", "prime_agent", "prime-agent", "Prime Agent",
		"primeagent", "prime_cli", "prime-cli", "prime cli", "prime-intellect", "Prime Intellect",
	} {
		t.Run(in, func(t *testing.T) {
			if got := NormalizeHarnessName(in); got != "prime_agent" {
				t.Errorf("NormalizeHarnessName(%q) = %q, want %q", in, got, "prime_agent")
			}
		})
	}
}

// Prime Agent is a Pi distribution -- it ships Pi's extension API under a rebranded config
// directory -- which is exactly why this needs a test. The two runtimes are observed through the
// same mechanism, so the temptation is to file them under one name; doing that would merge two
// products' sessions in every query that groups by harness.name, and would make "which runtime is
// running here" unanswerable from the log.
func TestPrimeAgentIsNotAttributedToPi(t *testing.T) {
	for _, in := range []string{"prime", "prime-agent", "prime_agent", "Prime Agent"} {
		if got := NormalizeHarnessName(in); got == "pi_cli" {
			t.Errorf("NormalizeHarnessName(%q) = %q; Prime Agent must not be recorded as Pi", in, got)
		}
	}
	for _, in := range []string{"pi", "pi.dev", "pi-agent"} {
		if got := NormalizeHarnessName(in); got != "pi_cli" {
			t.Errorf("NormalizeHarnessName(%q) = %q; adding Prime Agent must not move Pi", in, got)
		}
	}
}

// The reason the Prime case is an equality match rather than a Contains rule. "prime" is an
// ordinary word: it turns up in model ids, vendor names, and directory paths that have nothing to
// do with this runtime, and a substring rule would file every one of them under prime_agent.
func TestPrimeSubstringsAreNotTreatedAsTheHarness(t *testing.T) {
	for _, name := range []string{
		"prime-numbers", "amazon_prime", "primer", "optimus-prime-cli", "claude_code_primed",
	} {
		if got := NormalizeHarnessName(name); got == "prime_agent" {
			t.Errorf("NormalizeHarnessName(%q) = %q; a name that merely contains \"prime\" must not be "+
				"reported as the Prime Agent harness", name, got)
		}
	}
}
