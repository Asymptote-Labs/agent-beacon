package launchpkg

import "testing"

func TestFromCommand(t *testing.T) {
	cases := []struct {
		command string
		args    []string
		want    string
	}{
		{"npx", []string{"-y", "@playwright/mcp@latest"}, "npm:@playwright/mcp"},
		{"npx", []string{"--yes", "@Modelcontextprotocol/Server-GitHub"}, "npm:@modelcontextprotocol/server-github"},
		{"/usr/local/bin/npx", []string{"mcp-remote@0.1.2", "https://example.test/sse"}, "npm:mcp-remote"},
		{"npx.cmd", []string{"-y", "@scope/pkg"}, "npm:@scope/pkg"},
		{"npx", []string{"-p", "@scope/server", "server-bin"}, "npm:@scope/server"},
		{"npx", []string{"--package=tool@1", "tool"}, "npm:tool"},
		{"bunx", []string{"some-mcp"}, "npm:some-mcp"},
		{"pnpm", []string{"dlx", "@x/y@2"}, "npm:@x/y"},
		{"yarn", []string{"dlx", "z"}, "npm:z"},
		{"npm", []string{"exec", "--", "w@1.0.0"}, "npm:w"},
		{"npm", []string{"exec", "w@1.0.0", "--yes"}, "npm:w"},
		{"npm", []string{"exec", "w", "--", "--port", "3000"}, "npm:w"},
		{"npm", []string{"x", "w", "positional-arg"}, "npm:w"},
		// npm exec reads its options after the package word, so --package there names the
		// package npm installs and the plain word is the command it provides.
		{"npm", []string{"exec", "bar", "--package=@scope/foo"}, "npm:@scope/foo"},
		{"npm", []string{"exec", "bar", "-p", "@scope/foo"}, "npm:@scope/foo"},
		{"uvx", []string{"mcp-server-fetch"}, "pypi:mcp-server-fetch"},
		{"uvx", []string{"--python", "3.12", "Mcp_Server.Time==1.2"}, "pypi:mcp_server.time"},
		{"uvx", []string{"--from", "awslabs.aws-docs-mcp-server[cli]>=1", "aws-docs"}, "pypi:awslabs.aws-docs-mcp-server"},
		{"pipx", []string{"run", "mcp-git"}, "pypi:mcp-git"},
		{"uv", []string{"tool", "run", "--from=serena", "serena"}, "pypi:serena"},
		{"npx -y @playwright/mcp", nil, "npm:@playwright/mcp"},
		{"npx", []string{"--registry", "https://r.example", "-y", "pkg-a"}, "npm:pkg-a"},
		{"npx", []string{"--workspace=api", "pkg-b@2"}, "npm:pkg-b"},
		{"npx", []string{"-w", "api", "pkg-c"}, "npm:pkg-c"},
		{"uvx", []string{"--with-requirements", "requirements.txt", "real-server"}, "pypi:real-server"},
		{"uvx", []string{"-q", "--index-url=https://i.example/simple", "--with", "extra", "srv"}, "pypi:srv"},
		{"pipx", []string{"run", "--spec", "mcp-thing==1", "thing"}, "pypi:mcp-thing"},
	}
	for _, tc := range cases {
		if p, ok := FromCommand(tc.command, tc.args); !ok || p.Ref != tc.want {
			t.Errorf("FromCommand(%q, %q) = %+v, %v; want %q", tc.command, tc.args, p, ok, tc.want)
		}
	}
	for _, tc := range []struct {
		command string
		args    []string
	}{
		{"node", []string{"/home/me/server.js"}},
		{"python", []string{"-m", "my_server"}},
		{"docker", []string{"run", "-i", "mcp/github"}},
		{"npx", []string{"-y"}},
		{"npx", []string{"./local/pkg"}},
		{"npx", []string{"/abs/path"}},
		{"npx", []string{"https://example.test/x.tgz"}},
		{"npx", []string{"[REDACTED]"}},
		{"npm", []string{"run", "start"}},
		{"pnpm", []string{"start"}},
		{"pipx", []string{"install", "x"}},
		{"uv", []string{"run", "server.py"}},
		{"uvx", []string{"--from", "git+https://github.com/a/b", "b"}},
		// An option Beacon does not know may take a value, so nothing is guessed.
		{"uvx", []string{"--some-new-flag", "requirements.txt", "srv"}},
		{"npx", []string{"--mystery", "value", "pkg"}},
		{"npx", []string{"--mystery=value", "pkg"}},
		{"npx", []string{"-c", "echo hi"}},
		// npm exec would read an option after the package as its own, so one Beacon does not
		// know still stops the parse there.
		{"npm", []string{"exec", "pkg", "--mystery", "value"}},
		{"npm", []string{"exec", "pkg", "--@acme:registry=https://npm.acme.internal"}},
		{"", nil},
	} {
		if p, ok := FromCommand(tc.command, tc.args); ok {
			t.Errorf("FromCommand(%q, %q) = %+v, want no package", tc.command, tc.args, p)
		}
	}
}

func TestPrivateRegistriesAndWindowsShells(t *testing.T) {
	for _, tc := range []struct {
		command string
		args    []string
		want    string
		private bool
	}{
		{"npx", []string{"--registry", "https://npm.acme.internal", "-y", "@acme/billing-mcp"}, "npm:@acme/billing-mcp", true},
		{"npx", []string{"--registry=https://npm.acme.internal", "@acme/billing-mcp"}, "npm:@acme/billing-mcp", true},
		{"uvx", []string{"--index-url", "https://pypi.acme.internal/simple", "acme-billing-mcp"}, "pypi:acme-billing-mcp", true},
		{"uvx", []string{"--extra-index-url=https://x.example/simple", "srv"}, "pypi:srv", true},
		// A private index or registry named after the package flag still applies to that package.
		{"uvx", []string{"--from", "acme-billing-mcp", "--index-url", "https://pypi.acme.internal/simple", "acme-billing"}, "pypi:acme-billing-mcp", true},
		{"uvx", []string{"--from=acme-billing-mcp", "--index=https://pypi.acme.internal/simple", "acme-billing"}, "pypi:acme-billing-mcp", true},
		{"npx", []string{"-p", "@acme/mcp", "--registry", "https://npm.acme.internal", "acme-mcp"}, "npm:@acme/mcp", true},
		{"npx", []string{"--package=@acme/mcp", "--registry=https://npm.acme.internal", "acme-mcp"}, "npm:@acme/mcp", true},
		// npm exec reads a registry or config file named after the package too, up to --.
		{"npm", []string{"exec", "@acme/internal-mcp", "--registry=https://npm.acme.internal"}, "npm:@acme/internal-mcp", true},
		{"npm", []string{"x", "@acme/internal-mcp", "--registry", "https://npm.acme.internal"}, "npm:@acme/internal-mcp", true},
		{"npm", []string{"exec", "@acme/internal-mcp", "--userconfig", "/x/.npmrc"}, "npm:@acme/internal-mcp", true},
		{"npm", []string{"exec", "bar", "--package=@acme/foo", "--registry=https://npm.acme.internal"}, "npm:@acme/foo", true},
		// After -- the words are the command's, not npm's.
		{"npm", []string{"exec", "w", "--", "--registry=https://npm.acme.internal"}, "npm:w", false},
		// npx stops at the package word: what follows is the command's.
		{"npx", []string{"-y", "pkg", "--registry=https://elsewhere.example"}, "npm:pkg", false},
		{"uvx", []string{"--from", "mcp-server-fetch", "mcp-server-fetch"}, "pypi:mcp-server-fetch", false},
		// A config file or pip's own arguments can name an index or registry.
		{"npx", []string{"--userconfig", "/x/.npmrc", "@acme/mcp"}, "npm:@acme/mcp", true},
		{"uvx", []string{"--config-file", "/x/uv.toml", "acme-mcp"}, "pypi:acme-mcp", true},
		{"pipx", []string{"run", "--pip-args=--index-url=https://pypi.acme.internal/simple", "acme-mcp"}, "pypi:acme-mcp", true},
		{"cmd", []string{"/c", "npx", "-y", "@playwright/mcp"}, "npm:@playwright/mcp", false},
		{"C:\\Windows\\System32\\cmd.exe", []string{"/C", "uvx", "mcp-server-fetch"}, "pypi:mcp-server-fetch", false},
		{"cmd /c npx -y @playwright/mcp", nil, "npm:@playwright/mcp", false},
		{"C:\\Program Files\\nodejs\\npx.cmd", []string{"-y", "pkg"}, "npm:pkg", false},
	} {
		if p, ok := FromCommand(tc.command, tc.args); !ok || p != (Package{Ref: tc.want, Private: tc.private}) {
			t.Errorf("FromCommand(%q, %q) = %+v, %v; want %q private=%v", tc.command, tc.args, p, ok, tc.want, tc.private)
		}
	}
	if _, ok := FromCommand("cmd", []string{"/c", "node", "server.js"}); ok {
		t.Error("cmd /c node was read as a package")
	}
}

func TestFromCommandRefusesSpecsOffTheRegistry(t *testing.T) {
	for _, tc := range []struct {
		command string
		args    []string
	}{
		{"uvx", []string{"--from", "acme-internal-tool @ git+https://git.acme.internal/x.git", "acme"}},
		{"uvx", []string{"--from", "acme-internal-tool@git+https://git.acme.internal/x.git", "acme"}},
		{"uvx", []string{"--from", "acme_tool-1.0-py3-none-any.whl", "acme"}},
		{"uvx", []string{"--from", "acme-tool@", "acme"}},
		{"uvx", []string{"acme-tool@file:///src/acme"}},
		{"uvx", []string{"--from", "acme-tool-1.0.tar.gz", "acme"}},
		{"uvx", []string{"--from", "git+https://git.acme.internal/x.git", "acme"}},
		{"uvx", []string{"--from", "./tools/acme", "acme"}},
		{"pipx", []string{"run", "--spec", "https://files.acme.internal/acme-1.0.zip", "acme"}},
		{"npx", []string{"-y", "acme-tool@git+https://git.acme.internal/x.git"}},
		{"npx", []string{"-y", "acme-tool@github:acme/tool"}},
		{"npx", []string{"-y", "@acme/tool@https://files.acme.internal/tool.tgz"}},
		{"npx", []string{"-y", "acme-tool@npm:@acme/real-tool"}},
		{"npx", []string{"-y", "acme-tool@file:../tool"}},
		{"npx", []string{"-y", "acme-tool.tgz"}},
	} {
		if p, ok := FromCommand(tc.command, tc.args); ok {
			t.Errorf("FromCommand(%q, %q) = %+v; want no package", tc.command, tc.args, p)
		}
	}
	for _, tc := range []struct {
		command string
		args    []string
		want    string
	}{
		{"uvx", []string{"--from", "mcp-server-fetch==0.6.2", "mcp-server-fetch"}, "pypi:mcp-server-fetch"},
		{"uvx", []string{"mcp-server-fetch@latest"}, "pypi:mcp-server-fetch"},
		{"uvx", []string{"ruff@0.6.0"}, "pypi:ruff"},
		{"uvx", []string{"awslabs.aws-documentation-mcp-server@latest"}, "pypi:awslabs.aws-documentation-mcp-server"},
		{"pipx", []string{"run", "--spec", "pkg@1.0", "pkg"}, "pypi:pkg"},
		{"uvx", []string{"--from", "mcp-server-fetch@0.6.2", "mcp-server-fetch"}, "pypi:mcp-server-fetch"},
		{"uvx", []string{"--from", "mcp-server-fetch[cli]>=0.6 ; python_version >= '3.10'", "x"}, "pypi:mcp-server-fetch"},
		{"npx", []string{"-y", "@playwright/mcp@latest"}, "npm:@playwright/mcp"},
		{"npx", []string{"-y", "pkg@^1.2.3"}, "npm:pkg"},
	} {
		if p, ok := FromCommand(tc.command, tc.args); !ok || p.Ref != tc.want {
			t.Errorf("FromCommand(%q, %q) = %+v, %v; want %q", tc.command, tc.args, p, ok, tc.want)
		}
	}
}

func TestResolveHoldsBackRegistriesFromTheServersEnv(t *testing.T) {
	for _, tc := range []struct {
		key  string
		def  map[string]interface{}
		want string
	}{
		{"npm_config_registry", map[string]interface{}{"command": "npx", "args": []interface{}{"-y", "@acme/internal-mcp"}}, "npm:@acme/internal-mcp"},
		{"NPM_CONFIG_REGISTRY", map[string]interface{}{"command": "npx", "args": []interface{}{"-y", "@acme/internal-mcp"}}, "npm:@acme/internal-mcp"},
		{"npm_config_@acme:registry", map[string]interface{}{"command": "npx", "args": []interface{}{"-y", "@acme/internal-mcp"}}, "npm:@acme/internal-mcp"},
		{"NPM_CONFIG_USERCONFIG", map[string]interface{}{"command": "npx", "args": []interface{}{"-y", "@acme/internal-mcp"}}, "npm:@acme/internal-mcp"},
		{"UV_INDEX_URL", map[string]interface{}{"command": "uvx", "args": []interface{}{"acme-billing-mcp"}}, "pypi:acme-billing-mcp"},
		{"UV_DEFAULT_INDEX", map[string]interface{}{"command": "uvx", "args": []interface{}{"acme-billing-mcp"}}, "pypi:acme-billing-mcp"},
		{"UV_EXTRA_INDEX_URL", map[string]interface{}{"command": "uvx", "args": []interface{}{"acme-billing-mcp"}}, "pypi:acme-billing-mcp"},
		{"PIP_INDEX_URL", map[string]interface{}{"command": "pipx", "args": []interface{}{"run", "acme-billing-mcp"}}, "pypi:acme-billing-mcp"},
		{"PIP_EXTRA_INDEX_URL", map[string]interface{}{"command": "pipx", "args": []interface{}{"run", "acme-billing-mcp"}}, "pypi:acme-billing-mcp"},
		{"NPM_CONFIG_PREFIX", map[string]interface{}{"command": "npx", "args": []interface{}{"-y", "@acme/internal-mcp"}}, "npm:@acme/internal-mcp"},
	} {
		def := map[string]interface{}{"env": map[string]interface{}{tc.key: "https://registry.acme.internal"}}
		for k, v := range tc.def {
			def[k] = v
		}
		if p, ok := Resolve(def, nil, Registries{}); !ok || p != (Package{Ref: tc.want, Private: true}) {
			t.Errorf("%s: a package from a registry set in env = %+v, %v", tc.key, p, ok)
		}
	}
	// An env file Beacon does not read may set one too.
	envFile := map[string]interface{}{"command": "uvx", "args": []interface{}{"mcp-server-fetch"}, "envFile": "${workspaceFolder}/.env"}
	if p, _ := Resolve(envFile, nil, Registries{}); !p.Private {
		t.Errorf("a server with an envFile was public: %+v", p)
	}
	// Unrelated env keys, secrets included, leave a public package public.
	gh := map[string]interface{}{
		"command": "npx", "args": []interface{}{"-y", "@modelcontextprotocol/server-github"},
		"env": map[string]interface{}{"GITHUB_TOKEN": "[REDACTED]", "NODE_ENV": "production"},
	}
	if p, ok := Resolve(gh, nil, Registries{}); !ok || p.Private {
		t.Fatalf("a public package was held back: %+v", p)
	}
	// A scope the launcher config sends elsewhere is private; other scopes are not.
	scoped := Registries{NPMScopes: map[string]bool{"@acme": true}}
	if p, _ := Resolve(map[string]interface{}{"command": "npx", "args": []interface{}{"-y", "@acme/billing-mcp"}}, nil, scoped); !p.Private {
		t.Errorf("a package in a private scope was public: %+v", p)
	}
	if p, _ := Resolve(map[string]interface{}{"command": "npx", "args": []interface{}{"-y", "@playwright/mcp"}}, nil, scoped); p.Private {
		t.Errorf("a package in a public scope was private: %+v", p)
	}
}
