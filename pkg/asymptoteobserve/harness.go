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
	case name != "":
		// An unrecognized runtime keeps its own name rather than being coerced or dropped. A new
		// harness should show up in the log as itself, not as "unknown".
		return name
	default:
		return ""
	}
}
