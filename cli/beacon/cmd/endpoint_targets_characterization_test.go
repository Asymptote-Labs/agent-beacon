package cmd

import "testing"

// These characterization tests pin the exact input→output mapping of the two
// harness-target normalizers across every recognized alias (including the
// underscore spellings) and the OTLP/Hook asymmetries, before the two switch
// statements are collapsed into a single harness table. They guard against any
// behavior drift during that refactor.

func TestNormalizeEndpointTargetCharacterization(t *testing.T) {
	type want struct {
		name string
		kind endpointTargetKind
		ok   bool
	}
	cases := map[string]want{
		// OTLP harnesses
		"claude":         {"claude", endpointTargetOTLP, true},
		"claude-code":    {"claude", endpointTargetOTLP, true},
		"claude_code":    {"claude", endpointTargetOTLP, true},
		"codex":          {"codex", endpointTargetOTLP, true},
		"codex-cli":      {"codex", endpointTargetOTLP, true},
		"gemini":         {"gemini", endpointTargetOTLP, true},
		"gemini-cli":     {"gemini", endpointTargetOTLP, true},
		"vscode":         {"vscode", endpointTargetOTLP, true},
		"vs-code":        {"vscode", endpointTargetOTLP, true},
		"vscode-copilot": {"vscode", endpointTargetOTLP, true},
		// Hook harnesses
		"cursor":          {"cursor", endpointTargetHook, true},
		"claude-hooks":    {"claude", endpointTargetHook, true},
		"factory":         {"factory", endpointTargetHook, true},
		"droid":           {"factory", endpointTargetHook, true},
		"opencode":        {"opencode", endpointTargetHook, true},
		"grok":            {"grok", endpointTargetHook, true},
		"hermes":          {"hermes", endpointTargetHook, true},
		"hermes-agent":    {"hermes", endpointTargetHook, true},
		"antigravity":     {"antigravity", endpointTargetHook, true},
		"antigravity-cli": {"antigravity", endpointTargetHook, true},
		"devin":           {"devin-cli", endpointTargetHook, true},
		"devin-cli":       {"devin-cli", endpointTargetHook, true},
		"devin-desktop":   {"devin-desktop", endpointTargetHook, true},
		// Case/whitespace normalization
		"  Claude_Code  ": {"claude", endpointTargetOTLP, true},
		"DEVIN_DESKTOP":   {"devin-desktop", endpointTargetHook, true},
		// Unsupported
		"":              {"", "", false},
		"unknown":       {"", "", false},
		"vscode-hooks":  {"", "", false},
		"claude-cowork": {"", "", false},
	}
	for in, w := range cases {
		got, ok := normalizeEndpointTarget(in)
		if ok != w.ok || got.Name != w.name || (ok && got.Kind != w.kind) {
			t.Errorf("normalizeEndpointTarget(%q) = (%+v, %v), want ({Name:%q Kind:%q}, %v)",
				in, got, ok, w.name, w.kind, w.ok)
		}
	}
}

func TestNormalizeHookTargetCharacterization(t *testing.T) {
	type want struct {
		name string
		ok   bool
	}
	cases := map[string]want{
		"antigravity":     {"antigravity", true},
		"antigravity-cli": {"antigravity", true},
		"antigravity_cli": {"antigravity", true},
		"cursor":          {"cursor", true},
		// claude is OTLP in the endpoint namespace but a hook in the hook namespace
		"claude":        {"claude", true},
		"claude-code":   {"claude", true},
		"claude_code":   {"claude", true},
		"claude-hooks":  {"claude", true},
		"vscode":        {"vscode", true},
		"vs-code":       {"vscode", true},
		"vs_code":       {"vscode", true},
		"factory":       {"factory", true},
		"droid":         {"factory", true},
		"opencode":      {"opencode", true},
		"grok":          {"grok", true},
		"hermes":        {"hermes", true},
		"hermes-agent":  {"hermes", true},
		"devin":         {"devin-cli", true},
		"devin-cli":     {"devin-cli", true},
		"devin-desktop": {"devin-desktop", true},
		"DEVIN_DESKTOP": {"devin-desktop", true},
		// OTLP-only harnesses are not hook-addressable
		"codex":          {"", false},
		"codex-cli":      {"", false},
		"gemini":         {"", false},
		"vscode-copilot": {"", false},
		// Unsupported
		"":        {"", false},
		"unknown": {"", false},
	}
	for in, w := range cases {
		got, ok := normalizeHookTarget(in)
		if ok != w.ok || got != w.name {
			t.Errorf("normalizeHookTarget(%q) = (%q, %v), want (%q, %v)", in, got, ok, w.name, w.ok)
		}
	}
}
