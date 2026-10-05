package mcpconnect

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
)

// ServerName is the name Beacon registers the Beacon Cloud MCP server under in every harness.
// The local stdio server `beacon mcp serve` is "beacon"; the two names differ so neither entry
// overwrites the other.
const ServerName = "beacon-managed"

// TokenPage is where a person creates a personal Beacon Cloud MCP token.
const TokenPage = "beacon.sh → Dashboard → MCP Access"

// vscodeInputID names the VS Code input variable that holds a token in token mode.
const vscodeInputID = "beacon-managed-token"

// AuthMode is how a harness authenticates to Beacon Cloud MCP.
type AuthMode string

const (
	// AuthOAuth writes only the URL. The harness finds the authorization server from the MCP
	// server's 401 challenge, registers itself, and signs the user in in a browser. Beacon
	// writes no secret.
	AuthOAuth AuthMode = "oauth"
	// AuthTokenEnv writes a reference to an environment variable (or, for VS Code, a prompted
	// input) that holds a personal MCP token. The token value is never written.
	AuthTokenEnv AuthMode = "token-env"
	// AuthToken is an entry that carries a header written outside Beacon, which may hold a
	// token value. Beacon reports the mode and never reads the value out.
	AuthToken AuthMode = "token"
	// AuthManual means Beacon writes nothing and prints the steps instead.
	AuthManual AuthMode = "manual"
)

type configFormat int

const (
	formatJSON configFormat = iota
	formatTOML
)

// Target is one harness Beacon can register the server in, or print the steps for.
type Target struct {
	// Name is the harness's canonical discovery name (internal/endpoint/harness).
	Name        string
	DisplayName string
	// Aliases are the extra spellings --harness accepts, after normalization.
	Aliases []string

	// Automatic targets have a config Beacon writes. The rest are manual.
	Automatic bool

	format    configFormat
	path      func(home string) (string, error)
	container []string // path to the object holding servers, e.g. ["mcpServers"]
	entry     func(url, tokenEnv string) ordered
	// localEntry is the entry for the local stdio server `beacon mcp serve`, run by command.
	localEntry func(command string) ordered
	// skeleton is the content of a config Beacon creates, besides the server entry.
	skeleton ordered
	// cli names a CLI whose own `mcp add`/`mcp remove` Beacon prefers when it is on PATH.
	cli string
	// vscodeInputs is set for VS Code, whose token mode uses a prompted input.
	vscodeInputs bool

	oauthNext string
	tokenNext string // may contain %s for the variable name

	// manual renders the steps for a manual target.
	manual func(url string) string
}

// Targets is the catalog: every harness Beacon knows how to connect, automatic first.
func Targets() []Target {
	return []Target{
		{
			Name: "claude_code", DisplayName: "Claude Code", Aliases: []string{"claude", "claude-code"},
			Automatic: true, format: formatJSON, path: claudeConfigPath, container: []string{"mcpServers"},
			cli: "claude",
			entry: func(url, tokenEnv string) ordered {
				e := ordered{{"type", "http"}, {"url", url}}
				if tokenEnv != "" {
					e = append(e, field{"headers", ordered{{"Authorization", "Bearer ${" + tokenEnv + "}"}}})
				}
				return e
			},
			localEntry: func(command string) ordered {
				return ordered{{"type", "stdio"}, {"command", command}, {"args", localArgs()}}
			},
			oauthNext: "In Claude Code, run /mcp, choose " + ServerName + ", and sign in.",
			tokenNext: "Set %s in the environment Claude Code starts from.",
		},
		{
			Name: "codex_cli", DisplayName: "Codex CLI", Aliases: []string{"codex", "codex-cli"},
			Automatic: true, format: formatTOML, path: codexConfigPath, container: []string{"mcp_servers"},
			entry: func(url, tokenEnv string) ordered {
				e := ordered{{"url", url}}
				if tokenEnv != "" {
					e = append(e, field{"bearer_token_env_var", tokenEnv})
				}
				return e
			},
			localEntry: func(command string) ordered {
				return ordered{{"command", command}, {"args", localArgs()}}
			},
			oauthNext: "Run `codex mcp login " + ServerName + "`.",
			tokenNext: "Set %s in the environment Codex starts from.",
		},
		{
			Name: "cursor", DisplayName: "Cursor", Aliases: []string{"cursor"},
			Automatic: true, format: formatJSON, path: homePath(".cursor", "mcp.json"), container: []string{"mcpServers"},
			entry: func(url, tokenEnv string) ordered {
				e := ordered{{"url", url}}
				if tokenEnv != "" {
					e = append(e, field{"headers", ordered{{"Authorization", "Bearer ${env:" + tokenEnv + "}"}}})
				}
				return e
			},
			localEntry: func(command string) ordered {
				return ordered{{"command", command}, {"args", localArgs()}}
			},
			oauthNext: "In Cursor Settings → MCP, find " + ServerName + " (marked \"Needs login\") and sign in.",
			tokenNext: "Set %s in the environment Cursor starts from.",
		},
		{
			Name: "vscode", DisplayName: "VS Code", Aliases: []string{"vscode", "vs-code", "code"},
			Automatic: true, format: formatJSON, path: vscodeMCPPath, container: []string{"servers"},
			vscodeInputs: true,
			entry: func(url, tokenEnv string) ordered {
				e := ordered{{"type", "http"}, {"url", url}}
				if tokenEnv != "" {
					e = append(e, field{"headers", ordered{{"Authorization", "Bearer ${input:" + vscodeInputID + "}"}}})
				}
				return e
			},
			localEntry: func(command string) ordered {
				return ordered{{"type", "stdio"}, {"command", command}, {"args", localArgs()}}
			},
			oauthNext: "In VS Code, run \"MCP: List Servers\", start " + ServerName + ", and sign in.",
			tokenNext: "VS Code asks for the token the first time " + ServerName + " starts and keeps it in its secret storage; it does not read %s.",
		},
		{
			Name: "gemini_cli", DisplayName: "Gemini CLI", Aliases: []string{"gemini", "gemini-cli"},
			Automatic: true, format: formatJSON, path: homePath(".gemini", "settings.json"), container: []string{"mcpServers"},
			entry: func(url, tokenEnv string) ordered {
				e := ordered{{"url", url}, {"type", "http"}}
				if tokenEnv != "" {
					e = append(e, field{"headers", ordered{{"Authorization", "Bearer ${" + tokenEnv + "}"}}})
				}
				return e
			},
			localEntry: func(command string) ordered {
				return ordered{{"command", command}, {"args", localArgs()}}
			},
			oauthNext: "In Gemini CLI, run /mcp auth " + ServerName + ". Gemini CLI loads MCP servers only in trusted folders.",
			tokenNext: "Set %s in the environment Gemini CLI starts from. Gemini CLI loads MCP servers only in trusted folders.",
		},
		{
			Name: "opencode", DisplayName: "OpenCode", Aliases: []string{"opencode", "open-code"},
			Automatic: true, format: formatJSON, path: opencodeConfigPath, container: []string{"mcp"},
			skeleton: ordered{{"$schema", "https://opencode.ai/config.json"}},
			entry: func(url, tokenEnv string) ordered {
				e := ordered{{"type", "remote"}, {"url", url}, {"enabled", true}}
				if tokenEnv != "" {
					e = append(e, field{"oauth", false}, field{"headers", ordered{{"Authorization", "Bearer {env:" + tokenEnv + "}"}}})
				}
				return e
			},
			localEntry: func(command string) ordered {
				return ordered{{"type", "local"}, {"command", append([]any{command}, localArgs()...)}, {"enabled", true}}
			},
			oauthNext: "Run `opencode mcp auth " + ServerName + "`.",
			tokenNext: "Set %s in the environment OpenCode starts from.",
		},

		// Manual targets: no MCP OAuth support Beacon has confirmed, so Beacon writes nothing
		// and prints the steps. The snippets follow each runtime's documentation.
		{
			Name: "copilot_cli", DisplayName: "GitHub Copilot CLI", Aliases: []string{"copilot", "copilot-cli", "github-copilot-cli"},
			manual: func(url string) string {
				return "copilot mcp add --transport http " + ServerName + " " + url + " --header \"Authorization: Bearer <token>\""
			},
		},
		{
			Name: "cline", DisplayName: "Cline", Aliases: []string{"cline"},
			manual: func(url string) string {
				return "In Cline → MCP Servers → Configure, add to cline_mcp_settings.json:\n" +
					jsonSnippet("mcpServers", ordered{{"type", "streamableHttp"}, {"url", url}, {"headers", bearerPlaceholder()}})
			},
		},
		{
			Name: "kiro", DisplayName: "Kiro", Aliases: []string{"kiro", "kiro-ide", "kiro-cli"},
			manual: func(url string) string {
				return "Add to ~/.kiro/settings/mcp.json:\n" +
					jsonSnippet("mcpServers", ordered{{"url", url}, {"headers", bearerPlaceholder()}})
			},
		},
		{
			Name: "devin-desktop", DisplayName: "Devin Desktop", Aliases: []string{"devin-desktop", "windsurf"},
			manual: func(url string) string {
				return "Add to ~/.codeium/windsurf/mcp_config.json:\n" +
					jsonSnippet("mcpServers", ordered{{"serverUrl", url}, {"headers", bearerPlaceholder()}})
			},
		},
		{
			Name: "qwen_code", DisplayName: "Qwen Code", Aliases: []string{"qwen", "qwen-code"},
			manual: func(url string) string {
				return "Add to ~/.qwen/settings.json:\n" +
					jsonSnippet("mcpServers", ordered{{"httpUrl", url}, {"headers", bearerPlaceholder()}})
			},
		},
		{
			Name: "factory", DisplayName: "Factory Droid", Aliases: []string{"factory", "droid"},
			manual: func(url string) string {
				return "droid mcp add " + ServerName + " " + url + " --type http --header \"Authorization: Bearer <token>\""
			},
		},
		{
			Name: "hermes", DisplayName: "Hermes Agent", Aliases: []string{"hermes", "hermes-agent"},
			manual: func(url string) string {
				return "Add to ~/.hermes/config.yaml:\n" +
					"mcp_servers:\n  " + ServerName + ":\n    url: " + url + "\n    headers:\n      Authorization: \"Bearer <token>\""
			},
		},
		{
			Name: "kimi_code", DisplayName: "Kimi Code", Aliases: []string{"kimi", "kimi-code"},
			manual: func(url string) string {
				return "kimi mcp add --transport http " + ServerName + " " + url + " --header \"Authorization: Bearer <token>\""
			},
		},
		{
			Name: "antigravity_cli", DisplayName: "Antigravity CLI", Aliases: []string{"antigravity", "antigravity-cli"},
			manual: func(url string) string {
				return "Add to ~/.gemini/antigravity/mcp_config.json:\n" +
					jsonSnippet("mcpServers", ordered{{"serverUrl", url}, {"headers", bearerPlaceholder()}})
			},
		},
	}
}

func bearerPlaceholder() ordered {
	return ordered{{"Authorization", "Bearer <token>"}}
}

func jsonSnippet(container string, entry ordered) string {
	doc := ordered{{container, ordered{{ServerName, entry}}}}
	return strings.TrimRight(renderJSONDocument(doc, jsonStyle{unit: "  ", eol: "\n"}), "\n")
}

// NormalizeKey folds a harness spelling the way `beacon endpoint` target names are folded.
func NormalizeKey(name string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "_", "-")
}

// Lookup finds a target by its canonical name or an alias.
func Lookup(name string) (Target, bool) {
	key := NormalizeKey(name)
	if key == "" {
		return Target{}, false
	}
	for _, t := range Targets() {
		if NormalizeKey(t.Name) == key {
			return t, true
		}
		for _, alias := range t.Aliases {
			if NormalizeKey(alias) == key {
				return t, true
			}
		}
	}
	return Target{}, false
}

// ConfigPath is the file an automatic target's entry lives in.
func (t Target) ConfigPath(home string) (string, error) {
	if !t.Automatic {
		return "", errors.New("manual target has no config Beacon writes")
	}
	return t.path(home)
}

func homePath(parts ...string) func(string) (string, error) {
	return func(home string) (string, error) {
		return filepath.Join(append([]string{home}, parts...)...), nil
	}
}

// claudeConfigPath is the file `claude mcp add --scope user` writes: ~/.claude.json, or
// .claude.json inside CLAUDE_CONFIG_DIR when that is set.
func claudeConfigPath(home string) (string, error) {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return filepath.Join(dir, ".claude.json"), nil
	}
	return filepath.Join(home, ".claude.json"), nil
}

// codexConfigPath honors CODEX_HOME, as Codex does.
func codexConfigPath(home string) (string, error) {
	if dir := strings.TrimSpace(os.Getenv("CODEX_HOME")); dir != "" {
		return filepath.Join(dir, "config.toml"), nil
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

// vscodeMCPPath is the user-profile mcp.json, next to the settings.json VS Code discovery uses.
func vscodeMCPPath(string) (string, error) {
	settings, err := harness.VSCodeUserSettingsPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(settings), "mcp.json"), nil
}

// opencodeConfigPath picks the global config OpenCode's own `opencode mcp add` writes to: an
// existing opencode.json, else an existing opencode.jsonc, else a new opencode.jsonc. (Checked
// against opencode 1.18: with both files present it writes opencode.json.)
func opencodeConfigPath(home string) (string, error) {
	base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if base == "" {
		base = filepath.Join(home, ".config")
	}
	dir := filepath.Join(base, "opencode")
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && !info.IsDir() {
			return filepath.Join(dir, name), nil
		}
	}
	return filepath.Join(dir, "opencode.jsonc"), nil
}

// NextStep is what the person does after connect for an automatic target.
func (t Target) NextStep(auth AuthMode, tokenEnv string) string {
	if auth == AuthTokenEnv {
		if strings.Contains(t.tokenNext, "%s") {
			return fmt.Sprintf(t.tokenNext, tokenEnv)
		}
		return t.tokenNext
	}
	return t.oauthNext
}

// ManualSteps is what the person does by hand for a manual target.
func (t Target) ManualSteps(url string) string {
	if t.manual == nil {
		return ""
	}
	return "Create a token at " + TokenPage + ", then:\n" + t.manual(url)
}
