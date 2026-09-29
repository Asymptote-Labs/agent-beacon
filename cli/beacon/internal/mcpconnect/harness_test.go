package mcpconnect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

const testURL = "https://mcp.example.test"

// harnessFixture is one automatic target's test data: an existing config full of things Beacon
// must not touch, and the exact bytes connect must produce from it.
type harnessFixture struct {
	name string
	// existing is a realistic config with unrelated servers, comments where the format allows
	// them, and the harness's own formatting.
	existing string
	// wantExisting is existing after `connect` in OAuth mode.
	wantExisting string
	// wantNew is a config connect creates from nothing, in OAuth mode.
	wantNew string
	// wantToken is a config connect creates from nothing with --token-env BEACON_MCP_TOKEN.
	wantToken string
	// conflict is a config with a beacon-managed entry pointing somewhere else.
	conflict string
}

var harnessFixtures = []harnessFixture{
	{
		name: "claude_code",
		existing: `{
  "numStartups": 12,
  "mcpServers": {
    "beacon": {
      "type": "stdio",
      "command": "beacon",
      "args": [
        "mcp",
        "serve"
      ]
    }
  },
  "projects": {
    "/work/repo": {
      "allowedTools": []
    }
  }
}
`,
		wantExisting: `{
  "numStartups": 12,
  "mcpServers": {
    "beacon": {
      "type": "stdio",
      "command": "beacon",
      "args": [
        "mcp",
        "serve"
      ]
    },
    "beacon-managed": {
      "type": "http",
      "url": "https://mcp.example.test"
    }
  },
  "projects": {
    "/work/repo": {
      "allowedTools": []
    }
  }
}
`,
		wantNew: `{
  "mcpServers": {
    "beacon-managed": {
      "type": "http",
      "url": "https://mcp.example.test"
    }
  }
}
`,
		wantToken: `{
  "mcpServers": {
    "beacon-managed": {
      "type": "http",
      "url": "https://mcp.example.test",
      "headers": {
        "Authorization": "Bearer ${BEACON_MCP_TOKEN}"
      }
    }
  }
}
`,
		conflict: `{"mcpServers": {"beacon-managed": {"type": "http", "url": "https://elsewhere.example"}}}`,
	},
	{
		name: "codex_cli",
		existing: `# Codex settings
model = "gpt-5-codex"
approval_policy = "on-request" # ask first

[mcp_servers.docs]
command = "docs-mcp"
args = ["--stdio"]

[profiles.work]
model = "o3"
`,
		wantExisting: `# Codex settings
model = "gpt-5-codex"
approval_policy = "on-request" # ask first

[mcp_servers.docs]
command = "docs-mcp"
args = ["--stdio"]

[profiles.work]
model = "o3"

[mcp_servers.beacon-managed]
url = "https://mcp.example.test"
`,
		wantNew: `[mcp_servers.beacon-managed]
url = "https://mcp.example.test"
`,
		wantToken: `[mcp_servers.beacon-managed]
url = "https://mcp.example.test"
bearer_token_env_var = "BEACON_MCP_TOKEN"
`,
		conflict: "[mcp_servers.beacon-managed]\nurl = \"https://elsewhere.example\"\n",
	},
	{
		name: "cursor",
		existing: `{
    "mcpServers": {
        "github": {
            "url": "https://api.githubcopilot.com/mcp/",
            "headers": {
                "Authorization": "Bearer ${env:GITHUB_TOKEN}"
            }
        }
    }
}
`,
		wantExisting: `{
    "mcpServers": {
        "github": {
            "url": "https://api.githubcopilot.com/mcp/",
            "headers": {
                "Authorization": "Bearer ${env:GITHUB_TOKEN}"
            }
        },
        "beacon-managed": {
            "url": "https://mcp.example.test"
        }
    }
}
`,
		wantNew: `{
  "mcpServers": {
    "beacon-managed": {
      "url": "https://mcp.example.test"
    }
  }
}
`,
		wantToken: `{
  "mcpServers": {
    "beacon-managed": {
      "url": "https://mcp.example.test",
      "headers": {
        "Authorization": "Bearer ${env:BEACON_MCP_TOKEN}"
      }
    }
  }
}
`,
		conflict: `{"mcpServers":{"beacon-managed":{"url":"https://elsewhere.example"}}}`,
	},
	{
		name: "vscode",
		existing: `{
	// Servers for the whole profile.
	"servers": {
		"memory": {
			"type": "stdio",
			"command": "npx",
			"args": ["-y", "@modelcontextprotocol/server-memory"], // pinned later
		},
	},
	"inputs": [],
}
`,
		wantExisting: `{
	// Servers for the whole profile.
	"servers": {
		"memory": {
			"type": "stdio",
			"command": "npx",
			"args": ["-y", "@modelcontextprotocol/server-memory"], // pinned later
		},
		"beacon-managed": {
			"type": "http",
			"url": "https://mcp.example.test"
		},
	},
	"inputs": [],
}
`,
		wantNew: `{
  "servers": {
    "beacon-managed": {
      "type": "http",
      "url": "https://mcp.example.test"
    }
  }
}
`,
		wantToken: `{
  "servers": {
    "beacon-managed": {
      "type": "http",
      "url": "https://mcp.example.test",
      "headers": {
        "Authorization": "Bearer ${input:beacon-managed-token}"
      }
    }
  },
  "inputs": [
    {
      "type": "promptString",
      "id": "beacon-managed-token",
      "description": "Beacon Cloud MCP token (beacon.sh → Dashboard → MCP Access)",
      "password": true
    }
  ]
}
`,
		conflict: `{"servers": {"beacon-managed": {"type": "http", "url": "https://elsewhere.example"}}}`,
	},
	{
		name: "gemini_cli",
		existing: `{
  "security": {
    "auth": {
      "selectedType": "oauth-personal"
    }
  },
  "ui": {
    "theme": "GitHub"
  }
}
`,
		wantExisting: `{
  "security": {
    "auth": {
      "selectedType": "oauth-personal"
    }
  },
  "ui": {
    "theme": "GitHub"
  },
  "mcpServers": {
    "beacon-managed": {
      "url": "https://mcp.example.test",
      "type": "http"
    }
  }
}
`,
		wantNew: `{
  "mcpServers": {
    "beacon-managed": {
      "url": "https://mcp.example.test",
      "type": "http"
    }
  }
}
`,
		wantToken: `{
  "mcpServers": {
    "beacon-managed": {
      "url": "https://mcp.example.test",
      "type": "http",
      "headers": {
        "Authorization": "Bearer ${BEACON_MCP_TOKEN}"
      }
    }
  }
}
`,
		conflict: `{"mcpServers": {"beacon-managed": {"httpUrl": "https://elsewhere.example", "url": "https://elsewhere.example"}}}`,
	},
	{
		name: "opencode",
		existing: `{
  "$schema": "https://opencode.ai/config.json",
  // Plugins and servers.
  "plugin": ["./plugins/beacon.ts"],
  "mcp": {
    "context7": {
      "type": "remote",
      "url": "https://mcp.context7.com/mcp",
      "enabled": true,
    },
  },
}
`,
		wantExisting: `{
  "$schema": "https://opencode.ai/config.json",
  // Plugins and servers.
  "plugin": ["./plugins/beacon.ts"],
  "mcp": {
    "context7": {
      "type": "remote",
      "url": "https://mcp.context7.com/mcp",
      "enabled": true,
    },
    "beacon-managed": {
      "type": "remote",
      "url": "https://mcp.example.test",
      "enabled": true
    },
  },
}
`,
		wantNew: `{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "beacon-managed": {
      "type": "remote",
      "url": "https://mcp.example.test",
      "enabled": true
    }
  }
}
`,
		wantToken: `{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "beacon-managed": {
      "type": "remote",
      "url": "https://mcp.example.test",
      "enabled": true,
      "oauth": false,
      "headers": {
        "Authorization": "Bearer {env:BEACON_MCP_TOKEN}"
      }
    }
  }
}
`,
		conflict: `{"mcp": {"beacon-managed": {"type": "remote", "url": "https://elsewhere.example"}}}`,
	},
}

// isolate points every harness path at a fresh home and returns it.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	return home
}

func fileOptions(home string) Options {
	return Options{
		Home: home, URL: testURL,
		// The file writers are under test here; the Claude CLI path has its own tests.
		LookPath: func(string) (string, error) { return "", errors.New("not on PATH") },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("no CLI may run in file-writer tests")
		},
		Now: func() time.Time { return time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC) },
	}
}

func mustTarget(t *testing.T, name string) Target {
	t.Helper()
	target, ok := Lookup(name)
	if !ok {
		t.Fatalf("no target %q", name)
	}
	return target
}

func targetPath(t *testing.T, target Target, home string) string {
	t.Helper()
	path, err := target.ConfigPath(home)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func connect(t *testing.T, opts Options, target Target) Item {
	t.Helper()
	plan, err := Plan(opts, []Target{target})
	if err != nil {
		t.Fatal(err)
	}
	applied, err := Apply(context.Background(), opts, plan)
	if err != nil {
		t.Fatal(err)
	}
	if applied[0].Err != nil {
		t.Fatalf("apply %s: %v", target.Name, applied[0].Err)
	}
	return applied[0]
}

func disconnect(t *testing.T, opts Options, target Target) Item {
	t.Helper()
	items, err := Disconnect(context.Background(), opts, []Target{target})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("disconnect returned %d items", len(items))
	}
	if items[0].Err != nil {
		t.Fatalf("disconnect %s: %v", target.Name, items[0].Err)
	}
	return items[0]
}

func TestHarnessConnectWritesExactlyTheExpectedConfig(t *testing.T) {
	for _, fx := range harnessFixtures {
		t.Run(fx.name, func(t *testing.T) {
			target := mustTarget(t, fx.name)

			t.Run("missing file is created, and removed again", func(t *testing.T) {
				home := isolate(t)
				opts := fileOptions(home)
				path := targetPath(t, target, home)
				if got := connect(t, opts, target); got.Action != ActionAdd {
					t.Fatalf("action = %s, want add", got.Action)
				}
				if got := readFile(t, path); got != fx.wantNew {
					t.Fatalf("new config:\n%s\nwant:\n%s", got, fx.wantNew)
				}
				if testenv.HasPOSIXFileModes() {
					info, _ := os.Stat(path)
					if info.Mode().Perm() != 0o600 {
						t.Errorf("a config Beacon creates is %v, want 0600", info.Mode().Perm())
					}
				}
				if got := disconnect(t, opts, target); got.Action != ActionRemove {
					t.Fatalf("disconnect action = %s (%s)", got.Action, got.Detail)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("disconnect left the file connect created: %v", err)
				}
			})

			t.Run("existing file keeps everything else, byte for byte", func(t *testing.T) {
				home := isolate(t)
				opts := fileOptions(home)
				path := targetPath(t, target, home)
				writeFile(t, path, fx.existing)
				connect(t, opts, target)
				if got := readFile(t, path); got != fx.wantExisting {
					t.Fatalf("connected config:\n%s\nwant:\n%s", got, fx.wantExisting)
				}
				if testenv.HasPOSIXFileModes() {
					info, _ := os.Stat(path)
					if info.Mode().Perm() != 0o644 {
						t.Errorf("connect changed the file's mode to %v", info.Mode().Perm())
					}
				}
				backups, _ := filepath.Glob(path + ".beacon.*.bak")
				if len(backups) != 1 || readFile(t, backups[0]) != fx.existing {
					t.Fatalf("expected one backup holding the original, got %v", backups)
				}
				disconnect(t, opts, target)
				if got := readFile(t, path); got != fx.existing {
					t.Fatalf("disconnect did not restore the original:\n%s", got)
				}
			})

			t.Run("disconnect after the user edited the file removes only Beacon's entry", func(t *testing.T) {
				home := isolate(t)
				opts := fileOptions(home)
				path := targetPath(t, target, home)
				writeFile(t, path, fx.existing)
				connect(t, opts, target)
				// An unrelated edit the user makes afterwards, so the backup is stale and the
				// entry has to be spliced out.
				edited := strings.Replace(readFile(t, path), fx.existing[:1], fx.existing[:1]+userEdit(target), 1)
				writeFile(t, path, edited)
				disconnect(t, opts, target)
				want := strings.Replace(fx.existing, fx.existing[:1], fx.existing[:1]+userEdit(target), 1)
				if got := readFile(t, path); got != want {
					t.Fatalf("got:\n%s\nwant:\n%s", got, want)
				}
			})

			t.Run("re-running connect changes nothing", func(t *testing.T) {
				home := isolate(t)
				opts := fileOptions(home)
				path := targetPath(t, target, home)
				writeFile(t, path, fx.existing)
				connect(t, opts, target)
				first := readFile(t, path)
				manifestBefore := readFile(t, manifestPath(home))
				plan, err := Plan(opts, []Target{target})
				if err != nil {
					t.Fatal(err)
				}
				if plan[0].Action != ActionPresent {
					t.Fatalf("second plan = %s, want present", plan[0].Action)
				}
				if _, err := Apply(context.Background(), opts, plan); err != nil {
					t.Fatal(err)
				}
				if readFile(t, path) != first || readFile(t, manifestPath(home)) != manifestBefore {
					t.Fatal("a no-op connect rewrote the config or the manifest")
				}
				if backups, _ := filepath.Glob(path + ".beacon.*.bak"); len(backups) != 1 {
					t.Fatalf("a no-op connect made another backup: %v", backups)
				}
			})

			t.Run("token-env references the variable and never a value", func(t *testing.T) {
				home := isolate(t)
				opts := fileOptions(home)
				opts.TokenEnv = "BEACON_MCP_TOKEN"
				t.Setenv("BEACON_MCP_TOKEN", "bcn_mcp_must_never_be_written")
				path := targetPath(t, target, home)
				connect(t, opts, target)
				got := readFile(t, path)
				if got != fx.wantToken {
					t.Fatalf("token config:\n%s\nwant:\n%s", got, fx.wantToken)
				}
				if strings.Contains(got, "bcn_mcp_") {
					t.Fatal("the token value was written")
				}
				disconnect(t, opts, target)
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("disconnect left the token-mode file connect created")
				}
			})

			t.Run("switching auth mode updates Beacon's own entry", func(t *testing.T) {
				home := isolate(t)
				opts := fileOptions(home)
				path := targetPath(t, target, home)
				writeFile(t, path, fx.existing)
				connect(t, opts, target)
				tokenOpts := opts
				tokenOpts.TokenEnv = "BEACON_MCP_TOKEN"
				if got := connect(t, tokenOpts, target); got.Action != ActionUpdate {
					t.Fatalf("action = %s, want update", got.Action)
				}
				back := connect(t, opts, target)
				if back.Action != ActionUpdate {
					t.Fatalf("action = %s, want update", back.Action)
				}
				if got := readFile(t, path); got != fx.wantExisting {
					t.Fatalf("switching back did not restore the OAuth entry:\n%s", got)
				}
				disconnect(t, opts, target)
				if got := readFile(t, path); got != fx.existing {
					t.Fatalf("disconnect after updates did not restore the original:\n%s", got)
				}
			})

			t.Run("a conflicting entry is left alone without --force", func(t *testing.T) {
				home := isolate(t)
				opts := fileOptions(home)
				path := targetPath(t, target, home)
				writeFile(t, path, fx.conflict)
				plan, err := Plan(opts, []Target{target})
				if err != nil {
					t.Fatal(err)
				}
				if plan[0].Action != ActionConflict || plan[0].ExistingURL != "https://elsewhere.example" {
					t.Fatalf("plan = %s (%q)", plan[0].Action, plan[0].ExistingURL)
				}
				if _, err := Apply(context.Background(), opts, plan); err != nil {
					t.Fatal(err)
				}
				if readFile(t, path) != fx.conflict {
					t.Fatal("a conflict was overwritten without --force")
				}
				if got := disconnect(t, opts, target); got.Action != ActionAbsent || readFile(t, path) != fx.conflict {
					t.Fatal("disconnect touched an entry Beacon did not write")
				}

				opts.Force = true
				if got := connect(t, opts, target); got.Action != ActionReplace {
					t.Fatalf("forced action = %s", got.Action)
				}
				st, _ := inspect(target, path)
				if !sameURL(st.url, testURL) {
					t.Fatalf("--force did not replace the entry; url = %s", st.url)
				}
				disconnect(t, fileOptions(home), target)
				if got := readFile(t, path); got != fx.conflict {
					t.Fatalf("disconnect after --force should restore the pre-connect file:\n%s", got)
				}
			})

			t.Run("an identical entry Beacon did not write is left as is", func(t *testing.T) {
				home := isolate(t)
				opts := fileOptions(home)
				path := targetPath(t, target, home)
				writeFile(t, path, fx.wantNew)
				plan, err := Plan(opts, []Target{target})
				if err != nil {
					t.Fatal(err)
				}
				if plan[0].Action != ActionPresent {
					t.Fatalf("plan = %s", plan[0].Action)
				}
				if got := disconnect(t, opts, target); got.Action != ActionAbsent {
					t.Fatalf("disconnect action = %s", got.Action)
				}
				if readFile(t, path) != fx.wantNew {
					t.Fatal("disconnect removed an entry the user wrote")
				}
			})
		})
	}
}

// userEdit is a harmless edit placed right after the first byte of a fixture.
func userEdit(target Target) string {
	if target.format == formatTOML {
		return " user edit\n#"
	}
	return "\n  \"userEdit\": true,"
}

func TestUnparseableConfigsAreSkippedAndLeftAlone(t *testing.T) {
	for _, name := range []string{"claude_code", "codex_cli", "cursor", "vscode", "gemini_cli", "opencode"} {
		t.Run(name, func(t *testing.T) {
			home := isolate(t)
			target := mustTarget(t, name)
			path := targetPath(t, target, home)
			broken := "{ this is [ not valid\n"
			writeFile(t, path, broken)
			plan, err := Plan(fileOptions(home), []Target{target})
			if err != nil {
				t.Fatal(err)
			}
			if plan[0].Action != ActionSkip || plan[0].Detail == "" {
				t.Fatalf("plan = %s (%s)", plan[0].Action, plan[0].Detail)
			}
			if _, err := Apply(context.Background(), fileOptions(home), plan); err != nil {
				t.Fatal(err)
			}
			if readFile(t, path) != broken {
				t.Fatal("an unparseable config was rewritten")
			}
		})
	}
}

func TestCodexInlineTableIsNotRewritten(t *testing.T) {
	home := isolate(t)
	target := mustTarget(t, "codex")
	path := targetPath(t, target, home)
	inline := "mcp_servers = { beacon-managed = { url = \"https://elsewhere.example\" } }\n"
	writeFile(t, path, inline)
	opts := fileOptions(home)
	opts.Force = true
	plan, err := Plan(opts, []Target{target})
	if err != nil {
		t.Fatal(err)
	}
	applied, _ := Apply(context.Background(), opts, plan)
	if applied[0].Err == nil {
		t.Fatal("an inline table Beacon cannot splice was reported as replaced")
	}
	if readFile(t, path) != inline {
		t.Fatal("the inline table was rewritten")
	}
}

func TestCodexHonorsCodexHome(t *testing.T) {
	home := isolate(t)
	codexHome := filepath.Join(home, "elsewhere")
	t.Setenv("CODEX_HOME", codexHome)
	connect(t, fileOptions(home), mustTarget(t, "codex"))
	if !strings.Contains(readFile(t, filepath.Join(codexHome, "config.toml")), "[mcp_servers.beacon-managed]") {
		t.Fatal("CODEX_HOME was not honored")
	}
}

func TestClaudeHonorsClaudeConfigDir(t *testing.T) {
	home := isolate(t)
	dir := filepath.Join(home, "claude-config")
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	connect(t, fileOptions(home), mustTarget(t, "claude"))
	if !strings.Contains(readFile(t, filepath.Join(dir, ".claude.json")), ServerName) {
		t.Fatal("CLAUDE_CONFIG_DIR was not honored")
	}
}

func TestOpenCodePrefersAnExistingConfigFile(t *testing.T) {
	home := isolate(t)
	dir := filepath.Join(home, ".config", "opencode")
	writeFile(t, filepath.Join(dir, "opencode.json"), "{\n  \"$schema\": \"https://opencode.ai/config.json\"\n}\n")
	target := mustTarget(t, "opencode")
	if got := targetPath(t, target, home); got != filepath.Join(dir, "opencode.json") {
		t.Fatalf("path = %s, want the existing opencode.json", got)
	}
	writeFile(t, filepath.Join(dir, "opencode.jsonc"), "{}\n")
	if got := targetPath(t, target, home); got != filepath.Join(dir, "opencode.json") {
		t.Fatalf("path = %s, want opencode.json when both exist, as `opencode mcp add` chooses", got)
	}
	if err := os.Remove(filepath.Join(dir, "opencode.json")); err != nil {
		t.Fatal(err)
	}
	if got := targetPath(t, target, home); got != filepath.Join(dir, "opencode.jsonc") {
		t.Fatalf("path = %s, want the existing opencode.jsonc", got)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	if got := targetPath(t, target, home); got != filepath.Join(home, "xdg", "opencode", "opencode.jsonc") {
		t.Fatalf("XDG_CONFIG_HOME was not honored: %s", got)
	}
}

func TestSymlinkedConfigIsWrittenThroughTheLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	home := isolate(t)
	target := mustTarget(t, "cursor")
	path := targetPath(t, target, home)
	real := filepath.Join(home, "dotfiles", "mcp.json")
	writeFile(t, real, "{}\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, path); err != nil {
		t.Fatal(err)
	}
	connect(t, fileOptions(home), target)
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("connect replaced the symlink with a file")
	}
	if !strings.Contains(readFile(t, real), ServerName) {
		t.Fatal("the link target was not updated")
	}
	disconnect(t, fileOptions(home), target)
	if readFile(t, real) != "{}\n" {
		t.Fatalf("disconnect did not restore the link target: %q", readFile(t, real))
	}
}

func TestBlankExistingFileIsRestoredBlank(t *testing.T) {
	home := isolate(t)
	target := mustTarget(t, "gemini")
	path := targetPath(t, target, home)
	writeFile(t, path, "")
	connect(t, fileOptions(home), target)
	disconnect(t, fileOptions(home), target)
	if got := readFile(t, path); got != "" {
		t.Fatalf("got %q, want the original blank file", got)
	}
}

func TestVSCodeInputConflictNeedsForce(t *testing.T) {
	home := isolate(t)
	target := mustTarget(t, "vscode")
	path := targetPath(t, target, home)
	doc := `{"servers": {}, "inputs": [{"type": "promptString", "id": "beacon-managed-token", "description": "mine"}]}`
	writeFile(t, path, doc)
	opts := fileOptions(home)
	opts.TokenEnv = "BEACON_MCP_TOKEN"
	plan, _ := Plan(opts, []Target{target})
	if plan[0].Action != ActionConflict {
		t.Fatalf("plan = %s", plan[0].Action)
	}
	// OAuth mode does not use the input, so it is not a conflict there.
	plan, _ = Plan(fileOptions(home), []Target{target})
	if plan[0].Action != ActionAdd {
		t.Fatalf("oauth plan = %s", plan[0].Action)
	}
}

func TestManifestIsPrivateAndHoldsNoSecret(t *testing.T) {
	home := isolate(t)
	opts := fileOptions(home)
	opts.TokenEnv = "BEACON_MCP_TOKEN"
	t.Setenv("BEACON_MCP_TOKEN", "bcn_mcp_secret_value")
	connect(t, opts, mustTarget(t, "codex"))
	data := readFile(t, manifestPath(home))
	if strings.Contains(data, "bcn_mcp_secret_value") {
		t.Fatal("the manifest holds a token value")
	}
	if !strings.Contains(data, `"token_env": "BEACON_MCP_TOKEN"`) {
		t.Fatalf("manifest does not record the variable name:\n%s", data)
	}
	if testenv.HasPOSIXFileModes() {
		info, _ := os.Stat(manifestPath(home))
		dir, _ := os.Stat(filepath.Dir(manifestPath(home)))
		if info.Mode().Perm() != 0o600 || dir.Mode().Perm() != 0o700 {
			t.Fatalf("manifest %v in %v, want 0600 in 0700", info.Mode().Perm(), dir.Mode().Perm())
		}
	}
	disconnect(t, opts, mustTarget(t, "codex"))
	if _, err := os.Stat(manifestPath(home)); !os.IsNotExist(err) {
		t.Fatal("an empty manifest was left behind")
	}
}

func TestDisconnectLeavesAnEntryWhoseURLTheUserChanged(t *testing.T) {
	home := isolate(t)
	target := mustTarget(t, "cursor")
	path := targetPath(t, target, home)
	connect(t, fileOptions(home), target)
	changed := strings.Replace(readFile(t, path), testURL, "https://theirs.example", 1)
	writeFile(t, path, changed)
	if got := disconnect(t, fileOptions(home), target); got.Action != ActionAbsent {
		t.Fatalf("action = %s", got.Action)
	}
	if readFile(t, path) != changed {
		t.Fatal("disconnect removed an entry that no longer points at Beacon's URL")
	}
}

func TestLookupAcceptsEndpointTargetSpellings(t *testing.T) {
	for spelling, want := range map[string]string{
		"claude": "claude_code", "Claude_Code": "claude_code", "codex": "codex_cli", "codex-cli": "codex_cli",
		"gemini": "gemini_cli", "vs-code": "vscode", "opencode": "opencode", "cursor": "cursor",
		"copilot": "copilot_cli", "droid": "factory", "windsurf": "devin-desktop",
	} {
		got, ok := Lookup(spelling)
		if !ok || got.Name != want {
			t.Errorf("Lookup(%q) = %q, %v; want %q", spelling, got.Name, ok, want)
		}
	}
	if _, ok := Lookup("nonsense"); ok {
		t.Error("an unknown harness was accepted")
	}
}

func TestManualTargetsNeverWriteAndNeverPrintAToken(t *testing.T) {
	home := isolate(t)
	for _, target := range Targets() {
		if target.Automatic {
			continue
		}
		plan, err := Plan(fileOptions(home), []Target{target})
		if err != nil {
			t.Fatal(err)
		}
		if plan[0].Action != ActionManual || plan[0].Writes() {
			t.Fatalf("%s: manual target planned %s", target.Name, plan[0].Action)
		}
		steps := plan[0].NextStep(fileOptions(home))
		if !strings.Contains(steps, testURL) || !strings.Contains(steps, "<token>") || !strings.Contains(steps, TokenPage) {
			t.Errorf("%s: steps lack the URL, the placeholder or the token page:\n%s", target.Name, steps)
		}
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 0 {
		t.Fatalf("manual targets wrote into the home directory: %v", entries)
	}
}

func TestCreatedFileIsKeptWhenTheUserAddedToIt(t *testing.T) {
	home := isolate(t)
	target := mustTarget(t, "opencode")
	path := targetPath(t, target, home)
	connect(t, fileOptions(home), target)
	withTheirs, _, err := insertJSONMember(readFile(t, path), []string{"mcp"}, "theirs", ordered{{"type", "remote"}, {"url", "https://t"}})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, withTheirs)
	disconnect(t, fileOptions(home), target)
	got, err := decodeJSONC(readFile(t, path))
	if err != nil {
		t.Fatal(err)
	}
	servers, _ := got["mcp"].(map[string]any)
	if servers["theirs"] == nil || servers[ServerName] != nil {
		t.Fatalf("got %v", got)
	}

	// A created file whose only change since connect is formatting is still Beacon's to delete.
	home = isolate(t)
	path = targetPath(t, target, home)
	connect(t, fileOptions(home), target)
	writeFile(t, path, readFile(t, path)+"\n")
	disconnect(t, fileOptions(home), target)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a created file holding only Beacon's skeleton was left behind")
	}
}

func TestInspectReportsModeAndOwnershipWithoutValues(t *testing.T) {
	home := isolate(t)
	opts := fileOptions(home)
	opts.TokenEnv = "BEACON_MCP_TOKEN"
	connect(t, opts, mustTarget(t, "codex"))
	cursor := mustTarget(t, "cursor")
	writeFile(t, targetPath(t, cursor, home), `{"mcpServers": {"beacon-managed": {"url": "https://mcp.example.test", "headers": {"Authorization": "Bearer bcn_mcp_literal"}}}}`)
	statuses, err := Inspect(fileOptions(home), []Target{mustTarget(t, "codex"), cursor, mustTarget(t, "gemini"), mustTarget(t, "copilot")})
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		configured, byBeacon bool
		auth                 AuthMode
	}
	want := []row{{true, true, AuthTokenEnv}, {true, false, AuthToken}, {false, false, ""}, {false, false, AuthManual}}
	for i, s := range statuses {
		if got := (row{s.Configured, s.ByBeacon, s.Auth}); got != want[i] {
			t.Errorf("%s: %+v, want %+v", s.Target.Name, got, want[i])
		}
		if strings.Contains(s.Detail+s.URL, "bcn_mcp_") {
			t.Errorf("%s: status exposes a header value", s.Target.Name)
		}
	}
}

func TestNextStepsNameTheServerOrTheVariable(t *testing.T) {
	for _, target := range Targets() {
		if !target.Automatic {
			continue
		}
		if step := target.NextStep(AuthOAuth, ""); step == "" {
			t.Errorf("%s has no OAuth next step", target.Name)
		}
		if step := target.NextStep(AuthTokenEnv, "MY_TOKEN_VAR"); !strings.Contains(step, "MY_TOKEN_VAR") && !strings.Contains(step, "secret storage") {
			t.Errorf("%s token step does not name the variable: %q", target.Name, step)
		}
	}
}

// Regression: a Codex config whose only MCP server is Beacon's has no [mcp_servers] table left
// once Beacon's is stripped, which the disconnect check must accept.
func TestCodexDisconnectAfterEditWhenBeaconsIsTheOnlyServer(t *testing.T) {
	home := isolate(t)
	target := mustTarget(t, "codex")
	path := targetPath(t, target, home)
	writeFile(t, path, "model = \"o3\"\n")
	connect(t, fileOptions(home), target)
	writeFile(t, path, "# edited later\n"+readFile(t, path))
	if got := disconnect(t, fileOptions(home), target); got.Action != ActionRemove {
		t.Fatalf("action = %s (%s)", got.Action, got.Detail)
	}
	if got := readFile(t, path); got != "# edited later\nmodel = \"o3\"\n" {
		t.Fatalf("got %q", got)
	}
}

// Regression: an update made after the user edited the file must not leave a backup that holds
// Beacon's previous entry as the thing disconnect restores.
func TestDisconnectAfterUpdateOnAnEditedFileRemovesTheEntry(t *testing.T) {
	for _, name := range []string{"cursor", "codex"} {
		t.Run(name, func(t *testing.T) {
			home := isolate(t)
			target := mustTarget(t, name)
			path := targetPath(t, target, home)
			original := harnessFixtures[fixtureIndex(name)].existing
			writeFile(t, path, original)
			connect(t, fileOptions(home), target)
			edit := strings.Replace(readFile(t, path), original[:1], original[:1]+userEdit(target), 1)
			writeFile(t, path, edit)
			tokenOpts := fileOptions(home)
			tokenOpts.TokenEnv = "BEACON_MCP_TOKEN"
			if got := connect(t, tokenOpts, target); got.Action != ActionUpdate {
				t.Fatalf("action = %s", got.Action)
			}
			disconnect(t, fileOptions(home), target)
			want := strings.Replace(original, original[:1], original[:1]+userEdit(target), 1)
			if got := readFile(t, path); got != want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func fixtureIndex(name string) int {
	target, _ := Lookup(name)
	for i, fx := range harnessFixtures {
		if fx.name == target.Name {
			return i
		}
	}
	panic(name)
}
