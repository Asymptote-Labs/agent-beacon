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
		{name: "npmrc naming a config Beacon cannot resolve", file: ".npmrc", content: "globalconfig=${NPM_HOME}/npmrc\n", private: []string{"npm:plain"}},
		{name: "npmrc naming a missing config", file: ".npmrc", content: "globalconfig=/nonexistent/beacon-test/npmrc\n", public: []string{"npm:plain"}},
		{name: "scope in pnpm global rc", file: ".config/pnpm/rc", content: "@acme:registry=https://npm.acme.internal/\n",
			private: []string{"npm:@acme/internal-mcp"}, public: []string{"npm:@playwright/mcp"}},
		{name: "registry in macOS pnpm rc", file: "Library/Preferences/pnpm/rc", content: "registry=https://npm.acme.internal/\n", private: []string{"npm:plain"}},
		{name: "global npmrc under a lowercase npm_config_prefix", file: "npm-global/etc/npmrc", content: "registry=https://npm.acme.internal\n",
			environ: []string{"npm_config_prefix=HOME/npm-global"}, private: []string{"npm:plain"}},
		{name: "renamed yarn rc", file: ".acmerc.yml", content: "npmRegistryServer: https://npm.acme.internal\n",
			environ: []string{"YARN_RC_FILENAME=.acmerc.yml"}, private: []string{"npm:plain"}},
		{name: "site-wide pip in XDG_CONFIG_DIRS", file: "xdg-site/pip/pip.conf", content: "[global]\nindex-url = https://pypi.acme.internal/simple\n",
			environ: []string{"XDG_CONFIG_DIRS=HOME/xdg-site"}, private: []string{"pypi:x"}},
		{name: "Windows ProgramData uv", file: "ProgramData/uv/uv.toml", content: "index-url = \"https://pypi.acme.internal/simple\"\n",
			environ: []string{"ProgramData=HOME/ProgramData"}, private: []string{"pypi:x"}},
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

func TestLocalRegistriesReadsTheGlobalNpmrcOfTheNodeOnPath(t *testing.T) {
	home := t.TempDir()
	install := filepath.Join(home, ".nvm", "versions", "node", "v20.1.0")
	writeConfig(t, filepath.Join(install, "bin", "node"), "")
	writeConfig(t, filepath.Join(install, "etc", "npmrc"), "@acme:registry=https://npm.acme.internal\n")
	reg := LocalRegistries(RegistryEnv{Home: home, Environ: []string{"PATH=" + filepath.Join(home, "empty") + string(filepath.ListSeparator) + filepath.Join(install, "bin")}})
	if !reg.private("npm:@acme/x") || reg.private("npm:plain") {
		t.Fatalf("the nvm global npmrc was not read: %+v", reg)
	}

	// npm's builtin npmrc, beside the npm the node ships.
	writeConfig(t, filepath.Join(install, "etc", "npmrc"), "")
	writeConfig(t, filepath.Join(install, "lib", "node_modules", "npm", "npmrc"), "registry=https://npm.acme.internal\n")
	if reg := LocalRegistries(RegistryEnv{Home: home, Environ: []string{"PATH=" + filepath.Join(install, "bin")}}); !reg.private("npm:plain") {
		t.Fatalf("npm's builtin npmrc was not read: %+v", reg)
	}
}

func TestNpmrcFollowsTheConfigFilesAndPrefixItNames(t *testing.T) {
	home := t.TempDir()
	// A distro's builtin npmrc names /etc/npmrc; one naming only the public registry leaves
	// packages public.
	writeConfig(t, filepath.Join(home, "etc-npmrc"), "registry=https://registry.npmjs.org/\n")
	writeConfig(t, filepath.Join(home, ".npmrc"), "globalconfig="+filepath.Join(home, "etc-npmrc")+"\n")
	if reg := LocalRegistries(RegistryEnv{Home: home}); reg.private("npm:plain") || reg.private("npm:@x/y") {
		t.Fatalf("a globalconfig naming the public registry held packages back: %+v", reg)
	}
	// One naming a private scope holds that scope back, by ~ too.
	writeConfig(t, filepath.Join(home, "etc-npmrc"), "@acme:registry=https://npm.acme.internal\n")
	writeConfig(t, filepath.Join(home, ".npmrc"), "globalconfig=~/etc-npmrc\n")
	if reg := LocalRegistries(RegistryEnv{Home: home}); !reg.private("npm:@acme/x") || reg.private("npm:plain") {
		t.Fatalf("the named globalconfig was not followed: %+v", reg)
	}
	// prefix= in ~/.npmrc moves the global npmrc.
	writeConfig(t, filepath.Join(home, "npm-global", "etc", "npmrc"), "registry=https://npm.acme.internal\n")
	writeConfig(t, filepath.Join(home, ".npmrc"), "prefix=~/npm-global\n")
	if reg := LocalRegistries(RegistryEnv{Home: home}); !reg.private("npm:plain") {
		t.Fatalf("the prefix's global npmrc was not read: %+v", reg)
	}
	// A cycle of config files ends.
	writeConfig(t, filepath.Join(home, ".npmrc"), "userconfig=~/.npmrc\n")
	LocalRegistries(RegistryEnv{Home: home})
}

func TestNpmrcExpandsEnvironmentVariablesAsNpmDoes(t *testing.T) {
	home := t.TempDir()
	appData := filepath.Join(home, "AppData", "Roaming")
	install := filepath.Join(home, "nodejs")
	writeConfig(t, filepath.Join(install, "bin", "node"), "")
	// The official Windows installer's builtin npmrc.
	writeConfig(t, filepath.Join(install, "lib", "node_modules", "npm", "npmrc"), "prefix=${APPDATA}/npm\n")
	env := RegistryEnv{Home: home, Environ: []string{"APPDATA=" + appData, "PATH=" + filepath.Join(install, "bin")}}
	if reg := LocalRegistries(env); reg.private("npm:plain") || reg.private("npm:@x/y") {
		t.Fatalf("prefix=${APPDATA}/npm held every npm package back: %+v", reg)
	}
	writeConfig(t, filepath.Join(appData, "npm", "etc", "npmrc"), "@acme:registry=https://npm.acme.internal\n")
	if reg := LocalRegistries(env); !reg.private("npm:@acme/x") || reg.private("npm:plain") {
		t.Fatalf("the expanded prefix's global npmrc was not read: %+v", reg)
	}
	// An unset variable leaves Beacon unable to tell, so npm is held back.
	env.Environ = []string{"PATH=" + filepath.Join(install, "bin")}
	if reg := LocalRegistries(env); !reg.private("npm:plain") {
		t.Fatalf("an unset ${APPDATA} did not hold npm back: %+v", reg)
	}
}

func TestLocalRegistriesReadsTheBuiltinNpmrcAlongsideAnExportedPrefix(t *testing.T) {
	home := t.TempDir()
	install := filepath.Join(home, "node")
	writeConfig(t, filepath.Join(install, "bin", "node"), "")
	writeConfig(t, filepath.Join(install, "lib", "node_modules", "npm", "npmrc"), "registry=https://npm.acme.internal\n")
	reg := LocalRegistries(RegistryEnv{Home: home, Environ: []string{"npm_config_prefix=" + filepath.Join(home, "global"), "PATH=" + filepath.Join(install, "bin")}})
	if !reg.private("npm:plain") {
		t.Fatalf("the builtin npmrc was skipped once a prefix was exported: %+v", reg)
	}
}

func TestRegistriesForAServerOnItsOwnNode(t *testing.T) {
	home := t.TempDir()
	nvm := filepath.Join(home, ".nvm", "versions", "node", "v20.11.0")
	writeConfig(t, filepath.Join(nvm, "bin", "node"), "")
	writeConfig(t, filepath.Join(nvm, "bin", "npx"), "")
	writeConfig(t, filepath.Join(nvm, "etc", "npmrc"), "@acme:registry=https://npm.acme.internal\n")
	base := LocalRegistries(RegistryEnv{Home: home})
	if base.private("npm:@acme/x") {
		t.Fatal("the nvm node is not on Beacon's PATH in this test")
	}
	byCommand := base.forServer(filepath.Join(nvm, "bin", "npx"), map[string]interface{}{})
	byPath := base.forServer("npx", map[string]interface{}{"env": map[string]interface{}{"PATH": filepath.Join(nvm, "bin") + string(filepath.ListSeparator) + "/usr/bin"}})
	for name, reg := range map[string]Registries{"absolute command": byCommand, "env PATH": byPath} {
		if !reg.private("npm:@acme/x") || reg.private("npm:@playwright/mcp") {
			t.Errorf("%s: %+v", name, reg)
		}
	}
	if base.private("npm:@acme/x") {
		t.Fatal("forServer changed the machine-wide registries")
	}

	npx := filepath.Join(nvm, "bin", "npx")
	for name, def := range map[string]map[string]interface{}{
		"absolute command":  {"command": npx, "args": []interface{}{"-y", "@acme/billing-mcp"}},
		"whole launch line": {"command": npx + " -y @acme/billing-mcp"},
		"cmd /c wrapper":    {"command": "cmd", "args": []interface{}{"/c", npx, "-y", "@acme/billing-mcp"}},
	} {
		res := inventory.Result{MCPServers: []inventory.MCPServer{server("claude_desktop", "billing", "user", def)}}
		if items, skipped := FromInventory(res, FromInventoryOptions{Registries: base}); len(items) != 0 || len(skipped) != 1 || skipped[0].Reason != ReasonPrivatePackage {
			t.Errorf("%s: a server on its own nvm node was not held back: %+v %+v", name, items, skipped)
		}
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

func TestCurrentRegistryEnvReadsSystemAndProjectFiles(t *testing.T) {
	if env := CurrentRegistryEnv(); !env.System || env.WorkingDir == "" {
		t.Fatalf("CurrentRegistryEnv() = %+v", env)
	}
}

func TestLocalRegistriesReadsProjectConfigAboveTheWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	wd := filepath.Join(root, "repo", "packages", "app")
	writeConfig(t, filepath.Join(root, "repo", ".npmrc"), "@acme:registry=https://npm.acme.internal\n")
	writeConfig(t, filepath.Join(root, "repo", "pyproject.toml"), "[[tool.uv.index]]\nurl = \"https://pypi.acme.internal/simple\"\n")
	if err := os.MkdirAll(wd, 0o700); err != nil {
		t.Fatal(err)
	}
	reg := LocalRegistries(RegistryEnv{Home: t.TempDir(), WorkingDir: wd})
	if !reg.private("npm:@acme/x") || !reg.private("pypi:x") || reg.private("npm:plain") {
		t.Fatalf("project config above the working directory was not read: %+v", reg)
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
