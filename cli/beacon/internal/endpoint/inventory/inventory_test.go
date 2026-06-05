package inventory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanCurrentUserMCPInventory(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
      "env": {"NODE_ENV": "production"}
    }
  }
}`)
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), `
[mcp_servers.github]
command = "gh"
args = ["mcp", "serve"]

[mcp_servers.github.env]
GITHUB_TOKEN = "secret"
`)
	writeFile(t, filepath.Join(home, ".cursor", "mcp.json"), `{
  "mcpServers": {
    "remote": {
      "url": "https://example.test/sse",
      "transport": "sse"
    }
  }
}`)

	result := Scan(Options{
		ContentRetention: RedactionRedacted,
		HomeDir:          home,
		WorkingDir:       work,
		Now:              fixedNow,
	})

	if got, want := len(result.MCPServers), 3; got != want {
		t.Fatalf("MCPServers len = %d, want %d: %#v", got, want, result.MCPServers)
	}
	assertServer(t, result.MCPServers, "claude_code", "filesystem", TransportStdio, true, 3, 1, 1)
	assertServer(t, result.MCPServers, "codex_cli", "github", TransportStdio, true, 2, 0, 1)
	assertServer(t, result.MCPServers, "cursor", "remote", TransportSSE, false, 0, 0, 0)

	foundClaudeConfig := false
	for _, config := range result.Configs {
		if config.Runtime == "claude_code" && config.Scope == ScopeUser {
			foundClaudeConfig = true
			if !config.Exists || !config.Readable || config.ParserStatus != StatusOK {
				t.Fatalf("Claude config status = exists:%t readable:%t parser:%s", config.Exists, config.Readable, config.ParserStatus)
			}
			if config.MCPServerCount != 1 {
				t.Fatalf("Claude MCPServerCount = %d, want 1", config.MCPServerCount)
			}
			if config.FileSHA256 == "" || config.PathHash == "" {
				t.Fatal("Claude config missing hashes")
			}
		}
	}
	if !foundClaudeConfig {
		t.Fatal("Claude user config not found in inventory")
	}
}

func TestMetadataRedactionOmitsNamesAndPaths(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{
  "mcpServers": {
    "filesystem": {"command": "npx", "env": {"NODE_ENV": "production"}}
  }
}`)

	result := Scan(Options{
		ContentRetention: RedactionMetadata,
		HomeDir:          home,
		WorkingDir:       work,
		Now:              fixedNow,
	})

	if len(result.MCPServers) != 1 {
		t.Fatalf("MCPServers len = %d, want 1", len(result.MCPServers))
	}
	server := result.MCPServers[0]
	if server.ServerName != "" || server.CommandName != "" || server.SourcePath != "" || len(server.EnvKeys) != 0 {
		t.Fatalf("metadata server leaked redacted fields: %#v", server)
	}
	if server.ServerNameHash == "" || server.CommandNameHash == "" || server.SourcePathHash == "" || server.DefinitionHash == "" {
		t.Fatalf("metadata server missing hashes: %#v", server)
	}
	for _, config := range result.Configs {
		if config.Exists && config.Path != "" {
			t.Fatalf("metadata config leaked path: %#v", config)
		}
	}
}

func TestScanKeepsPartialResultsWhenAConfigIsMalformed(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{bad json`)
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), `
[mcp_servers.github]
command = "gh"
`)

	result := Scan(Options{
		ContentRetention: RedactionRedacted,
		HomeDir:          home,
		WorkingDir:       work,
		Now:              fixedNow,
	})

	var malformedFound bool
	for _, config := range result.Configs {
		if config.Runtime == "claude_code" && config.Scope == ScopeUser {
			malformedFound = true
			if config.ParserStatus != StatusParseFailed {
				t.Fatalf("malformed Claude parser status = %s, want %s", config.ParserStatus, StatusParseFailed)
			}
		}
	}
	if !malformedFound {
		t.Fatal("malformed Claude config result not found")
	}
	assertServer(t, result.MCPServers, "codex_cli", "github", TransportStdio, true, 0, 0, 0)
}

func TestMissingCandidatesAreReportedAsNotFound(t *testing.T) {
	result := Scan(Options{
		ContentRetention: RedactionRedacted,
		HomeDir:          t.TempDir(),
		WorkingDir:       t.TempDir(),
		Now:              fixedNow,
	})
	if len(result.Configs) == 0 {
		t.Fatal("expected candidate config results")
	}
	for _, config := range result.Configs {
		if config.Exists {
			continue
		}
		if config.ParserStatus != StatusNotFound {
			t.Fatalf("missing config status = %s, want %s", config.ParserStatus, StatusNotFound)
		}
		if config.PathHash == "" {
			t.Fatal("missing config should still include a path hash")
		}
	}
}

func fixedNow() time.Time {
	return time.Date(2026, 6, 5, 7, 0, 0, 0, time.UTC)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func assertServer(t *testing.T, servers []MCPServer, runtime, name, transport string, commandPresent bool, argsCount, envKeys, envKeyCount int) {
	t.Helper()
	for _, server := range servers {
		if server.Runtime == runtime && server.ServerName == name {
			if server.Transport != transport {
				t.Fatalf("%s/%s transport = %s, want %s", runtime, name, server.Transport, transport)
			}
			if server.CommandPresent != commandPresent {
				t.Fatalf("%s/%s commandPresent = %t, want %t", runtime, name, server.CommandPresent, commandPresent)
			}
			if server.ArgsCount != argsCount {
				t.Fatalf("%s/%s argsCount = %d, want %d", runtime, name, server.ArgsCount, argsCount)
			}
			if len(server.EnvKeys) != envKeys {
				t.Fatalf("%s/%s EnvKeys len = %d, want %d (%#v)", runtime, name, len(server.EnvKeys), envKeys, server.EnvKeys)
			}
			if server.EnvKeyCount != envKeyCount {
				t.Fatalf("%s/%s EnvKeyCount = %d, want %d", runtime, name, server.EnvKeyCount, envKeyCount)
			}
			if server.DefinitionHash == "" || server.ServerNameHash == "" || server.SourcePathHash == "" {
				t.Fatalf("%s/%s missing hashes: %#v", runtime, name, server)
			}
			return
		}
	}
	t.Fatalf("server %s/%s not found in %#v", runtime, name, servers)
}
