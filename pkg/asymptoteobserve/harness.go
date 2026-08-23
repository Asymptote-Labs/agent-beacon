package asymptoteobserve

import "strings"

// NormalizeHarnessName maps whatever a runtime calls itself onto Beacon's canonical harness name.
//
// This lives in the shared module because two independent paths write the same field and must
// agree. The OTLP path derives a name from resource attributes and event names in the collector
// exporter; the hook path takes it from the `--platform` value it was installed with. They
// disagreed: one session produced both `claude` and `claude_code`, which splits that session in
// two for any query, dashboard, or SIEM detection that groups by harness.name -- exactly what a
// customer forwarding to Sentinel does.
//
// Normalizing at the point of writing rather than at every point of reading is deliberate. The
// alternative pushes the inconsistency onto every consumer, including consumers we do not own,
// and `harness.name` is a release contract that downstream queries are written against.
//
// The `--platform` flag is intentionally left alone: it identifies the runtime being hooked and
// is part of the hook command written into settings.json, so changing it would only take effect
// after every existing install were rewritten. Mapping at write time fixes existing installs too.
//
// Ordering matters. The browser-chat cases must precede the generic "claude" rule, which would
// otherwise coerce claude_web into claude_code.
func NormalizeHarnessName(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch {
	case lower == "":
		return ""
	case strings.Contains(lower, "cowork") || strings.Contains(lower, "co-work"):
		return "claude_cowork"
	case strings.Contains(lower, "claude_agent_sdk") || strings.Contains(lower, "claude-agent-sdk") || strings.Contains(lower, "claude agent sdk"):
		return "claude_agent_sdk"
	case strings.Contains(lower, "claude_web") || strings.Contains(lower, "claude-web") || lower == "claude.ai":
		return "claude_web"
	case strings.Contains(lower, "chatgpt") || lower == "chatgpt.com" || strings.Contains(lower, "openai_web"):
		return "chatgpt_web"
	case strings.Contains(lower, "claude_code") || strings.Contains(lower, "claude-code") || strings.Contains(lower, "claude code") || strings.HasPrefix(lower, "claude_code."):
		return "claude_code"
	case lower == "claude" || strings.Contains(lower, "claude"):
		return "claude_code"
	case strings.Contains(lower, "openclaw") || strings.Contains(lower, "open-claw"):
		return "openclaw_gateway"
	case strings.Contains(lower, "antigravity") || strings.Contains(lower, "anti-gravity"):
		return "antigravity_cli"
	case strings.Contains(lower, "codex"):
		return "codex_cli"
	case strings.Contains(lower, "gemini"):
		return "gemini_cli"
	// Every VS Code spelling resolves before the generic copilot rule below, which contains
	// "copilot" and would otherwise swallow them. Two consequences of getting this wrong, both
	// real: the hook path installs with --platform vscode while the OTLP path reports
	// vscode_copilot, so one session was recorded under two names; and vscode_copilot itself
	// normalized to copilot_cli, which is a different product -- meaning the canonical name was
	// not stable under re-normalization and VS Code activity was attributed to the Copilot CLI.
	case strings.Contains(lower, "copilot-chat") || strings.Contains(lower, "vscode_copilot") ||
		strings.Contains(lower, "vscode-copilot") || strings.Contains(lower, "vscode") ||
		strings.Contains(lower, "vs code"):
		return "vscode_copilot"
	case strings.Contains(lower, "github-copilot") || strings.Contains(lower, "copilot_cli") || strings.Contains(lower, "copilot"):
		return "copilot_cli"
	// Pi is matched by equality against a fixed set of spellings, not by substring, because "pi" is
	// two characters and appears inside names that belong to other runtimes: "copilot" contains it,
	// so a Contains rule here would report every GitHub Copilot and VS Code Copilot session as Pi.
	// The set is closed for the same reason -- a prefix rule would claim any future runtime whose
	// name happens to start with those two letters.
	//
	// Its position after the Copilot rules is also deliberate. Equality makes this case
	// order-independent today, but if it is ever loosened to a substring match, sitting below the
	// broader rules means Copilot still resolves correctly and only genuine Pi names reach here.
	// The reverse ordering would turn that same edit into silent misattribution.
	case lower == "pi" || lower == "pi.dev" || lower == "pi_cli" || lower == "pi-cli" ||
		lower == "pi_agent" || lower == "pi-agent" || lower == "pi agent":
		return "pi_cli"
	// Cline reaches Beacon from more than one host -- a VS Code extension, a JetBrains plugin and a
	// CLI, all over the same agent core -- so the spelling that arrives depends on which host
	// reported it. Pinning the canonical name here is what keeps one Cline session from being
	// recorded under two names when the hook path (`--platform cline`) and an OTLP resource
	// attribute disagree about capitalization or suffix.
	//
	// Equality against a closed set, like Pi above and unlike the Contains rules further up.
	// "cline" is a substring of Cline's own configuration names -- `.clinerules` is a directory
	// every Cline user has -- so a Contains rule here would claim any harness whose name merely
	// mentioned one of those paths. The set is closed rather than a prefix rule for the same
	// reason.
	case lower == "cline" || lower == "cline_cli" || lower == "cline-cli" || lower == "cline cli" ||
		lower == "cline.bot":
		return "cline"
	// Qwen Code is matched by equality against a closed set, not by a substring rule, and the
	// reason is specific to this runtime rather than general caution: every Qwen model id begins
	// with the same four letters the harness does -- qwen3-coder-plus, qwen-max, qwen-turbo. A
	// Contains rule would therefore report any event whose harness attribute happened to carry a
	// model string as a Qwen Code session, which is how a model name ends up masquerading as a
	// runtime in a dashboard grouped by harness.name.
	//
	// The canonical spelling is qwen_code rather than qwen_cli because Qwen Code is the product's
	// name; it follows claude_code, not codex_cli. Both spellings arrive in practice -- the hook
	// path installs with --platform qwen, while an OTLP resource attribute carries whatever the
	// runtime calls itself -- and pinning them here is what keeps one session from being recorded
	// under two names.
	case lower == "qwen" || lower == "qwen_code" || lower == "qwen-code" || lower == "qwen code" ||
		lower == "qwencode" || lower == "qwen_cli" || lower == "qwen-cli" || lower == "qwen cli" ||
		lower == "qwen_coder" || lower == "qwen-coder" || lower == "qwen coder":
		return "qwen_code"
	case name != "":
		// An unrecognized runtime keeps its own name rather than being coerced or dropped. A new
		// harness should show up in the log as itself, not as "unknown".
		return name
	default:
		return ""
	}
}
