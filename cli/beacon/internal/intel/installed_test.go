package intel

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/inventory"
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
	}
	for _, tc := range cases {
		q, basis, ok := PackageFromCommand(tc.command, tc.args)
		if !ok || q != tc.want {
			t.Errorf("PackageFromCommand(%q, %q) = %q, %v; want %q", tc.command, tc.args, q, ok, tc.want)
		}
		if ok && basis != BasisNPM && basis != BasisPyPI {
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
	if strings.Join(got, ", ") != "mcp npm:@playwright/mcp, mcp pypi:mcp-server-fetch, skill pdf" {
		t.Fatalf("items = %v", got)
	}
	if n := len(items[0].Sources); n != 3 || items[0].Sources[0].Runtime != "claude_code" || items[0].Basis != BasisNPM {
		t.Fatalf("playwright sources = %+v", items[0])
	}
	if len(items[2].Sources) != 2 {
		t.Fatalf("pdf sources = %+v", items[2].Sources)
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
		"skill beacon-memory-recall": ReasonBeacon,
		"skill /Users/me/skills/x":   ReasonNoName,
	}
	for k, v := range want {
		if reasons[k] != v {
			t.Errorf("skipped[%q] = %q, want %q", k, reasons[k], v)
		}
	}
	if len(skipped) != len(want) {
		t.Errorf("skipped = %+v", skipped)
	}

	// The lookups that would be sent carry none of the definition.
	var sent []CheckItem
	for _, item := range items {
		sent = append(sent, CheckItem{Q: item.Q, Kind: item.Kind})
	}
	body, _ := json.Marshal(sent)
	for _, leak := range []string{secret, "/home/me", "company", "internal", "mcp.internal.example", "--token", "TOKEN", "latest"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("what would be sent contains %q: %s", leak, body)
		}
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
	items, skipped = FromInventory(res, FromInventoryOptions{IncludeServerNames: true})
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
	first, _ := FromInventory(res, FromInventoryOptions{})
	res.MCPServers[0], res.MCPServers[1] = res.MCPServers[1], res.MCPServers[0]
	second, _ := FromInventory(res, FromInventoryOptions{})
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Fatalf("order depends on input order:\n%s\n%s", a, b)
	}
	if first[0].Q != "npm:aa" || first[2].Kind != "skill" {
		t.Fatalf("order = %+v", first)
	}
}
