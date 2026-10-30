package asymptoteobserve

import "testing"

// Two independent writers must produce the same name for the same session. The hook path installs
// with --platform <runtime> (cli/beacon/internal/endpoint/hooks/*.go) and the OTLP path derives a
// name from resource attributes; before both went through one function, a single Claude Code
// session was recorded as both "claude" and "claude_code", which splits it for any query,
// dashboard, or SIEM detection that groups by harness.name. Every spelling here is one a writer
// produces; the closed-set cases list each member.
func TestHarnessSpellingsConvergeOnCanonicalNames(t *testing.T) {
	for want, spellings := range map[string][]string{
		"claude_code":     {"claude", "claude-code"},
		"claude_web":      {"claude.ai", "claude_web"}, // ordered before the generic claude rule
		"codex_cli":       {"codex", "codex-cli", "codex_exec", "codex_cli_rs"},
		"codex_desktop":   {"codex-app-server", "codex_app_server", "Codex App Server", "codex-desktop", "Codex Desktop"},
		"gemini_cli":      {"gemini"},
		"antigravity_cli": {"antigravity"},
		// VS Code resolves before the generic copilot rule; "copilot" contains "pi", so Pi must not
		// be a substring rule either.
		"vscode_copilot": {"vscode", "copilot-chat"},
		"copilot_cli":    {"copilot", "github-copilot"},
		"pi_cli":         {"pi", " PI ", "pi.dev", "pi_cli", "pi-cli", "pi_agent", "pi-agent", "pi agent"},
		// Oh My Pi and Pi are separate products; every Oh My Pi spelling ends in "pi".
		"omp":         {"omp", "omp_cli", "omp-cli", "omp cli", "oh-my-pi", "oh_my_pi", "ohmypi", "Oh My Pi", "omp.sh"},
		"cline":       {"cline", "cline_cli", "cline-cli", "cline cli", "cline.bot"},
		"qwen_code":   {"qwen", "qwen_code", "qwen-code", "Qwen Code", "qwencode", "qwen_cli", "qwen-cli", "qwen cli", "qwen-coder", "Qwen Coder"},
		"prime_agent": {"prime", "prime_agent", "prime-agent", "Prime Agent", "primeagent", "prime_cli", "prime-cli", "prime cli", "prime-intellect", "Prime Intellect"},
		"omo_senpi":   {"omo", "omo_senpi", "omo-senpi", "Omo Senpi", "omo senpi"},
		"vercel_fx":   {"fx", "fx_cli", "fx-cli", "fx cli", "fx.sh", "vercel_fx", "vercel-fx", "Vercel FX", "vercel fx cli", "fx_agent", "fx-agent"},
		"muse_code":   {"muse", "muse_code", "muse-code", "Muse Code", "musecode", "muse_cli", "muse-cli", "Muse CLI"},
		"openhands": {"openhands", "open_hands", "open-hands", "Open Hands", "openhands_cli", "openhands-cli", "OpenHands CLI",
			"openhands_agent", "openhands-agent", "OpenHands Agent", "openhands.dev"},
		"grok_bot": {"grok_bot", "grok-bot", "Grok Bot", "grokbot", "grok_bot_desktop", "grok-bot-desktop", "cursor.grok_bot",
			"cursor/grok_bot", "xai_grok_bot", "xai-grok-bot"},
		// Kiro's IDE and CLI share one .kiro/hooks directory, so they are one harness deliberately.
		"kiro": {"kiro", "kiro_ide", "kiro-ide", "Kiro IDE", "kiro_cli", "kiro-cli", "Kiro CLI", "kirocli", "kiro_code", "kiro-code",
			"Kiro Code", "kirocode", "kiro_agent", "kiro-agent", "Kiro Agent", "kiro.dev"},
		"goose": {"goose", "goose_cli", "goose-cli", "Goose CLI", "goosecli", "goose_agent", "goose-agent", "Goose Agent",
			"codename_goose", "codename-goose", "Codename Goose", "block_goose", "block-goose", "Block Goose"},
		"deepseek_harness": {"dsh", "dsh_cli", "dsh-cli", "DSH CLI", "dshcli", "deepseek_harness", "deepseek-harness", "DeepSeek Harness",
			"deepseekharness", "deepseek_ai/dsh", "deepseek-ai/dsh", "@deepseek-ai/dsh", "deepseek_dsh", "deepseek-dsh"},
		// kimi_code_cli is the client_type on every Kimi hook payload; it must agree with --platform kimi.
		"kimi_code": {"kimi", "kimi_code", "kimi-code", "Kimi Code", "kimicode", "kimi_cli", "kimi-cli", "Kimi CLI", "kimi_code_cli",
			"kimi-code-cli", "Kimi Code CLI", "kimi_code_acp", "kimi-code-acp", "moonshot_kimi_code", "moonshot-kimi-code"},
		// Harness² writes its own name with a superscript; the session collector writes harness2.
		"harness2": {"harness2", "Harness2", "Harness²", "harness^2", "harness_2", "harness-2", "Harness 2", "harness2_cli",
			"harness2-cli", "google-research/harness2", "google_research/harness2", "google_harness2", "google-harness2"},
		"": {"", "   ", "\t"},
	} {
		for _, in := range spellings {
			if got := NormalizeHarnessName(in); got != want {
				t.Errorf("NormalizeHarnessName(%q) = %q, want %q -- one session would be recorded under two names", in, got, want)
			}
		}
	}
}

// Normalizing an already-canonical name must return it unchanged. vscode_copilot once collapsed
// into copilot_cli, a different product, so anything that re-read and re-wrote an event silently
// reassigned VS Code activity to the CLI.
func TestCanonicalNamesAreStableUnderRenormalization(t *testing.T) {
	for _, canonical := range []string{
		"claude_code", "codex_cli", "codex_desktop", "gemini_cli", "antigravity_cli", "vscode_copilot",
		"copilot_cli", "claude_web", "chatgpt_web", "claude_cowork", "claude_agent_sdk",
		"openclaw_gateway", "pi_cli", "omp", "cline", "qwen_code", "prime_agent", "omo_senpi", "vercel_fx",
		"muse_code", "openhands", "grok_bot", "kiro", "goose", "deepseek_harness", "kimi_code", "harness2",
	} {
		if got := NormalizeHarnessName(canonical); got != canonical {
			t.Errorf("NormalizeHarnessName(%q) = %q; a canonical name must survive being normalized again", canonical, got)
		}
	}
}

// An unrecognized runtime keeps its own name: coercing it would erase the one clue available when
// a new harness starts reporting. Short closed-set names (pi, omp, cline, qwen, fx) sit inside
// ordinary words and model ids, so these must fall through rather than be claimed by a substring
// rule. `grok` is Grok Build's own hook name; fx's model providers are not fx.
func TestUnrecognizedNamesArePreserved(t *testing.T) {
	for _, name := range []string{
		"cursor", "hermes", "opencode", "factory", "grok", "gateway", "xai",
		"pipeline", "pied-piper", "pixel-cli",
		"prompt-runner", "compass", "ompa-cli",
		".clinerules", "clinerules-sync", "decline-bot",
		"qwen3-coder-plus", "qwen-max", "qwen2.5-coder-32b-instruct",
		"fxagent", "sfx", "myfx-cli", "fx-runner",
	} {
		if got := NormalizeHarnessName(name); got != name {
			t.Errorf("NormalizeHarnessName(%q) = %q, want it preserved", name, got)
		}
	}
}

// Closed-set cases whose name is shared with the vendor's model family, an ordinary word, a config
// path, or a sibling product. A substring rule would file these under the runtime, and a reader
// could not tell the misattribution from the real thing.
func TestLookalikeNamesAreNotAttributedToTheHarness(t *testing.T) {
	for harness, names := range map[string][]string{
		"prime_agent": {"prime-numbers", "amazon_prime", "primer", "claude_code_primed"},
		// Upstream Senpi and the wider oh-my-openagent project name more than this edition. omoplate
		// guards a prefix rule, the others a substring rule.
		"omo_senpi": {"senpi", "oh-my-openagent", "oh_my_openagent", "ammo", "promotion", "homology", "omoplate"},
		"muse_code": {"muse-spark", "muse_spark", "muse-spark-1.2-contributor", "Muse Spark 1.2", "amuse", "museum"},
		// Bare "hands" guards a suffix rule, the others a substring or prefix rule.
		"openhands": {"openhands-lm", "openhands-lm-32b-v0.1", "all-hands/openhands-lm-32b-v0.1", "OpenHands LM 32B",
			"openai", "opencode", "open-interpreter", "handsfree", "openhands-runtime-sandbox", "hands"},
		// Grok Build (the local CLI) and Grok models must never be filed under the cloud Grok Bot.
		"grok_bot": {"Grok", "grok_build", "grok-build", "Grok Build", "grok-cli", "grok-4", "grok-code-fast-1", "Grok 4.6", "x.ai"},
		"kiro":     {".kiro/hooks", ".kiroignore", "kiro_powers", "kiro-runtime-sandbox", "kiro-lm-7b", "amazonq", "kiro-coder-32b", "aws/kiro-lm-32b-v0.1"},
		// GooseAI is a different company's inference provider.
		"goose": {"gooseai", "goose-ai", "goose.ai", "gooseai/gpt-neo-20b", ".goosehints", "GOOSE_MODE", "mongoose", "goose-island"},
		// Bare "deepseek" is the vendor and model family, not the harness.
		"deepseek_harness": {"deepseek-chat", "deepseek-reasoner", "deepseek-ai/DeepSeek-V3", "DeepSeek-R1-0528", "deepseek",
			"deepseek.com", "deepseek_api", "DSH_HOME", "~/.dsh", ".dsh/skills", "dsh-runtime-sandbox", "dshield"},
		"kimi_code": {"kimi-k2", "kimi-k2.7-code", "kimi-for-coding", "moonshotai/kimi-k2.5", "moonshot-kimi-k2-instruct",
			"kimi.com", "KIMI_API_KEY", "providers.kimi", "~/.kimi-code", "KIMI_CODE_HOME", "akimi"},
		// Harness (harness.io) is a CI/CD product with its own CLI, and "harness" is the generic word
		// for the code around a model. Harness² drives OpenCode and Codex, which keep their own names.
		"harness2": {"harness", "Harness", "harness.io", "harness-cli", "harness_cli", "harness-delegate", "harness_ci",
			"agent-harness", "harness3", "harness22", "harness2-runner", "HARNESS2_RUNS_DIR"},
	} {
		for _, name := range names {
			if got := NormalizeHarnessName(name); got == harness {
				t.Errorf("NormalizeHarnessName(%q) = %q; a lookalike must not be reported as that harness", name, got)
			}
		}
	}
}
