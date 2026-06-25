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
	{name: "grok", endpointKind: endpointTargetHook, endpointAliases: []string{"grok"}, hookAliases: []string{"grok"}},
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
