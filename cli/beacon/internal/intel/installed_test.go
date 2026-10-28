package intel

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/inventory"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/intelapi"
)

func TestPackageFromCommand(t *testing.T) {
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
		q, basis, ok := PackageFromCommand(tc.command, tc.args)
		if !ok || q != tc.want {
			t.Errorf("PackageFromCommand(%q, %q) = %q, %v; want %q", tc.command, tc.args, q, ok, tc.want)
		}
		if ok && basis != BasisNPM && basis != BasisPyPI && basis != BasisPrivateNPM && basis != BasisPrivatePyPI {
			t.Errorf("basis = %q", basis)
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
		{"", nil},
	} {
		if q, _, ok := PackageFromCommand(tc.command, tc.args); ok {
			t.Errorf("PackageFromCommand(%q, %q) = %q, want no package", tc.command, tc.args, q)
		}
	}
}

func server(runtime, name, scope string, def map[string]interface{}) inventory.MCPServer {
	_, hasURL := def["url"]
	_, hasCommand := def["command"]
	return inventory.MCPServer{Runtime: runtime, ServerName: name, SourceScope: scope, Definition: def, URLPresent: hasURL, CommandPresent: hasCommand}
}

func TestFromInventorySendsOnlyPublicIdentifiers(t *testing.T) {
	secret := "ghp_supersecrettoken123"
	res := inventory.Result{
		MCPServers: []inventory.MCPServer{
			server("claude_code", "playwright", "user", map[string]interface{}{"command": "npx", "args": []interface{}{"-y", "@playwright/mcp@latest", "--token", secret}, "env": map[string]interface{}{"TOKEN": secret}}),
			server("cursor", "pw", "project", map[string]interface{}{"command": "npx", "args": []interface{}{"@playwright/mcp"}}),
			server("cursor", "playwright", "user", map[string]interface{}{"command": "npx", "args": []interface{}{"@playwright/mcp"}}),
			server("codex", "fetch", "user", map[string]interface{}{"command": "uvx", "args": []interface{}{"mcp-server-fetch"}}),
			server("claude_code", "internal-db", "user", map[string]interface{}{"command": "node", "args": []interface{}{"/home/me/company/secret-server.js"}}),
			server("claude_code", "remote", "user", map[string]interface{}{"url": "https://mcp.internal.example/" + secret}),
			server("claude_code", "beacon", "user", map[string]interface{}{"command": "beacon", "args": []interface{}{"mcp", "serve"}}),
			server("claude_code", "Beacon-Managed", "user", map[string]interface{}{"url": "https://beacon.sh/mcp"}),
			server("claude_code", "../../etc/passwd", "user", map[string]interface{}{"command": "node"}),
		},
		Skills: []inventory.Skill{
			{Runtime: "claude_code", SkillName: "pdf", SourceScope: "user", Exists: true},
			{Runtime: "codex", SkillName: "pdf", SourceScope: "user", Exists: true},
			{Runtime: "claude_code", SkillName: "beacon-memory-recall", SourceScope: "user", Exists: true},
			{Runtime: "claude_code", SkillName: "gone", SourceScope: "user", Exists: false},
			{Runtime: "claude_code", SkillName: "/Users/me/skills/x", SourceScope: "user", Exists: true},
		},
	}
	items, skipped := FromInventory(res, FromInventoryOptions{BeaconSkills: []string{"beacon-memory-recall"}})

	var got []string
	for _, item := range items {
		got = append(got, item.Kind+" "+item.Q)
	}
	if strings.Join(got, ", ") != "mcp npm:@playwright/mcp, mcp pypi:mcp-server-fetch" {
		t.Fatalf("items = %v", got)
	}
	if n := len(items[0].Sources); n != 3 || items[0].Sources[0].Runtime != "claude_code" || items[0].Basis != BasisNPM {
		t.Fatalf("playwright sources = %+v", items[0])
	}

	reasons := map[string]string{}
	for _, s := range skipped {
		reasons[s.Kind+" "+s.Name] = s.Reason
	}
	want := map[string]string{
		"mcp internal-db":            ReasonNoPackage,
		"mcp remote":                 ReasonRemote,
		"mcp beacon":                 ReasonBeacon,
		"mcp Beacon-Managed":         ReasonBeacon,
		"mcp ../../etc/passwd":       ReasonNoName,
		"skill pdf":                  ReasonPrivateSkill,
		"skill beacon-memory-recall": ReasonBeacon,
		"skill /Users/me/skills/x":   ReasonNoName,
	}
	for k, v := range want {
		if reasons[k] != v {
			t.Errorf("skipped[%q] = %q, want %q", k, reasons[k], v)
		}
	}
	if len(skipped) != len(want)+1 { // pdf is skipped once per runtime
		t.Errorf("skipped = %+v", skipped)
	}

	// With consent, skills go by name, once for every runtime they are in.
	withNames, _ := FromInventory(res, FromInventoryOptions{IncludePrivateNames: true, BeaconSkills: []string{"beacon-memory-recall"}})
	last := withNames[len(withNames)-1]
	if last.Kind != "skill" || last.Q != "pdf" || len(last.Sources) != 2 || last.Basis != BasisSkillName {
		t.Fatalf("pdf with consent = %+v", last)
	}

	// The lookups that would be sent carry none of the definition.
	var sent []intelapi.CheckItem
	for _, item := range items {
		sent = append(sent, intelapi.CheckItem{Q: item.Q, Kind: item.Kind})
	}
	body, _ := json.Marshal(sent)
	for _, leak := range []string{secret, "/home/me", "company", "internal", "mcp.internal.example", "--token", "TOKEN", "latest"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("what would be sent contains %q: %s", leak, body)
		}
	}
}

func TestPrivateRegistriesAndWindowsShells(t *testing.T) {
	for _, tc := range []struct {
		command string
		args    []string
		want    string
		basis   string
	}{
		{"npx", []string{"--registry", "https://npm.acme.internal", "-y", "@acme/billing-mcp"}, "npm:@acme/billing-mcp", BasisPrivateNPM},
		{"npx", []string{"--registry=https://npm.acme.internal", "@acme/billing-mcp"}, "npm:@acme/billing-mcp", BasisPrivateNPM},
		{"uvx", []string{"--index-url", "https://pypi.acme.internal/simple", "acme-billing-mcp"}, "pypi:acme-billing-mcp", BasisPrivatePyPI},
		{"uvx", []string{"--extra-index-url=https://x.example/simple", "srv"}, "pypi:srv", BasisPrivatePyPI},
		// A private index or registry named after the package flag still applies to that package.
		{"uvx", []string{"--from", "acme-billing-mcp", "--index-url", "https://pypi.acme.internal/simple", "acme-billing"}, "pypi:acme-billing-mcp", BasisPrivatePyPI},
		{"uvx", []string{"--from=acme-billing-mcp", "--index=https://pypi.acme.internal/simple", "acme-billing"}, "pypi:acme-billing-mcp", BasisPrivatePyPI},
		{"npx", []string{"-p", "@acme/mcp", "--registry", "https://npm.acme.internal", "acme-mcp"}, "npm:@acme/mcp", BasisPrivateNPM},
		{"npx", []string{"--package=@acme/mcp", "--registry=https://npm.acme.internal", "acme-mcp"}, "npm:@acme/mcp", BasisPrivateNPM},
		{"uvx", []string{"--from", "mcp-server-fetch", "mcp-server-fetch"}, "pypi:mcp-server-fetch", BasisPyPI},
		// A config file or pip's own arguments can name an index or registry.
		{"npx", []string{"--userconfig", "/x/.npmrc", "@acme/mcp"}, "npm:@acme/mcp", BasisPrivateNPM},
		{"uvx", []string{"--config-file", "/x/uv.toml", "acme-mcp"}, "pypi:acme-mcp", BasisPrivatePyPI},
		{"pipx", []string{"run", "--pip-args=--index-url=https://pypi.acme.internal/simple", "acme-mcp"}, "pypi:acme-mcp", BasisPrivatePyPI},
		{"cmd", []string{"/c", "npx", "-y", "@playwright/mcp"}, "npm:@playwright/mcp", BasisNPM},
		{"C:\\Windows\\System32\\cmd.exe", []string{"/C", "uvx", "mcp-server-fetch"}, "pypi:mcp-server-fetch", BasisPyPI},
		{"cmd /c npx -y @playwright/mcp", nil, "npm:@playwright/mcp", BasisNPM},
		{"C:\\Program Files\\nodejs\\npx.cmd", []string{"-y", "pkg"}, "npm:pkg", BasisNPM},
	} {
		q, basis, ok := PackageFromCommand(tc.command, tc.args)
		if !ok || q != tc.want || basis != tc.basis {
			t.Errorf("PackageFromCommand(%q, %q) = %q, %q, %v; want %q, %q", tc.command, tc.args, q, basis, ok, tc.want, tc.basis)
		}
	}
	if _, _, ok := PackageFromCommand("cmd", []string{"/c", "node", "server.js"}); ok {
		t.Error("cmd /c node was read as a package")
	}

	res := inventory.Result{MCPServers: []inventory.MCPServer{
		server("cursor", "billing", "user", map[string]interface{}{"command": "uvx", "args": []interface{}{"--index-url", "https://pypi.acme.internal/simple", "acme-billing-mcp"}}),
	}}
	items, skipped := FromInventory(res, FromInventoryOptions{})
	if len(items) != 0 || len(skipped) != 1 || skipped[0].Reason != ReasonPrivatePackage {
		t.Fatalf("a private-index package was not held back: %+v %+v", items, skipped)
	}
	items, _ = FromInventory(res, FromInventoryOptions{IncludePrivateNames: true})
	if len(items) != 1 || items[0].Q != "pypi:acme-billing-mcp" || items[0].Basis != BasisPrivatePyPI {
		t.Fatalf("with consent = %+v", items)
	}
}

func TestPackageFromCommandRefusesSpecsOffTheRegistry(t *testing.T) {
	for _, tc := range []struct {
		command string
		args    []string
	}{
		{"uvx", []string{"--from", "acme-internal-tool @ git+https://git.acme.internal/x.git", "acme"}},
		{"uvx", []string{"--from", "acme-internal-tool@git+https://git.acme.internal/x.git", "acme"}},
		{"uvx", []string{"--from", "acme_tool-1.0-py3-none-any.whl", "acme"}},
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
		if q, basis, ok := PackageFromCommand(tc.command, tc.args); ok {
			t.Errorf("PackageFromCommand(%q, %q) = %q, %q; want no package", tc.command, tc.args, q, basis)
		}
	}
	for _, tc := range []struct {
		command string
		args    []string
		want    string
	}{
		{"uvx", []string{"--from", "mcp-server-fetch==0.6.2", "mcp-server-fetch"}, "pypi:mcp-server-fetch"},
		{"uvx", []string{"--from", "mcp-server-fetch[cli]>=0.6 ; python_version >= '3.10'", "x"}, "pypi:mcp-server-fetch"},
		{"npx", []string{"-y", "@playwright/mcp@latest"}, "npm:@playwright/mcp"},
		{"npx", []string{"-y", "pkg@^1.2.3"}, "npm:pkg"},
	} {
		if q, _, ok := PackageFromCommand(tc.command, tc.args); !ok || q != tc.want {
			t.Errorf("PackageFromCommand(%q, %q) = %q, %v; want %q", tc.command, tc.args, q, ok, tc.want)
		}
	}
}

func TestFromInventoryHoldsBackRegistriesFromEnv(t *testing.T) {
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
	} {
		def := map[string]interface{}{"env": map[string]interface{}{tc.key: "https://registry.acme.internal"}}
		for k, v := range tc.def {
			def[k] = v
		}
		res := inventory.Result{MCPServers: []inventory.MCPServer{server("cursor", "internal-tool", "user", def)}}
		items, skipped := FromInventory(res, FromInventoryOptions{})
		if len(items) != 0 || len(skipped) != 1 || skipped[0].Reason != ReasonPrivatePackage {
			t.Errorf("%s: a package from a registry set in env was not held back: %+v %+v", tc.key, items, skipped)
		}
		items, _ = FromInventory(res, FromInventoryOptions{IncludePrivateNames: true})
		if len(items) != 1 || items[0].Q != tc.want || (items[0].Basis != BasisPrivateNPM && items[0].Basis != BasisPrivatePyPI) {
			t.Errorf("%s: with consent = %+v", tc.key, items)
		}
	}

	// Unrelated env keys, secrets included, leave a public package public.
	res := inventory.Result{MCPServers: []inventory.MCPServer{server("cursor", "gh", "user", map[string]interface{}{
		"command": "npx", "args": []interface{}{"-y", "@modelcontextprotocol/server-github"},
		"env": map[string]interface{}{"GITHUB_TOKEN": "[REDACTED]", "NODE_ENV": "production"},
	})}}
	items, skipped := FromInventory(res, FromInventoryOptions{})
	if len(items) != 1 || items[0].Basis != BasisNPM || len(skipped) != 0 {
		t.Fatalf("a public package was held back: %+v %+v", items, skipped)
	}
}

func TestFromInventoryServerNamesAreOptIn(t *testing.T) {
	res := inventory.Result{MCPServers: []inventory.MCPServer{
		server("claude_code", "Desktop Commander", "user", map[string]interface{}{"command": "node", "args": []interface{}{"/x/dc.js"}}),
		server("claude_code", "linear", "user", map[string]interface{}{"url": "https://mcp.linear.app/sse"}),
		server("claude_code", "https://evil.example", "user", map[string]interface{}{"command": "node"}),
	}}
	items, skipped := FromInventory(res, FromInventoryOptions{})
	if len(items) != 0 || len(skipped) != 3 {
		t.Fatalf("without opt-in: items %+v, skipped %+v", items, skipped)
	}
	items, skipped = FromInventory(res, FromInventoryOptions{IncludePrivateNames: true})
	if len(items) != 2 || items[0].Q != "Desktop Commander" || items[1].Q != "linear" || items[0].Basis != BasisServerName {
		t.Fatalf("with opt-in: %+v", items)
	}
	if len(skipped) != 1 || skipped[0].Reason != ReasonNoName {
		t.Fatalf("a URL-shaped name was sent: %+v", skipped)
	}
}

func TestFromInventoryIsStable(t *testing.T) {
	res := inventory.Result{
		MCPServers: []inventory.MCPServer{
			server("b", "z", "user", map[string]interface{}{"command": "npx", "args": []interface{}{"zz"}}),
			server("a", "y", "user", map[string]interface{}{"command": "npx", "args": []interface{}{"aa"}}),
		},
		Skills: []inventory.Skill{{Runtime: "a", SkillName: "s", Exists: true}},
	}
	first, _ := FromInventory(res, FromInventoryOptions{IncludePrivateNames: true})
	res.MCPServers[0], res.MCPServers[1] = res.MCPServers[1], res.MCPServers[0]
	second, _ := FromInventory(res, FromInventoryOptions{IncludePrivateNames: true})
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Fatalf("order depends on input order:\n%s\n%s", a, b)
	}
	if first[0].Q != "npm:aa" || first[2].Kind != "skill" {
		t.Fatalf("order = %+v", first)
	}
}
