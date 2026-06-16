package inventory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
		HomeDir:    home,
		WorkingDir: work,
		Now:        fixedNow,
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
			if config.ParserMode != formatJSON || config.ConfigKind != KindNativeConfig {
				t.Fatalf("Claude config mode/kind = %s/%s, want %s/%s", config.ParserMode, config.ConfigKind, formatJSON, KindNativeConfig)
			}
		}
	}
	if !foundClaudeConfig {
		t.Fatal("Claude user config not found in inventory")
	}
}

func TestScanIncludesNamesAndPaths(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{
  "mcpServers": {
    "filesystem": {"command": "npx", "env": {"NODE_ENV": "production"}}
  }
}`)

	result := Scan(Options{
		HomeDir:    home,
		WorkingDir: work,
		Now:        fixedNow,
	})

	if len(result.MCPServers) != 1 {
		t.Fatalf("MCPServers len = %d, want 1", len(result.MCPServers))
	}
	server := result.MCPServers[0]
	if server.ServerName != "filesystem" || server.CommandName != "npx" || server.SourcePath == "" || len(server.EnvKeys) != 1 {
		t.Fatalf("server missing full inventory fields: %#v", server)
	}
	if server.ServerNameHash == "" || server.CommandNameHash == "" || server.SourcePathHash == "" || server.DefinitionHash == "" {
		t.Fatalf("server missing hashes: %#v", server)
	}
	for _, config := range result.Configs {
		if config.Exists && config.Path == "" {
			t.Fatalf("config missing path: %#v", config)
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
		HomeDir:    home,
		WorkingDir: work,
		Now:        fixedNow,
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
		HomeDir:    t.TempDir(),
		WorkingDir: t.TempDir(),
		Now:        fixedNow,
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

func TestScanIncludesAllSupportedCurrentUserAndProjectConfigs(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("SHELL", "/bin/bash")

	result := Scan(Options{
		HomeDir:    home,
		WorkingDir: work,
		Now:        fixedNow,
	})

	expected := []candidate{
		{runtime: "claude_code", path: filepath.Join(home, ".claude", "settings.json"), scope: ScopeUser, format: formatJSON, kind: KindNativeConfig},
		{runtime: "claude_code", path: filepath.Join(work, ".claude", "settings.json"), scope: ScopeProject, format: formatJSON, kind: KindNativeConfig},
		{runtime: "claude_code", path: "/Library/Application Support/ClaudeCode/managed-settings.json", scope: ScopeManaged, format: formatJSON, kind: KindManagedConfig},
		{runtime: "codex_cli", path: filepath.Join(home, ".codex", "config.toml"), scope: ScopeUser, format: formatTOML, kind: KindNativeConfig},
		{runtime: "cursor", path: filepath.Join(home, ".cursor", "mcp.json"), scope: ScopeUser, format: formatJSON, kind: KindNativeConfig},
		{runtime: "cursor", path: filepath.Join(work, ".cursor", "mcp.json"), scope: ScopeProject, format: formatJSON, kind: KindNativeConfig},
		{runtime: "cursor", path: filepath.Join(home, ".cursor", "hooks.json"), scope: ScopeUser, format: formatJSON, kind: KindHookConfig},
		{runtime: "cursor", path: filepath.Join(work, ".cursor", "hooks.json"), scope: ScopeProject, format: formatJSON, kind: KindHookConfig},
		{runtime: "gemini_cli", path: filepath.Join(home, ".gemini", "settings.json"), scope: ScopeUser, format: formatJSON, kind: KindNativeConfig},
		{runtime: "antigravity_cli", path: filepath.Join(home, ".gemini", "config", "hooks.json"), scope: ScopeUser, format: formatJSON, kind: KindHookConfig},
		{runtime: "antigravity_cli", path: filepath.Join(work, ".agents", "hooks.json"), scope: ScopeProject, format: formatJSON, kind: KindHookConfig},
		{runtime: "vscode", path: vscodeUserSettingsPath(home), scope: ScopeUser, format: formatJSON, kind: KindNativeConfig},
		{runtime: "vscode", path: filepath.Join(work, ".vscode", "settings.json"), scope: ScopeProject, format: formatJSON, kind: KindNativeConfig},
		{runtime: "vscode", path: filepath.Join(home, ".copilot", "hooks", "beacon.json"), scope: ScopeUser, format: formatJSON, kind: KindHookConfig},
		{runtime: "vscode", path: filepath.Join(work, ".github", "hooks", "beacon.json"), scope: ScopeProject, format: formatJSON, kind: KindHookConfig},
		{runtime: "factory", path: filepath.Join(home, ".bash_profile"), scope: ScopeUser, format: formatMetadataOnly, kind: KindProfile},
		{runtime: "factory", path: filepath.Join(home, ".factory", "settings.json"), scope: ScopeUser, format: formatJSON, kind: KindHookConfig},
		{runtime: "factory", path: filepath.Join(work, ".factory", "settings.json"), scope: ScopeProject, format: formatJSON, kind: KindHookConfig},
		{runtime: "copilot_cli", path: filepath.Join(home, ".bash_profile"), scope: ScopeUser, format: formatMetadataOnly, kind: KindProfile},
		{runtime: "opencode", path: filepath.Join(home, ".config", "opencode", "plugins", "beacon.ts"), scope: ScopeUser, format: formatMetadataOnly, kind: KindPlugin},
		{runtime: "opencode", path: filepath.Join(work, ".opencode", "plugins", "beacon.ts"), scope: ScopeProject, format: formatMetadataOnly, kind: KindPlugin},
		{runtime: "hermes", path: filepath.Join(home, ".hermes", "config.yaml"), scope: ScopeUser, format: formatYAML, kind: KindNativeConfig},
		{runtime: "devin-cli", path: filepath.Join(home, ".config", "devin", "config.json"), scope: ScopeUser, format: formatJSON, kind: KindNativeConfig},
		{runtime: "devin-cli", path: filepath.Join(work, ".devin", "hooks.v1.json"), scope: ScopeProject, format: formatJSON, kind: KindHookConfig},
		{runtime: "devin-desktop", path: filepath.Join(home, ".codeium", "windsurf", "hooks.json"), scope: ScopeUser, format: formatJSON, kind: KindHookConfig},
		{runtime: "devin-desktop", path: filepath.Join(work, ".windsurf", "hooks.json"), scope: ScopeProject, format: formatJSON, kind: KindHookConfig},
		{runtime: "grok", path: filepath.Join(home, ".grok", "hooks", "beacon-endpoint.json"), scope: ScopeUser, format: formatJSON, kind: KindHookConfig},
		{runtime: "grok", path: filepath.Join(work, ".grok", "hooks", "beacon-endpoint.json"), scope: ScopeProject, format: formatJSON, kind: KindHookConfig},
	}
	if got, want := len(result.Configs), len(expected); got != want {
		t.Fatalf("config candidates = %d, want %d", got, want)
	}
	for _, item := range expected {
		config := findConfig(result.Configs, item.runtime, item.path)
		if config == nil {
			t.Fatalf("missing candidate %s %s", item.runtime, item.path)
		}
		if config.Scope != item.scope {
			t.Fatalf("%s %s scope = %s, want %s", item.runtime, item.path, config.Scope, item.scope)
		}
		if config.ParserMode != item.format || config.ConfigKind != item.kind {
			t.Fatalf("%s %s mode/kind = %s/%s, want %s/%s", item.runtime, item.path, config.ParserMode, config.ConfigKind, item.format, item.kind)
		}
	}
}

func TestScanYAMLAndMetadataOnlyConfigs(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("SHELL", "/bin/zsh")
	writeFile(t, filepath.Join(home, ".hermes", "config.yaml"), `
mcpServers:
  memory:
    command: uvx
    args:
      - mcp-server-memory
`)
	writeFile(t, filepath.Join(home, ".config", "opencode", "plugins", "beacon.ts"), `// beacon-managed-opencode-plugin:v1`)
	writeFile(t, filepath.Join(home, ".zshrc"), `export OTEL_TELEMETRY_ENDPOINT=http://127.0.0.1:4318`)

	result := Scan(Options{
		HomeDir:    home,
		WorkingDir: work,
		Now:        fixedNow,
	})

	assertServer(t, result.MCPServers, "hermes", "memory", TransportStdio, true, 1, 0, 0)
	opencode := findConfig(result.Configs, "opencode", filepath.Join(home, ".config", "opencode", "plugins", "beacon.ts"))
	if opencode == nil {
		t.Fatal("opencode metadata-only config not found")
	}
	if opencode.ParserStatus != StatusOK || !opencode.BeaconManaged {
		t.Fatalf("opencode status = %s managed=%t, want ok/managed", opencode.ParserStatus, opencode.BeaconManaged)
	}
	if opencode.ParserMode != formatMetadataOnly || opencode.ConfigKind != KindPlugin {
		t.Fatalf("opencode mode/kind = %s/%s, want %s/%s", opencode.ParserMode, opencode.ConfigKind, formatMetadataOnly, KindPlugin)
	}
	factoryProfile := findConfig(result.Configs, "factory", filepath.Join(home, ".zshrc"))
	if factoryProfile == nil {
		t.Fatal("factory shell profile config not found")
	}
	if factoryProfile.ParserStatus != StatusOK || !factoryProfile.BeaconManaged {
		t.Fatalf("factory profile status = %s managed=%t, want ok/managed", factoryProfile.ParserStatus, factoryProfile.BeaconManaged)
	}
	if factoryProfile.ParserMode != formatMetadataOnly || factoryProfile.ConfigKind != KindProfile {
		t.Fatalf("factory profile mode/kind = %s/%s, want %s/%s", factoryProfile.ParserMode, factoryProfile.ConfigKind, formatMetadataOnly, KindProfile)
	}
	copilotProfile := findConfig(result.Configs, "copilot_cli", filepath.Join(home, ".zshrc"))
	if copilotProfile == nil {
		t.Fatal("copilot shell profile config not found")
	}
	if copilotProfile.BeaconManaged {
		t.Fatal("factory OTEL marker should not make copilot profile Beacon-managed")
	}
}

func TestCopilotManagedDetectionFalsePositives(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "commented_out",
			content: "# export COPILOT_OTEL_ENABLED=true\nexport OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318\n",
			want:    false,
		},
		{
			name:    "disabled_false",
			content: "export COPILOT_OTEL_ENABLED=false\nexport OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318\n",
			want:    false,
		},
		{
			name:    "disabled_zero",
			content: "export COPILOT_OTEL_ENABLED=0\nexport COPILOT_OTEL_ENDPOINT=http://127.0.0.1:4318\n",
			want:    false,
		},
		{
			name:    "enabled_no_endpoint",
			content: "export COPILOT_OTEL_ENABLED=true\n",
			want:    false,
		},
		{
			name:    "enabled_remote_endpoint",
			content: "export COPILOT_OTEL_ENABLED=true\nexport COPILOT_OTEL_ENDPOINT=http://remote.example.com:4318\n",
			want:    false,
		},
		{
			name:    "enabled_with_local_endpoint",
			content: "export COPILOT_OTEL_ENABLED=true\nexport COPILOT_OTEL_ENDPOINT=http://127.0.0.1:4318\n",
			want:    true,
		},
		{
			name:    "enabled_with_generic_otlp_endpoint",
			content: "export COPILOT_OTEL_ENABLED=1\nexport OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318\n",
			want:    true,
		},
		{
			name:    "factory_endpoint_only",
			content: "export OTEL_TELEMETRY_ENDPOINT=http://127.0.0.1:4318\n",
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			work := t.TempDir()
			writeFile(t, filepath.Join(home, ".zshrc"), tc.content)

			result := Scan(Options{
				HomeDir:    home,
				WorkingDir: work,
				Now:        fixedNow,
			})

			copilotProfile := findConfig(result.Configs, "copilot_cli", filepath.Join(home, ".zshrc"))
			if copilotProfile == nil {
				t.Fatal("copilot shell profile config not found")
			}
			if copilotProfile.BeaconManaged != tc.want {
				t.Fatalf("copilot BeaconManaged = %t, want %t", copilotProfile.BeaconManaged, tc.want)
			}
		})
	}
}

func TestScanCurrentUserAndProjectSkillInventory(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	claudeSkillPath := filepath.Join(home, ".claude", "skills", "deploy", "SKILL.md")
	agentSkillPath := filepath.Join(work, ".agents", "skills", "review", "SKILL.md")
	writeFile(t, claudeSkillPath, "---\ndescription: do not retain\n---\nSECRET INSTRUCTIONS")
	writeFile(t, agentSkillPath, "# Review\nKeep this instruction body out of inventory.")

	result := Scan(Options{
		HomeDir:    home,
		WorkingDir: work,
		Now:        fixedNow,
	})

	deploy := findSkill(result.Skills, "claude_code", "deploy", claudeSkillPath)
	if deploy == nil {
		t.Fatalf("Claude skill not found in %#v", result.Skills)
	}
	if deploy.SourceScope != ScopeUser || !deploy.Exists || !deploy.Readable || deploy.ParserStatus != StatusOK {
		t.Fatalf("Claude skill status = %#v", deploy)
	}
	if deploy.FileSHA256 == "" || deploy.SkillNameHash == "" || deploy.RootPathHash == "" || deploy.ManifestPathHash == "" {
		t.Fatalf("Claude skill missing hashes: %#v", deploy)
	}
	review := findSkill(result.Skills, "agent_skills", "review", agentSkillPath)
	if review == nil || review.SourceScope != ScopeProject {
		t.Fatalf("project agent skill = %#v, want project scope", review)
	}

	data, err := json.Marshal(result.Skills)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, forbidden := range []string{"SECRET INSTRUCTIONS", "Keep this instruction body", "do not retain"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("skill inventory retained manifest content %q in %s", forbidden, text)
		}
	}
}

func TestMissingSkillRootsAreReportedAsNotFound(t *testing.T) {
	result := Scan(Options{
		HomeDir:    t.TempDir(),
		WorkingDir: t.TempDir(),
		Now:        fixedNow,
	})
	if len(result.Skills) == 0 {
		t.Fatal("expected missing skill roots to be reported")
	}
	for _, skill := range result.Skills {
		if skill.Exists {
			continue
		}
		if skill.ParserStatus != StatusNotFound {
			t.Fatalf("missing skill status = %s, want %s", skill.ParserStatus, StatusNotFound)
		}
		if skill.RootPathHash == "" {
			t.Fatal("missing skill root should still include a path hash")
		}
	}
}

func TestSkillScanIsBoundedToDirectSkillManifests(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, filepath.Join(home, ".cursor", "skills", "direct", "SKILL.md"), "# Direct")
	writeFile(t, filepath.Join(home, ".cursor", "skills", "nested", "child", "SKILL.md"), "# Nested")
	writeFile(t, filepath.Join(home, ".cursor", "skills", "node_modules", "package", "SKILL.md"), "# Vendor")

	result := Scan(Options{
		HomeDir:    home,
		WorkingDir: work,
		Now:        fixedNow,
	})

	if findSkill(result.Skills, "cursor", "direct", filepath.Join(home, ".cursor", "skills", "direct", "SKILL.md")) == nil {
		t.Fatal("direct Cursor skill not found")
	}
	if findSkill(result.Skills, "cursor", "child", filepath.Join(home, ".cursor", "skills", "nested", "child", "SKILL.md")) != nil {
		t.Fatal("nested skill manifest should not be discovered")
	}
	if findSkill(result.Skills, "cursor", "package", filepath.Join(home, ".cursor", "skills", "node_modules", "package", "SKILL.md")) != nil {
		t.Fatal("vendor skill manifest should not be discovered")
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

func findConfig(configs []Config, runtime, path string) *Config {
	pathHash := hashString(path)
	for i := range configs {
		if configs[i].Runtime == runtime && configs[i].PathHash == pathHash {
			return &configs[i]
		}
	}
	return nil
}

func findSkill(skills []Skill, runtime, name, manifestPath string) *Skill {
	manifestPathHash := hashString(manifestPath)
	for i := range skills {
		if skills[i].Runtime == runtime && skills[i].SkillName == name && skills[i].ManifestPathHash == manifestPathHash {
			return &skills[i]
		}
	}
	return nil
}
