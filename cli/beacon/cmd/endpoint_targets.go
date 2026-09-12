package cmd

import (
	"fmt"
	"strings"
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
	// OpenHands. "open-hands" is accepted because the product is written as two words as often as
	// one, and normalizeHarnessKey folds "open_hands" onto it. "openhands" is also the canonical
	// harness name events are written under, so a row read out of the runtime log and passed back
	// to --harness resolves to the runtime it names. "openhands-lm" is deliberately not an alias:
	// that is All Hands' model family, and accepting it would let someone ask to install hooks for
	// a model.
	{name: "openhands", endpointKind: endpointTargetHook, endpointAliases: []string{"openhands", "open-hands"}, hookAliases: []string{"openhands", "open-hands"}},
	// Kiro. One row for the IDE and the CLI together, because they are one harness reading one
	// hooks directory -- so "kiro-ide" and "kiro-cli" are aliases of the same install rather than
	// two targets, and asking for either gets the one that covers both. "kiro" is also the
	// canonical harness name events are written under, so a row read out of the runtime log and
	// passed back to --harness resolves to the runtime it names. "kiro-code" is accepted because
	// people say it; Kiro's own documentation does not, which is why it is an alias and not the
	// name.
	{name: "kiro", endpointKind: endpointTargetHook, endpointAliases: []string{"kiro", "kiro-ide", "kiro-cli", "kiro-code"}, hookAliases: []string{"kiro", "kiro-ide", "kiro-cli", "kiro-code"}},
	// goose (Block). One name for the CLI and the desktop app together, because they are one agent
	// core reading one plugins directory and exporting under one service.name -- so asking for
	// either gets the install that covers both. "goose" is also the canonical harness name events
	// are written under, so a row read out of the runtime log and passed back to --harness resolves
	// to the runtime it names.
	//
	// Two rows, and it is the only runtime with both kinds under one name. They split the two
	// commands cleanly: `endpoint install --harness goose` configures the OTLP export, because only
	// the first row carries endpoint aliases, and `endpoint hooks install --harness goose` installs
	// the hooks, because only the second carries hook aliases. The two lookups are built from
	// different alias lists, so neither row can shadow the other.
	//
	// Both paths are wanted on a goose endpoint and neither subsumes the other: hooks see prompts,
	// tool calls, commands and file edits; OTLP carries the token usage, reported cost, model,
	// provider and reasoning that goose puts on no hook at all.
	//
	// "gooseai" and "goose-ai" are deliberately not aliases on either row: GooseAI is a different
	// company's inference service, and accepting either would let someone ask to install telemetry
	// for a model provider.
	{name: "goose", endpointKind: endpointTargetOTLP, endpointAliases: []string{"goose", "codename-goose", "block-goose"}},
	{name: "goose", endpointKind: endpointTargetHook, hookAliases: []string{"goose", "codename-goose", "block-goose"}},
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
			// Codex usage comes from OTLP turn spans, but those spans do not
			// identify the local OS account. Its SessionStart context hook is
			// therefore part of the token integration rather than an optional
			// duplicate activity path.
			if target.Name == "codex" && !seenHooks["codex"] {
				hooks = append(hooks, "codex")
				seenHooks["codex"] = true
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
