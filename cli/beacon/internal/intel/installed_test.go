package intel

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/inventory"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/launchpkg"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/intelapi"
)

// server is a configured server as the inventory scan leaves it: its launch definition, and the
// package it runs as the scan resolved it with no launcher config of the machine's own.
func server(runtime, name, scope string, def map[string]interface{}) inventory.MCPServer {
	_, hasURL := def["url"]
	_, hasCommand := def["command"]
	s := inventory.MCPServer{Runtime: runtime, ServerName: name, SourceScope: scope, Launch: def, URLPresent: hasURL, CommandPresent: hasCommand}
	if p, ok := launchpkg.Resolve(def, nil, launchpkg.Registries{}); ok {
		s.RecordPackage(p)
	}
	return s
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
	beaconSkill := func(s inventory.Skill) bool { return s.SkillName == "beacon-memory-recall" }
	items, skipped := FromInventory(res, FromInventoryOptions{BeaconSkill: beaconSkill})

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
	withNames, _ := FromInventory(res, FromInventoryOptions{IncludePrivateNames: true, BeaconSkill: beaconSkill})
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

// A skill the skills CLI installed from GitHub is checked by default by its exact key; a skill
// with only a name waits for consent.
func TestFromInventoryChecksASkillByItsKey(t *testing.T) {
	res := inventory.Result{Skills: []inventory.Skill{
		{Runtime: "claude_code", SkillName: "frontend-design", SourceScope: "user", Exists: true, IntelKey: "skill:anthropics/skills#frontend-design"},
		{Runtime: "agent_skills", SkillName: "frontend-design", SourceScope: "user", Exists: true, IntelKey: "skill:anthropics/skills#frontend-design"},
		{Runtime: "claude_code", SkillName: "local", SourceScope: "user", Exists: true},
	}}
	items, skipped := FromInventory(res, FromInventoryOptions{})
	if len(items) != 1 || items[0].Q != "skill:anthropics/skills#frontend-design" || items[0].Basis != BasisSkillSource || len(items[0].Sources) != 2 {
		t.Fatalf("without consent: items %+v", items)
	}
	if len(skipped) != 1 || skipped[0].Name != "local" || skipped[0].Reason != ReasonPrivateSkill {
		t.Fatalf("without consent: skipped %+v", skipped)
	}
	items, _ = FromInventory(res, FromInventoryOptions{IncludePrivateNames: true})
	if len(items) != 2 || items[0].Q != "local" || items[0].Basis != BasisSkillName {
		t.Fatalf("with consent = %+v", items)
	}
}

func TestFromInventoryHoldsBackPrivatePackages(t *testing.T) {
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

func TestFromInventoryKnowsBeaconByDefinitionNotName(t *testing.T) {
	res := inventory.Result{
		MCPServers: []inventory.MCPServer{
			server("claude_code", "beacon", "user", map[string]interface{}{"command": "/opt/beacon/bin/beacon", "args": []interface{}{"mcp", "serve"}}),
			server("codex", "beacon-managed", "user", map[string]interface{}{"url": "https://beacon.sh/mcp"}),
			// Servers that only take Beacon's names are checked like any other.
			server("cursor", "beacon", "user", map[string]interface{}{"command": "npx", "args": []interface{}{"-y", "some-pkg"}}),
			server("cursor", "BEACON-MANAGED", "project", map[string]interface{}{"command": "uvx", "args": []interface{}{"other-pkg"}}),
		},
		Skills: []inventory.Skill{
			{Runtime: "claude_code", SkillName: "beacon-memory-recall", ManifestPath: "/home/me/.claude/skills/beacon-memory-recall/SKILL.md", SourceScope: "user", Exists: true},
			{Runtime: "codex", SkillName: "beacon-memory-recall", ManifestPath: "/home/me/project/.agents/skills/beacon-memory-recall/SKILL.md", SourceScope: "project", Exists: true},
		},
	}
	beaconSkill := func(s inventory.Skill) bool {
		return s.ManifestPath == "/home/me/.claude/skills/beacon-memory-recall/SKILL.md"
	}
	items, skipped := FromInventory(res, FromInventoryOptions{IncludePrivateNames: true, BeaconSkill: beaconSkill})
	var got []string
	for _, item := range items {
		got = append(got, item.Kind+" "+item.Q+" "+item.Sources[0].Runtime)
	}
	if strings.Join(got, ", ") != "mcp npm:some-pkg cursor, mcp pypi:other-pkg cursor, skill beacon-memory-recall codex" {
		t.Fatalf("items = %v", got)
	}
	var beacon []string
	for _, s := range skipped {
		if s.Reason == ReasonBeacon {
			beacon = append(beacon, s.Kind+" "+s.Runtime+" "+s.Name)
		}
	}
	if strings.Join(beacon, ", ") != "mcp claude_code beacon, mcp codex beacon-managed, skill claude_code beacon-memory-recall" {
		t.Fatalf("Beacon's own = %v (skipped %+v)", beacon, skipped)
	}
}

func TestFromInventoryKeepsThePrivateBasisWhicheverSourceComesFirst(t *testing.T) {
	public := server("cursor", "a", "user", map[string]interface{}{"command": "npx", "args": []interface{}{"-y", "foo"}})
	private := server("claude_code", "b", "user", map[string]interface{}{"command": "npx", "args": []interface{}{"--registry=https://npm.corp.example", "-y", "foo"}})
	for _, order := range [][]inventory.MCPServer{{public, private}, {private, public}} {
		items, _ := FromInventory(inventory.Result{MCPServers: order}, FromInventoryOptions{IncludePrivateNames: true})
		if len(items) != 1 || items[0].Q != "npm:foo" || items[0].Basis != BasisPrivateNPM || len(items[0].Sources) != 2 {
			t.Errorf("order %s, %s: items = %+v", order[0].ServerName, order[1].ServerName, items)
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
