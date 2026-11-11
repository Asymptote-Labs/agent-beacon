// Package harness identifies the agent harness behind a model call and binds its tool calls to
// the neutral action they perform (read, shell, fetch, mcp), so analyzers see a file read as a
// file read whichever harness made it.
package harness

import (
	"encoding/json"
	"net/url"
	"strings"

	obs "github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Harness is the client that made a call.
type Harness struct {
	// Name is the canonical harness name (asymptoteobserve.NormalizeHarnessName) of the user
	// agent's product; "unknown" without a user agent.
	Name    string
	Version string
}

// products maps user-agent product tokens that do not name their harness to the name they mean.
var products = map[string]string{
	"claude-cli":   "claude_code",
	"codex_cli_rs": "codex_cli",
	"codex_exec":   "codex_cli",
	"codex-tui":    "codex_cli",
	"geminicli":    "gemini_cli",
	// VS Code's Copilot Chat extension; the bare word "copilot" would normalize to the CLI.
	"githubcopilotchat": "vscode_copilot",
}

// Identify names the harness from a user agent's leading product token
// ("claude-cli/2.1.306 (external, cli)" → claude_code 2.1.306). The name ends in
// asymptoteobserve.NormalizeHarnessName, so a gateway's events name a harness as every other
// collection path does.
func Identify(userAgent string) Harness {
	token, _, _ := strings.Cut(strings.TrimSpace(userAgent), " ")
	product, version, _ := strings.Cut(token, "/")
	product = strings.ToLower(product)
	if product == "" {
		return Harness{Name: "unknown"}
	}
	if name, ok := products[product]; ok {
		product = name
	}
	return Harness{Name: obs.NormalizeHarnessName(product), Version: version}
}

// Kind is the neutral action a tool call performs.
type Kind uint8

// Kinds.
const (
	// Other is a tool the bindings do not know.
	Other Kind = iota
	// Shell runs a command.
	Shell
	// Read reads a file.
	Read
	// Fetch fetches a URL.
	Fetch
	// MCP calls an MCP server's tool.
	MCP
)

// Call is a tool call bound to its neutral action.
type Call struct {
	Kind    Kind
	Name    string
	Command string // Shell
	Path    string // Read
	URL     string // Fetch
	Server  string // MCP
	Tool    string // MCP
}

// Bind recovers the neutral action of a tool call from its name and JSON arguments, using the
// tool names and argument keys of Claude Code, Codex, omp and OpenCode. Bindings key on the tool
// name, not the harness, because the names do not collide.
func Bind(name, arguments string) Call {
	var args map[string]any
	_ = json.Unmarshal([]byte(arguments), &args)
	c := Call{Name: name}
	switch {
	case strings.HasPrefix(name, "mcp__"):
		// Claude Code and Codex: mcp__<server>__<tool>.
		c.Kind = MCP
		c.Server, c.Tool, _ = strings.Cut(strings.TrimPrefix(name, "mcp__"), "__")
	case name == "Bash" || name == "bash" || name == "shell" || name == "local_shell" || name == "exec_command":
		c.Kind = Shell
		c.Command = command(args)
	case name == "Read" || name == "read" || name == "view_image":
		target := firstString(args, "file_path", "filePath", "path")
		if isURL(target) {
			// omp's read takes a URL as well as a path and returns the page.
			c.Kind, c.URL = Fetch, target
		} else {
			c.Kind, c.Path = Read, target
		}
	case name == "WebFetch" || name == "webfetch" || name == "fetch" || name == "web_fetch":
		c.Kind = Fetch
		c.URL = firstString(args, "url")
	}
	return c
}

// command reads a shell call's command: a string, or Codex's argv list.
func command(args map[string]any) string {
	if s := firstString(args, "command", "cmd"); s != "" {
		return s
	}
	argv, _ := args["command"].([]any)
	parts := make([]string, 0, len(argv))
	for _, a := range argv {
		if s, ok := a.(string); ok {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}

func firstString(args map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := args[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func isURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}
