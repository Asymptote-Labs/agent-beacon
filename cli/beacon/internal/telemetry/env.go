package telemetry

import (
	"strings"
)

// Environment variables that turn telemetry off.
const (
	// EnvTelemetry set to 0, false, no or off turns CLI telemetry off.
	EnvTelemetry = "BEACON_TELEMETRY"
	// EnvDoNotTrack is the cross-tool convention (https://consoledonottrack.com); any value other
	// than empty, 0 or false turns CLI telemetry off.
	EnvDoNotTrack = "DO_NOT_TRACK"
)

// OptedOut reports whether the environment turns telemetry off.
func OptedOut(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv(EnvTelemetry))) {
	case "0", "false", "no", "off":
		return true
	}
	switch strings.ToLower(strings.TrimSpace(getenv(EnvDoNotTrack))) {
	case "", "0", "false":
		return false
	default:
		return true
	}
}

// agentMarker names the environment a coding agent sets in the shell it runs commands in. A marker
// must be one the agent sets for its own commands, not a setting a person exports to configure it
// (CLINE_DIR, GEMINI_CLI_HOME, OPENCODE_DB), and not one every terminal of an editor carries
// whether or not an agent is typing (Cursor's CURSOR_TRACE_ID is in every integrated terminal).
type agentMarker struct {
	agent    string
	names    []string
	prefixes []string
}

// agentMarkers is checked in order and the first match wins, so an agent started from another
// agent's shell is reported as the inner one when its marker comes first.
var agentMarkers = []agentMarker{
	// CLAUDECODE=1 and CLAUDE_CODE_ENTRYPOINT (cli, sdk-ts, ...) are set in every Bash tool shell.
	{agent: AgentClaudeCode, names: []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT"}},
	// The Cursor agent sets CURSOR_AGENT=1 in the terminals it drives.
	{agent: AgentCursor, names: []string{"CURSOR_AGENT"}},
	// Codex sets CODEX_SANDBOX in sandboxed commands and CODEX_THREAD_ID in every shell it spawns;
	// its npm and bun launchers export CODEX_MANAGED_BY_NPM / CODEX_MANAGED_BY_BUN to the process.
	{agent: AgentCodex, names: []string{"CODEX_SANDBOX", "CODEX_THREAD_ID"}, prefixes: []string{"CODEX_MANAGED_BY_"}},
	// Gemini CLI sets GEMINI_CLI=1 in the shell tool.
	{agent: AgentGeminiCLI, names: []string{"GEMINI_CLI"}},
	// OpenCode sets OPENCODE=1 in the bash tool.
	{agent: AgentOpenCode, names: []string{"OPENCODE"}},
	// Cline sets CLINE_ACTIVE=true in the terminals it runs commands in.
	{agent: AgentCline, names: []string{"CLINE_ACTIVE"}},
}

// envAIAgent is the generic convention an agent without a marker of its own can set
// (AI_AGENT=<name>); its value is reported after normalization.
const envAIAgent = "AI_AGENT"

// maxAgentNameLen bounds a name taken from AI_AGENT.
const maxAgentNameLen = 32

// InvokingAgent names the coding agent whose shell ran this command, from its environment
// (os.Environ shape), or AgentNone.
func InvokingAgent(environ []string) string {
	values := make(map[string]string, len(environ))
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if ok {
			values[name] = value
		}
	}
	for _, marker := range agentMarkers {
		for _, name := range marker.names {
			if strings.TrimSpace(values[name]) != "" {
				return marker.agent
			}
		}
		for _, prefix := range marker.prefixes {
			for name, value := range values {
				if strings.HasPrefix(name, prefix) && strings.TrimSpace(value) != "" {
					return marker.agent
				}
			}
		}
	}
	if name := normalizeAgentName(values[envAIAgent]); name != "" {
		return name
	}
	return AgentNone
}

// normalizeAgentName keeps a self-reported agent name to lower-case letters, digits, '_' and '-',
// so a free-form variable can never carry anything but a short label.
func normalizeAgentName(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	var b strings.Builder
	for _, r := range raw {
		if b.Len() >= maxAgentNameLen {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		case r == ' ' || r == '.':
			b.WriteByte('_')
		}
	}
	name := strings.Trim(b.String(), "_-")
	if name == "" || name == AgentNone {
		return ""
	}
	return name
}
