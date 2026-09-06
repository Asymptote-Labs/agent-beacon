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
	// Oh My Pi (omp) is a separate product from Pi, not another Pi host, so it gets its own
	// canonical name rather than folding into pi_cli. It is a fork of pi-mono with its own binary
	// (`omp`), its own config root (`~/.omp`), its own npm package, and an event API Pi does not
	// have -- approval decisions among them. Recording both under one name would merge two
	// separately installed runtimes' activity in every query that groups by harness.name, which is
	// the one thing a security log must not do to two different products on the same machine.
	//
	// The canonical spelling is `omp` because that is what the user sees: the binary they run and
	// the directory Beacon installs into. `oh-my-pi` is the repository name, not the command.
	//
	// Equality against a closed set, like Pi below and for a sharper version of the same reason:
	// "omp" is three characters and sits inside ordinary words this field carries in practice --
	// "prompt" contains it, and so does any harness name containing "comp". A Contains rule would
	// report those as Oh My Pi sessions.
	//
	// Placed above the Pi case deliberately. Equality makes the order irrelevant today, but every
	// Oh My Pi spelling here ends in "pi", so if the Pi case were ever loosened to a substring
	// match, resolving Oh My Pi first is what keeps "oh-my-pi" from being recorded as Pi.
	case lower == "omp" || lower == "omp_cli" || lower == "omp-cli" || lower == "omp cli" ||
		lower == "oh-my-pi" || lower == "oh_my_pi" || lower == "ohmypi" || lower == "oh my pi" ||
		lower == "omp.sh":
		return "omp"
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
	// Muse Code is Meta's terminal coding agent; Muse Spark is the model it runs. Beacon hooks the
	// agent, so the harness is muse_code -- the canonical spelling follows claude_code and
	// qwen_code, because Muse Code is the product's name rather than a CLI suffix.
	//
	// Muse Spark spellings are deliberately absent from this set, and that is the whole reason the
	// case is written as equality against a closed set rather than Contains(lower, "muse"). Every
	// Muse Spark model id begins with the same four letters the harness does -- muse-spark-1.2,
	// muse-spark-1.3, muse-spark-1.2-contributor -- so a substring rule would report any event
	// whose harness attribute happened to carry a model string as a Muse Code session. That is the
	// Qwen failure exactly (see TestQwenModelNamesAreNotTreatedAsTheHarness), and here it would be
	// worse: the model and the agent ship under one brand, so a reader seeing "muse_spark" in
	// harness.name has no way to tell a misattributed model id from a real runtime. Leaving those
	// spellings unmapped means they fall to the passthrough case below and show up as themselves,
	// which is visible as an anomaly rather than silently filed under the agent.
	//
	// "muse" alone is also an ordinary English word, so the closed set is what keeps a harness
	// attribute that merely contains it from being claimed. There is no prefix rule for the same
	// reason: a future runtime named "musey" is not this one.
	case lower == "muse" || lower == "muse_code" || lower == "muse-code" || lower == "muse code" ||
		lower == "musecode" || lower == "muse_cli" || lower == "muse-cli" || lower == "muse cli":
		return "muse_code"
	// Prime Agent (Prime Intellect) ships the same extension API as Pi, which is why Beacon
	// observes it the same way -- but it is a separate product, and recording its sessions as
	// pi_cli would merge two runtimes' activity under one name in every query that groups by
	// harness.name. The canonical spelling is prime_agent because that is the product's name and
	// the command it installs; it follows claude_code and qwen_code, not codex_cli.
	//
	// Equality against a closed set, like Pi, Cline and Qwen Code above rather than the Contains
	// rules further up. "prime" is an ordinary English word that appears in model ids, file paths
	// and vendor names, so a substring rule would report any event whose harness attribute merely
	// contained it as a Prime Agent session. The set is closed rather than a prefix rule for the
	// same reason.
	//
	// Both the hook spelling and the runtime's own are pinned here: the hook path installs with
	// --platform prime, while the runtime calls itself prime-agent, and without this one session
	// would be recorded under two names.
	case lower == "prime" || lower == "prime_agent" || lower == "prime-agent" || lower == "prime agent" ||
		lower == "primeagent" || lower == "prime_cli" || lower == "prime-cli" || lower == "prime cli" ||
		lower == "prime_intellect" || lower == "prime-intellect" || lower == "prime intellect":
		return "prime_agent"
	// fx (vercel-labs/fx) is matched by equality against a closed set for the same reason Pi is,
	// only more so: "fx" is two characters and appears inside ordinary words a harness attribute
	// can plausibly carry -- "sfx", "fx-runner", "effects" does not contain it but "fxagent" does
	// -- so a Contains rule would claim sessions that are not fx's. There is no prefix rule here
	// either: a future runtime named "fxr" is not this one.
	//
	// The canonical name is vercel_fx rather than fx because harness.name is what a SIEM query
	// groups by, and a two-letter value there says nothing about which product produced the row.
	// The vendor prefix follows what the product is called in practice ("Vercel fx") and keeps the
	// name self-describing for a reader who has never seen it before.
	case lower == "fx" || lower == "fx_cli" || lower == "fx-cli" || lower == "fx cli" ||
		lower == "fx.sh" || lower == "vercel_fx" || lower == "vercel-fx" || lower == "vercel fx" ||
		lower == "vercel fx cli" || lower == "fx_agent" || lower == "fx-agent":
		return "vercel_fx"
	// OpenHands (formerly All Hands AI) is matched by equality against a closed set for the same
	// reason Qwen Code and Muse Code are, and it is the Muse case exactly: the same organization
	// ships the agent and a model family under one name, so every OpenHands LM model id begins
	// with the letters the harness does -- openhands-lm-32b-v0.1, openhands-lm-7b-v0.1. A
	// Contains(lower, "openhands") rule would report any event whose harness attribute happened to
	// carry one of those model strings as an OpenHands session, and a reader could not tell the
	// misattribution from the real thing because both start with the same word.
	//
	// Model spellings are therefore deliberately left out of the set: they fall to the passthrough
	// case and show up as themselves, which is visible as an anomaly rather than silently filed
	// under the agent.
	//
	// The canonical spelling is plain `openhands` rather than `openhands_cli`, because OpenHands
	// is one product that runs the same agent in Cloud, the CLI and the local GUI -- Beacon hooks
	// all three through the same `.openhands/hooks.json`, so a CLI suffix would name only one of
	// the places the events come from. It follows `cline` and `omp`, not `codex_cli`.
	case lower == "openhands" || lower == "open_hands" || lower == "open-hands" || lower == "open hands" ||
		lower == "openhands_cli" || lower == "openhands-cli" || lower == "openhands cli" ||
		lower == "openhands_agent" || lower == "openhands-agent" || lower == "openhands agent" ||
		lower == "openhands.dev":
		return "openhands"
	// Grok Bot is xAI's always-on agent product: each Bot runs on a Cursor-hosted cloud computer
	// and the desktop app is a client to it. It is a different product from Grok Build, the xAI
	// terminal coding agent Beacon hooks under the `grok` harness, and the two must never share a
	// name: one runs on the endpoint under the operator's own account, the other runs in a vendor
	// cloud and reaches Beacon only through Cursor's server-side OpenTelemetry export (resource
	// attribute cursor.surface=grok_bot). A query grouping by harness.name that merged them would
	// file cloud-computer shell commands under a local CLI.
	//
	// Equality against a closed set, and deliberately narrower than every other xAI spelling.
	// "grok" alone is Grok Build and stays on the passthrough path as itself; "grok-4", "grok-4.6"
	// and the other model ids also begin with the same four letters, so a Contains(lower, "grok")
	// rule would claim both the sibling product and any event whose harness attribute carried a
	// model string. Those fall through and show up as themselves, which is the visible anomaly a
	// reader can act on rather than a silent misattribution.
	//
	// The canonical spelling is grok_bot because that is what the product is called and what
	// Cursor's own export writes for the surface; it follows claude_code and muse_code, not a CLI
	// suffix, because there is no CLI.
	case lower == "grok_bot" || lower == "grok-bot" || lower == "grok bot" || lower == "grokbot" ||
		lower == "grok_bot_desktop" || lower == "grok-bot-desktop" || lower == "cursor.grok_bot" ||
		lower == "cursor/grok_bot" || lower == "xai_grok_bot" || lower == "xai-grok-bot":
		return "grok_bot"
	case name != "":
		// An unrecognized runtime keeps its own name rather than being coerced or dropped. A new
		// harness should show up in the log as itself, not as "unknown".
		return name
	default:
		return ""
	}
}
