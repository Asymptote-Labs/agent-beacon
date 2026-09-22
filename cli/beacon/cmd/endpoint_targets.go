package cmd

import (
	"fmt"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
)

type endpointTargetKind string

const (
	endpointTargetOTLP endpointTargetKind = "otlp"
	endpointTargetHook endpointTargetKind = "hook"
)

type endpointTarget struct {
	Name string
	Kind endpointTargetKind
}

const endpointHarnessAuto = "auto"

type autoEndpointTarget struct {
	OTLP  []string
	Hooks []string
	Skip  string
}

type skippedEndpointTarget struct {
	Name   string
	Reason string
}

type endpointTargetSelection struct {
	OTLP      []string
	Hooks     []string
	Skipped   []skippedEndpointTarget
	Automatic bool
}

// autoEndpointTargets maps discovery's canonical event names onto endpoint install's target
// names. Some discovered runtimes deliberately have no install target: Beacon can observe their
// presence, but enabling collection requires an external admin setting, launch environment, or a
// poll command rather than a config file endpoint install may safely mutate.
var autoEndpointTargets = map[string]autoEndpointTarget{
	"claude_code":      {OTLP: []string{"claude"}, Hooks: []string{"claude"}},
	"codex_cli":        {OTLP: []string{"codex"}, Hooks: []string{"codex"}},
	"gemini_cli":       {OTLP: []string{"gemini"}},
	"antigravity_cli":  {Hooks: []string{"antigravity"}},
	"copilot_cli":      {Skip: "its OTLP endpoint is managed through the Copilot CLI launch environment"},
	"opencode":         {Hooks: []string{"opencode"}},
	"cline":            {Hooks: []string{"cline"}},
	"pi_cli":           {Hooks: []string{"pi"}},
	"omp":              {Hooks: []string{"omp"}},
	"openclaw":         {Hooks: []string{"openclaw"}},
	"grok":             {Hooks: []string{"grok"}},
	"prime_agent":      {Hooks: []string{"prime"}},
	"omo_senpi":        {Hooks: []string{"omo"}},
	"vercel_fx":        {Skip: "collection is enabled by running `beacon endpoint fx sync`"},
	"deepseek_harness": {Hooks: []string{"dsh"}},
	"qwen_code":        {Hooks: []string{"qwen"}},
	"kimi_code":        {Hooks: []string{"kimi"}},
	"kiro":             {Hooks: []string{"kiro"}},
	"muse_code":        {Hooks: []string{"muse"}},
	"openhands":        {Skip: "the CLI and GUI require project-level hooks installed from inside each repository"},
	"hermes":           {Hooks: []string{"hermes"}},
	"factory":          {Hooks: []string{"factory"}},
	"vscode":           {OTLP: []string{"vscode"}, Hooks: []string{"vscode"}},
	"cursor":           {Hooks: []string{"cursor"}},
	"devin-cli":        {Hooks: []string{"devin-cli"}},
	"devin-desktop":    {Hooks: []string{"devin-desktop"}},
	"claude_cowork":    {Skip: "its OTLP endpoint requires Claude admin configuration"},
}

// harnessTarget is one row of the supported-harness registry. It is the single
// source of truth for both normalizers:
//
//   - endpointAliases map a spelling to the harness in the combined endpoint
//     namespace, classified by endpointKind (OTLP vs hook).
//   - hookAliases map a spelling to the harness in the hook-only namespace.
//
// The two alias sets differ on purpose: in the hook-only namespace some OTLP
// spellings are reinterpreted as their hook variant (for example "claude",
// "codex", and "vscode"), and OTLP-only harnesses are not hook-addressable at all.
type harnessTarget struct {
	name            string
	endpointKind    endpointTargetKind
	endpointAliases []string
	hookAliases     []string
}

// harnessTargets lists every supported runtime. Adding a harness is one row;
// the lookup tables below are derived from it.
var harnessTargets = []harnessTarget{
	{name: "claude", endpointKind: endpointTargetOTLP, endpointAliases: []string{"claude", "claude-code"}, hookAliases: []string{"claude", "claude-code"}},
	{name: "claude", endpointKind: endpointTargetHook, endpointAliases: []string{"claude-hooks"}, hookAliases: []string{"claude-hooks"}},
	{name: "codex", endpointKind: endpointTargetOTLP, endpointAliases: []string{"codex", "codex-cli"}, hookAliases: []string{"codex", "codex-cli"}},
	{name: "gemini", endpointKind: endpointTargetOTLP, endpointAliases: []string{"gemini", "gemini-cli"}},
	{name: "vscode", endpointKind: endpointTargetOTLP, endpointAliases: []string{"vscode", "vs-code", "vscode-copilot"}, hookAliases: []string{"vscode", "vs-code"}},
	{name: "cursor", endpointKind: endpointTargetHook, endpointAliases: []string{"cursor"}, hookAliases: []string{"cursor"}},
	{name: "factory", endpointKind: endpointTargetHook, endpointAliases: []string{"factory", "droid"}, hookAliases: []string{"factory", "droid"}},
	{name: "opencode", endpointKind: endpointTargetHook, endpointAliases: []string{"opencode"}, hookAliases: []string{"opencode"}},
	{name: "cline", endpointKind: endpointTargetHook, endpointAliases: []string{"cline"}, hookAliases: []string{"cline"}},
	// pi_cli is accepted alongside pi because it is the canonical harness name events are written
	// under, so anyone reading a Pi row out of the runtime log and passing it back to a --harness
	// flag gets the runtime they are looking at rather than an "unsupported harness" error.
	{name: "pi", endpointKind: endpointTargetHook, endpointAliases: []string{"pi", "pi-cli", "pi_cli"}, hookAliases: []string{"pi", "pi-cli", "pi_cli"}},
	// Oh My Pi is a separate row from Pi rather than an alias of it. The two are separately
	// installed products with different config roots and different capability surfaces, and folding
	// either spelling into the other would install one runtime's extension while the operator asked
	// for the other's. "oh-my-pi" is accepted because it is the repository name people reach for;
	// "omp" is the binary, and the canonical harness name events are written under.
	{name: "omp", endpointKind: endpointTargetHook, endpointAliases: []string{"omp", "oh-my-pi", "ohmypi"}, hookAliases: []string{"omp", "oh-my-pi", "ohmypi"}},
	// Prime Agent is a third row rather than an alias of either, for the reason Oh My Pi is a
	// second: it is a separately installed product with its own config root, and folding a spelling
	// into another runtime would install that runtime's extension while the operator asked for this
	// one. "prime" is the --platform value and the shortest unambiguous spelling; "prime-agent" is
	// what the binary is called; "prime_agent" is the canonical harness name events are written
	// under, so a row read out of the runtime log and passed back to --harness resolves to the
	// runtime it names. Prime Intellect's model names are deliberately not aliases -- accepting one
	// would let someone ask to install an extension for a model.
	{name: "prime", endpointKind: endpointTargetHook, endpointAliases: []string{"prime", "prime-agent", "prime_agent", "primeagent"}, hookAliases: []string{"prime", "prime-agent", "prime_agent", "primeagent"}},
	// Senpi (the standalone edition of oh-my-openagent) is a fourth row rather than an alias of any
	// of the above, for the reason Prime Agent is a third: it is a separately installed product with
	// its own config root, and folding a spelling into another runtime would install that runtime's
	// extension while the operator asked for this one. "omo" is the --platform value, the binary the
	// operator runs, and the config directory Beacon installs into; "omo_senpi" and "omo-senpi" are
	// the canonical harness name events are written under, so a row read out of the runtime log and
	// passed back to --harness resolves to the runtime it names. Bare "senpi" and
	// "oh-my-openagent" are deliberately not aliases: upstream Senpi is a separately installable
	// engine a user can run without OMO at all, and "oh-my-openagent" also names OMO's OpenCode and
	// Codex CLI editions, which install through the existing "opencode" and hook-config paths rather
	// than through this extension -- accepting either spelling here would claim a name that belongs
	// to a different install, exactly the reason NormalizeHarnessName keeps them unmapped too.
	{name: "omo", endpointKind: endpointTargetHook, endpointAliases: []string{"omo", "omo_senpi", "omo-senpi"}, hookAliases: []string{"omo", "omo_senpi", "omo-senpi"}},
	// OpenHands. "open-hands" is accepted because the product is written as two words as often as
	// one, and normalizeHarnessKey folds "open_hands" onto it. "openhands" is also the canonical
	// harness name events are written under, so a row read out of the runtime log and passed back
	// to --harness resolves to the runtime it names. "openhands-lm" is deliberately not an alias:
	// that is All Hands' model family, and accepting it would let someone ask to install hooks for
	// a model.
	{name: "openhands", endpointKind: endpointTargetHook, endpointAliases: []string{"openhands", "open-hands"}, hookAliases: []string{"openhands", "open-hands"}},
	// DeepSeek Harness. One row for every surface, because the CLI, Web, ACP and SDK are four
	// compositions of one harness booting from one Harness home -- and Beacon installs into the
	// home-level patch layer, which applies to all of them. "dsh" is the binary, the --platform
	// value and the shortest unambiguous spelling; "deepseek-harness" and "deepseek_harness" are
	// what the product is called and the canonical harness name events are written under, so a row
	// read out of the runtime log and passed back to --harness resolves to the runtime it names.
	//
	// Bare "deepseek" is deliberately not an alias, and it is the one a user is most likely to
	// type. It names the vendor and the model family, and accepting it would let someone ask to
	// install hooks for a model -- the same reason NormalizeHarnessName leaves it unmapped.
	{name: "dsh", endpointKind: endpointTargetHook, endpointAliases: []string{"dsh", "deepseek-harness", "deepseek_harness", "deepseekharness"}, hookAliases: []string{"dsh", "deepseek-harness", "deepseek_harness", "deepseekharness"}},
	// Kiro. One row for the IDE and the CLI together, because they are one harness reading one
	// hooks directory -- so "kiro-ide" and "kiro-cli" are aliases of the same install rather than
	// two targets, and asking for either gets the one that covers both. "kiro" is also the
	// canonical harness name events are written under, so a row read out of the runtime log and
	// passed back to --harness resolves to the runtime it names. "kiro-code" is accepted because
	// people say it; Kiro's own documentation does not, which is why it is an alias and not the
	// name.
	{name: "kiro", endpointKind: endpointTargetHook, endpointAliases: []string{"kiro", "kiro-ide", "kiro-cli", "kiro-code"}, hookAliases: []string{"kiro", "kiro-ide", "kiro-cli", "kiro-code"}},
	// Kimi Code. One row for every surface, because the terminal CLI, the desktop app, the VS
	// Code extension and the ACP server are front ends over one agent core that reads one
	// user-level config.toml -- so one install covers all of them, and "kimi-code-cli" is an alias
	// of that install rather than a target of its own. "kimi" is the binary and the --platform
	// value; "kimi-code" and "kimi_code" are what the product is called and the canonical harness
	// name events are written under, so a row read out of the runtime log and passed back to
	// --harness resolves to the runtime it names.
	//
	// Bare "moonshot" and every "kimi-k2"/"kimi-k3" spelling are deliberately not aliases. The
	// first is the vendor and the rest are the model family, and accepting either would let
	// someone ask to install hooks for a model -- the same reason NormalizeHarnessName leaves them
	// unmapped. Bare "kimi" is accepted here and not there for the same reason it is accepted in
	// the harness normalizer: it is the name of the thing the operator runs.
	{name: "kimi", endpointKind: endpointTargetHook, endpointAliases: []string{"kimi", "kimi-code", "kimi-code-cli"}, hookAliases: []string{"kimi", "kimi-code", "kimi-code-cli"}},
	// OpenClaw Gateway. One row, and it is the plugin row: `--harness openclaw` installs the
	// Beacon-managed plugin, which is the path that collects the agent's work. The gateway's other
	// surface, its own diagnostics-otel plugin, is configured inside OpenClaw rather than by
	// Beacon and has its own commands under `beacon endpoint integrations openclaw`, so it is not
	// an endpoint target here.
	//
	// "openclaw-gateway" and "openclaw_gateway" both normalize to "openclaw-gateway" through
	// normalizeHarnessKey, so the two spellings need one alias between them; it is accepted
	// because `openclaw_gateway` is the canonical harness name events are written under, and a row
	// read out of the runtime log and passed back to --harness should resolve to the runtime it
	// names. "claw" is deliberately not an alias: it is short enough to collide with something
	// else later, and nothing calls the product that.
	{name: "openclaw", endpointKind: endpointTargetHook, endpointAliases: []string{"openclaw", "openclaw-gateway"}, hookAliases: []string{"openclaw", "openclaw-gateway"}},
	{name: "grok", endpointKind: endpointTargetHook, endpointAliases: []string{"grok"}, hookAliases: []string{"grok"}},
	// "qwen-code" and "qwen_code" both normalize to "qwen-code" through normalizeHarnessKey, so the
	// two spellings need one alias between them. "qwen-cli" is not accepted: the product is Qwen
	// Code, and an alias nobody uses is an alias to keep working.
	{name: "qwen", endpointKind: endpointTargetHook, endpointAliases: []string{"qwen", "qwen-code"}, hookAliases: []string{"qwen", "qwen-code"}},
	// Muse Code, Meta's terminal agent. "muse-code" and "muse_code" both normalize to "muse-code"
	// through normalizeHarnessKey, so the two spellings need one alias between them; it is accepted
	// because muse_code is the canonical harness name events are written under, so a row read out
	// of the runtime log and passed back to --harness resolves to the runtime it names. "spark" is
	// deliberately not an alias: Muse Spark is the model, and accepting it here would let someone
	// ask to install hooks for a model.
	{name: "muse", endpointKind: endpointTargetHook, endpointAliases: []string{"muse", "muse-code"}, hookAliases: []string{"muse", "muse-code"}},
	{name: "hermes", endpointKind: endpointTargetHook, endpointAliases: []string{"hermes", "hermes-agent"}, hookAliases: []string{"hermes", "hermes-agent"}},
	{name: "antigravity", endpointKind: endpointTargetHook, endpointAliases: []string{"antigravity", "antigravity-cli"}, hookAliases: []string{"antigravity", "antigravity-cli"}},
	{name: "devin-cli", endpointKind: endpointTargetHook, endpointAliases: []string{"devin", "devin-cli"}, hookAliases: []string{"devin", "devin-cli"}},
	{name: "devin-desktop", endpointKind: endpointTargetHook, endpointAliases: []string{"devin-desktop"}, hookAliases: []string{"devin-desktop"}},
}

var (
	endpointTargetLookup = buildEndpointTargetLookup()
	hookTargetLookup     = buildHookTargetLookup()
)

func buildEndpointTargetLookup() map[string]endpointTarget {
	m := make(map[string]endpointTarget)
	for _, t := range harnessTargets {
		for _, alias := range t.endpointAliases {
			m[alias] = endpointTarget{Name: t.name, Kind: t.endpointKind}
		}
	}
	return m
}

func buildHookTargetLookup() map[string]string {
	m := make(map[string]string)
	for _, t := range harnessTargets {
		for _, alias := range t.hookAliases {
			m[alias] = t.name
		}
	}
	return m
}

// normalizeHarnessKey canonicalizes a user-supplied harness spelling: trimmed,
// lowercased, with underscores treated as hyphens.
func normalizeHarnessKey(name string) string {
	key := strings.ToLower(strings.TrimSpace(name))
	return strings.ReplaceAll(key, "_", "-")
}

func normalizeEndpointTarget(name string) (endpointTarget, bool) {
	key := normalizeHarnessKey(name)
	if key == "" {
		return endpointTarget{}, false
	}
	target, ok := endpointTargetLookup[key]
	return target, ok
}

func normalizeHookTarget(name string) (string, bool) {
	key := normalizeHarnessKey(name)
	if key == "" {
		return "", false
	}
	target, ok := hookTargetLookup[key]
	return target, ok
}

// otlpTargetCarriesHook names the OTLP harnesses whose hook integration is installed
// alongside their OpenTelemetry settings by `endpoint install` and `endpoint repair`.
// `beacon endpoint hooks install` still installs the same hooks on their own, and
// `beacon endpoint hooks uninstall` removes them.
var otlpTargetCarriesHook = map[string]bool{
	"claude": true,
	"codex":  true,
}

func splitEndpointTargets(values []string) (otlp []string, hooks []string, err error) {
	seenOTLP := map[string]bool{}
	seenHooks := map[string]bool{}
	for _, value := range values {
		target, ok := normalizeEndpointTarget(value)
		if !ok {
			if strings.TrimSpace(value) == "" {
				continue
			}
			return nil, nil, fmt.Errorf("unsupported harness %q", value)
		}
		switch target.Kind {
		case endpointTargetOTLP:
			if !seenOTLP[target.Name] {
				otlp = append(otlp, target.Name)
				seenOTLP[target.Name] = true
			}
			// Some OTLP harnesses carry their hook integration with them, so the
			// default install list configures both without a second command:
			//
			//   - Codex usage comes from OTLP turn spans, but those spans do not
			//     identify the local OS account. Its SessionStart context hook is
			//     therefore part of the token integration rather than an optional
			//     duplicate activity path.
			//   - Claude Code's OTLP export has no session start or end, no file
			//     reads or diffs, and no subagent lifecycle; those come only from
			//     its hooks. Without them a default install records a thinner
			//     session than every other supported runtime.
			if otlpTargetCarriesHook[target.Name] && !seenHooks[target.Name] {
				hooks = append(hooks, target.Name)
				seenHooks[target.Name] = true
			}
		case endpointTargetHook:
			if !seenHooks[target.Name] {
				hooks = append(hooks, target.Name)
				seenHooks[target.Name] = true
			}
		}
	}
	return otlp, hooks, nil
}

func resolveEndpointTargets(value string, discovered []harness.Harness) (endpointTargetSelection, error) {
	values := splitHarnessCSV(value)
	if len(values) != 1 || (normalizeHarnessKey(values[0]) != endpointHarnessAuto && normalizeHarnessKey(values[0]) != "all") {
		otlp, hooks, err := splitEndpointTargets(values)
		return endpointTargetSelection{OTLP: otlp, Hooks: hooks}, err
	}

	switch normalizeHarnessKey(values[0]) {
	case "all":
		return allInstallableEndpointTargets(), nil
	case endpointHarnessAuto:
		selection := endpointTargetSelection{Automatic: true}
		seenOTLP := map[string]bool{}
		seenHooks := map[string]bool{}
		for _, found := range discovered {
			if !found.Detected {
				continue
			}
			target, ok := autoEndpointTargets[found.Name]
			if !ok {
				selection.Skipped = append(selection.Skipped, skippedEndpointTarget{
					Name:   found.DisplayName,
					Reason: "no automatic installer is registered",
				})
				continue
			}
			if target.Skip != "" {
				selection.Skipped = append(selection.Skipped, skippedEndpointTarget{
					Name:   found.DisplayName,
					Reason: target.Skip,
				})
				continue
			}
			for _, name := range target.OTLP {
				if !seenOTLP[name] {
					selection.OTLP = append(selection.OTLP, name)
					seenOTLP[name] = true
				}
			}
			for _, name := range target.Hooks {
				if !seenHooks[name] {
					selection.Hooks = append(selection.Hooks, name)
					seenHooks[name] = true
				}
			}
		}
		return selection, nil
	default:
		panic("unreachable endpoint harness selection")
	}
}

func allInstallableEndpointTargets() endpointTargetSelection {
	selection := endpointTargetSelection{}
	seenOTLP := map[string]bool{}
	seenHooks := map[string]bool{}
	for _, target := range harnessTargets {
		if target.endpointKind == endpointTargetOTLP && !seenOTLP[target.name] {
			selection.OTLP = append(selection.OTLP, target.name)
			seenOTLP[target.name] = true
		}
		// hookAliases is also populated on OTLP rows whose runtime has a separately
		// installable hook integration, notably Claude, Codex, and VS Code.
		if len(target.hookAliases) > 0 && !seenHooks[target.name] {
			selection.Hooks = append(selection.Hooks, target.name)
			seenHooks[target.name] = true
		}
	}
	return selection
}

func canonicalHookTargets(values []string) ([]string, error) {
	seen := map[string]bool{}
	targets := []string{}
	for _, value := range values {
		target, ok := normalizeHookTarget(value)
		if !ok {
			if strings.TrimSpace(value) == "" {
				continue
			}
			return nil, fmt.Errorf("unsupported hook harness %q", value)
		}
		if !seen[target] {
			targets = append(targets, target)
			seen[target] = true
		}
	}
	return targets, nil
}
