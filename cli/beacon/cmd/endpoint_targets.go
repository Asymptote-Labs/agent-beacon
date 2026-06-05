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

func normalizeEndpointTarget(name string) (endpointTarget, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	key = strings.ReplaceAll(key, "_", "-")
	switch key {
	case "":
		return endpointTarget{}, false
	case "claude", "claude-code":
		return endpointTarget{Name: "claude", Kind: endpointTargetOTLP}, true
	case "codex", "codex-cli":
		return endpointTarget{Name: "codex", Kind: endpointTargetOTLP}, true
	case "gemini", "gemini-cli":
		return endpointTarget{Name: "gemini", Kind: endpointTargetOTLP}, true
	case "vscode", "vs-code", "vscode-copilot":
		return endpointTarget{Name: "vscode", Kind: endpointTargetOTLP}, true
	case "cursor":
		return endpointTarget{Name: "cursor", Kind: endpointTargetHook}, true
	case "claude-hooks":
		return endpointTarget{Name: "claude", Kind: endpointTargetHook}, true
	case "factory", "droid":
		return endpointTarget{Name: "factory", Kind: endpointTargetHook}, true
	case "opencode":
		return endpointTarget{Name: "opencode", Kind: endpointTargetHook}, true
	case "grok":
		return endpointTarget{Name: "grok", Kind: endpointTargetHook}, true
	case "hermes", "hermes-agent":
		return endpointTarget{Name: "hermes", Kind: endpointTargetHook}, true
	case "antigravity", "antigravity-cli":
		return endpointTarget{Name: "antigravity", Kind: endpointTargetHook}, true
	case "devin", "devin-cli":
		return endpointTarget{Name: "devin-cli", Kind: endpointTargetHook}, true
	case "devin-desktop":
		return endpointTarget{Name: "devin-desktop", Kind: endpointTargetHook}, true
	default:
		return endpointTarget{}, false
	}
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

func normalizeHookTarget(name string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	key = strings.ReplaceAll(key, "_", "-")
	switch key {
	case "":
		return "", false
	case "antigravity", "antigravity-cli":
		return "antigravity", true
	case "cursor":
		return "cursor", true
	case "claude", "claude-code":
		return "claude", true
	case "vscode", "vs-code":
		return "vscode", true
	case "factory", "droid":
		return "factory", true
	case "opencode":
		return "opencode", true
	case "grok":
		return "grok", true
	case "hermes", "hermes-agent":
		return "hermes", true
	case "devin", "devin-cli":
		return "devin-cli", true
	case "devin-desktop":
		return "devin-desktop", true
	default:
		if target, ok := normalizeEndpointTarget(key); ok && target.Kind == endpointTargetHook {
			return target.Name, true
		}
		return "", false
	}
}
