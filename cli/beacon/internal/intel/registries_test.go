package intel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/inventory"
)

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLocalRegistries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		file    string // relative to home, or "wd/..." for the working directory
		content string
		environ []string
		private []string
		public  []string
	}{
		{name: "nothing configured", public: []string{"npm:@acme/x", "npm:x", "pypi:x"}},
		{name: "scope in user npmrc", file: ".npmrc", content: "; comment\n@acme:registry=https://npm.acme.internal/\n//npm.acme.internal/:_authToken=${TOKEN}\n",
			private: []string{"npm:@acme/billing-mcp"}, public: []string{"npm:@playwright/mcp", "npm:plain", "pypi:x"}},
		{name: "registry in project npmrc", file: "wd/.npmrc", content: "registry = \"https://npm.acme.internal\"\n",
			private: []string{"npm:@playwright/mcp", "npm:plain"}, public: []string{"pypi:x"}},
		{name: "public registry in npmrc", file: ".npmrc", content: "registry=https://registry.npmjs.org/\n@x:registry=https://registry.npmjs.org\n",
			public: []string{"npm:@x/y", "npm:plain"}},
		{name: "npmrc pointing at another config", file: ".npmrc", content: "userconfig=/elsewhere/.npmrc\n", private: []string{"npm:plain"}},
		{name: "scope in pnpm global rc", file: ".config/pnpm/rc", content: "@acme:registry=https://npm.acme.internal/\n",
			private: []string{"npm:@acme/internal-mcp"}, public: []string{"npm:@playwright/mcp"}},
		{name: "registry in macOS pnpm rc", file: "Library/Preferences/pnpm/rc", content: "registry=https://npm.acme.internal/\n", private: []string{"npm:plain"}},
		{name: "yarn registry", file: ".yarnrc.yml", content: "npmRegistryServer: \"https://npm.acme.internal\"\n", private: []string{"npm:plain"}},
		{name: "yarn public registry", file: ".yarnrc.yml", content: "npmRegistryServer: \"https://registry.yarnpkg.com\"\n", public: []string{"npm:plain"}},
		{name: "yarn scopes", file: ".yarnrc.yml", content: "npmScopes:\n  acme:\n    npmRegistryServer: https://npm.acme.internal\n", private: []string{"npm:plain"}},
		{name: "bun scopes", file: ".bunfig.toml", content: "[install.scopes]\nacme = \"https://npm.acme.internal\"\n", private: []string{"npm:plain"}},
		{name: "pip.conf index", file: ".config/pip/pip.conf", content: "[global]\nindex-url = https://pypi.acme.internal/simple\n",
			private: []string{"pypi:acme-billing-mcp"}, public: []string{"npm:plain"}},
		{name: "legacy pip.conf extra index", file: ".pip/pip.conf", content: "[global]\nextra_index_url=https://pypi.acme.internal/simple\n", private: []string{"pypi:x"}},
		{name: "pip.conf public index", file: ".config/pip/pip.conf", content: "[global]\nindex-url = https://pypi.org/simple\ntimeout = 60\n", public: []string{"pypi:x"}},
		{name: "uv.toml index table", file: ".config/uv/uv.toml", content: "[[index]]\nname = \"acme\"\nurl = \"https://pypi.acme.internal/simple\"\n", private: []string{"pypi:x"}},
		{name: "project pyproject uv index", file: "wd/pyproject.toml", content: "[project]\nname = \"x\"\n[[tool.uv.index]]\nurl = \"https://pypi.acme.internal/simple\"\n", private: []string{"pypi:x"}},
		{name: "inherited npm scope", environ: []string{"npm_config_@acme:registry=https://npm.acme.internal"},
			private: []string{"npm:@acme/x"}, public: []string{"npm:@other/x", "npm:plain"}},
		{name: "inherited npm registry", environ: []string{"NPM_CONFIG_REGISTRY=https://npm.acme.internal"}, private: []string{"npm:plain"}},
		{name: "inherited public npm registry", environ: []string{"npm_config_registry=https://registry.npmjs.org/"}, public: []string{"npm:plain"}},
		{name: "inherited uv index", environ: []string{"UV_INDEX_URL=https://pypi.acme.internal/simple"}, private: []string{"pypi:x"}, public: []string{"npm:plain"}},
		{name: "inherited pip index", environ: []string{"PIP_EXTRA_INDEX_URL=https://pypi.acme.internal/simple"}, private: []string{"pypi:x"}},
		{name: "a config file named in the environment is read, not counted", file: "elsewhere/npmrc", content: "registry=https://registry.npmjs.org/\n",
			environ: []string{"NPM_CONFIG_USERCONFIG=HOME/elsewhere/npmrc"}, public: []string{"npm:plain"}},
		{name: "unrelated environment", environ: []string{"GITHUB_TOKEN=ghp_x", "NODE_ENV=production"}, public: []string{"npm:plain", "pypi:x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, wd := t.TempDir(), t.TempDir()
			if tc.file != "" {
				path := filepath.Join(home, tc.file)
				if rel, ok := strings.CutPrefix(tc.file, "wd/"); ok {
					path = filepath.Join(wd, rel)
				}
				writeConfig(t, path, tc.content)
			}
			var environ []string
			for _, kv := range tc.environ {
				environ = append(environ, strings.ReplaceAll(kv, "HOME/", home+string(filepath.Separator)))
			}
			reg := LocalRegistries(RegistryEnv{Home: home, WorkingDir: wd, Environ: environ})
			for _, q := range tc.private {
				if !reg.private(q) {
					t.Errorf("%s was not private: %+v", q, reg)
				}
			}
			for _, q := range tc.public {
				if reg.private(q) {
					t.Errorf("%s was private: %+v", q, reg)
				}
			}
		})
	}
}

func TestLocalRegistriesFollowsAConfigFileNamedInTheEnvironment(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "ci", "pip.conf")
	writeConfig(t, path, "[global]\nindex-url = https://pypi.acme.internal/simple\n")
	if reg := LocalRegistries(RegistryEnv{Home: home, Environ: []string{"PIP_CONFIG_FILE=" + path}}); !reg.private("pypi:x") {
		t.Fatalf("PIP_CONFIG_FILE was not read: %+v", reg)
	}
}

func TestCurrentRegistryEnvReadsSystemFiles(t *testing.T) {
	if env := CurrentRegistryEnv(false); !env.System || env.WorkingDir != "" {
		t.Fatalf("CurrentRegistryEnv(false) = %+v", env)
	}
}

func TestLocalRegistriesSkipsProjectFilesWithoutAWorkingDirectory(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, filepath.Join(home, "uv.toml"), "index-url = \"https://pypi.acme.internal/simple\"\n")
	if reg := LocalRegistries(RegistryEnv{Home: home}); reg.private("pypi:x") {
		t.Fatalf("a file outside every config location counted: %+v", reg)
	}
}

func TestFromInventoryHoldsBackLauncherConfigAndEnvFiles(t *testing.T) {
	res := inventory.Result{MCPServers: []inventory.MCPServer{
		server("cursor", "billing", "user", map[string]interface{}{"command": "npx", "args": []interface{}{"-y", "@acme/billing-mcp"}}),
		server("cursor", "pw", "user", map[string]interface{}{"command": "npx", "args": []interface{}{"-y", "@playwright/mcp"}}),
		server("vscode", "fetch", "user", map[string]interface{}{"command": "uvx", "args": []interface{}{"mcp-server-fetch"}, "envFile": "${workspaceFolder}/.env"}),
	}}
	reg := Registries{NPMScopes: map[string]bool{"@acme": true}}
	items, skipped := FromInventory(res, FromInventoryOptions{Registries: reg})
	if len(items) != 1 || items[0].Q != "npm:@playwright/mcp" || items[0].Basis != BasisNPM {
		t.Fatalf("items = %+v", items)
	}
	if len(skipped) != 2 || skipped[0].Reason != ReasonPrivatePackage || skipped[1].Reason != ReasonPrivatePackage {
		t.Fatalf("skipped = %+v", skipped)
	}
	items, _ = FromInventory(res, FromInventoryOptions{Registries: reg, IncludePrivateNames: true})
	if len(items) != 3 {
		t.Fatalf("with consent = %+v", items)
	}
}
