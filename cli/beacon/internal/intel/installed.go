package intel

import (
	"regexp"
	"sort"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/inventory"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/launchpkg"
)

// What a machine has installed, as Beacon Intel lookups. Like cve.now's check_stack reads a
// lockfile, this reads the MCP servers and skills Beacon's inventory finds in each runtime's
// own config, and names each the way the index does.
//
// By default only identifiers the inventory recorded leave the machine: an npm or PyPI package
// in a server's launch command and fetched from the public registry, and the Intel key of a skill
// the skills CLI installed from GitHub (skill:<owner>/<repo>#<name>; Intel keeps nothing it is
// asked, the repository included). Names that may be private (a server's configured name, a
// package from a registry or index named by the command, the server's env, or the launcher's own
// config and environment, a skill's name) are sent only when the caller opts in. Paths, URLs,
// environment, other arguments and the rest of a definition are never sent.

// Installed is one lookup and every place it was found.
type Installed struct {
	Kind    string            `json:"kind"`
	Q       string            `json:"q"`
	Basis   string            `json:"basis"`
	Sources []InstalledSource `json:"found_in"`
}

// InstalledSource is one configured MCP server or skill.
type InstalledSource struct {
	Runtime string `json:"runtime"`
	Name    string `json:"name"`
	Scope   string `json:"scope"`
}

// Skipped is a configured server or skill nothing was sent for, and why.
type Skipped struct {
	Kind    string `json:"kind"`
	Runtime string `json:"runtime"`
	Name    string `json:"name"`
	Reason  string `json:"reason"`
}

const (
	BasisNPM         = "npm package in its launch command"
	BasisPyPI        = "PyPI package in its launch command"
	BasisPrivateNPM  = "npm package from a private registry in its launch command"
	BasisPrivatePyPI = "PyPI package from a private index in its launch command"
	BasisServerName  = "configured server name"
	BasisSkillName   = "skill name"
	BasisSkillSource = "repository the skill was installed from, and its name"

	ReasonBeacon         = "Beacon's own"
	ReasonNoPackage      = "no npm or PyPI package Beacon can read with confidence in its launch command; pass include_private_names to look it up by its configured name"
	ReasonRemote         = "a remote URL; pass include_private_names to look it up by its configured name"
	ReasonPrivatePackage = "its package comes from a private registry or index, so its name may be private; pass include_private_names to send it"
	ReasonPrivateSkill   = "a skill's name may be one a person gave a private skill; pass include_private_names to check skills by name"
	ReasonNoName         = "no name to look up"
)

// IsPrivateBasis reports whether a lookup names a package fetched from a private registry or
// index, which the index can only match against the public package sharing its name.
func IsPrivateBasis(basis string) bool {
	return basis == BasisPrivateNPM || basis == BasisPrivatePyPI
}

// FromInventoryOptions tunes FromInventory.
type FromInventoryOptions struct {
	// IncludePrivateNames also sends names that may be private: a server's configured name when
	// its command names no package, a package from a private registry or index, and the name of
	// a skill with no Intel key. Off by default, so only public packages and skill keys leave.
	IncludePrivateNames bool
	// BeaconSkill reports whether a skill is one Beacon installed itself (an agentskills name
	// in a directory carrying Beacon's marker), never checked. A skill that only shares the
	// name is checked like any other.
	BeaconSkill func(inventory.Skill) bool
}

// beaconServer reports whether a configured server is Beacon's own: the local `beacon` server
// (the beacon binary) or Beacon Cloud MCP (`beacon-managed`, a URL with no command). A server
// that only takes one of the names is checked like any other.
func beaconServer(name string, s inventory.MCPServer) bool {
	switch strings.ToLower(name) {
	case "beacon":
		return launchpkg.Program(s.Launch) == "beacon"
	case "beacon-managed":
		command, _ := launchpkg.Command(s.Launch)
		return s.URLPresent && !s.CommandPresent && command == ""
	}
	return false
}

// serverPackage is the package the scan found a server runs, and its basis.
func serverPackage(s inventory.MCPServer) (q, basis string, ok bool) {
	switch s.PackageRegistry {
	case inventory.PackageRegistryPublic:
		q = s.Package
	case inventory.PackageRegistryPrivate:
		q = s.PrivatePackage
	}
	private := s.PackageRegistry == inventory.PackageRegistryPrivate
	switch {
	case strings.HasPrefix(q, "npm:") && private:
		return q, BasisPrivateNPM, true
	case strings.HasPrefix(q, "npm:"):
		return q, BasisNPM, true
	case strings.HasPrefix(q, "pypi:") && private:
		return q, BasisPrivatePyPI, true
	case strings.HasPrefix(q, "pypi:"):
		return q, BasisPyPI, true
	}
	return "", "", false
}

// FromInventory turns an inventory scan into deduplicated lookups, servers first, each list in
// a stable order.
func FromInventory(res inventory.Result, opts FromInventoryOptions) ([]Installed, []Skipped) {
	byKey := map[string]*Installed{}
	var order []string
	var skipped []Skipped
	add := func(kind, q, basis string, src InstalledSource) {
		key := kind + "\x00" + q
		item, ok := byKey[key]
		if !ok {
			item = &Installed{Kind: kind, Q: q, Basis: basis}
			byKey[key] = item
			order = append(order, key)
		} else if IsPrivateBasis(basis) {
			// One source fetching the package from a private registry or index makes every
			// match a match by name for that source, so the private basis wins.
			item.Basis = basis
		}
		for _, s := range item.Sources {
			if s == src {
				return
			}
		}
		item.Sources = append(item.Sources, src)
	}

	for _, s := range res.MCPServers {
		name := strings.TrimSpace(s.ServerName)
		src := InstalledSource{Runtime: s.Runtime, Name: name, Scope: s.SourceScope}
		if beaconServer(name, s) {
			skipped = append(skipped, Skipped{Kind: "mcp", Runtime: s.Runtime, Name: name, Reason: ReasonBeacon})
			continue
		}
		if q, basis, ok := serverPackage(s); ok {
			if IsPrivateBasis(basis) && !opts.IncludePrivateNames {
				skipped = append(skipped, Skipped{Kind: "mcp", Runtime: s.Runtime, Name: name, Reason: ReasonPrivatePackage})
				continue
			}
			add("mcp", q, basis, src)
			continue
		}
		switch {
		case opts.IncludePrivateNames && validName(name):
			add("mcp", name, BasisServerName, src)
		case name == "" || !validName(name):
			skipped = append(skipped, Skipped{Kind: "mcp", Runtime: s.Runtime, Name: name, Reason: ReasonNoName})
		case s.URLPresent && !s.CommandPresent:
			skipped = append(skipped, Skipped{Kind: "mcp", Runtime: s.Runtime, Name: name, Reason: ReasonRemote})
		default:
			skipped = append(skipped, Skipped{Kind: "mcp", Runtime: s.Runtime, Name: name, Reason: ReasonNoPackage})
		}
	}
	for _, s := range res.Skills {
		name := strings.TrimSpace(s.SkillName)
		if !s.Exists {
			continue
		}
		if opts.BeaconSkill != nil && opts.BeaconSkill(s) {
			skipped = append(skipped, Skipped{Kind: "skill", Runtime: s.Runtime, Name: name, Reason: ReasonBeacon})
			continue
		}
		// The repository the skills CLI installed it from names the skill exactly; a name alone
		// may match a different skill that shares it, and may be private.
		if s.IntelKey != "" {
			add("skill", s.IntelKey, BasisSkillSource, InstalledSource{Runtime: s.Runtime, Name: name, Scope: s.SourceScope})
			continue
		}
		if !validName(name) {
			skipped = append(skipped, Skipped{Kind: "skill", Runtime: s.Runtime, Name: name, Reason: ReasonNoName})
			continue
		}
		if !opts.IncludePrivateNames {
			skipped = append(skipped, Skipped{Kind: "skill", Runtime: s.Runtime, Name: name, Reason: ReasonPrivateSkill})
			continue
		}
		add("skill", name, BasisSkillName, InstalledSource{Runtime: s.Runtime, Name: name, Scope: s.SourceScope})
	}

	out := make([]Installed, 0, len(order))
	for _, key := range order {
		item := byKey[key]
		sort.Slice(item.Sources, func(i, j int) bool {
			a, b := item.Sources[i], item.Sources[j]
			if a.Runtime != b.Runtime {
				return a.Runtime < b.Runtime
			}
			if a.Name != b.Name {
				return a.Name < b.Name
			}
			return a.Scope < b.Scope
		})
		out = append(out, *item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == "mcp"
		}
		return out[i].Q < out[j].Q
	})
	sort.SliceStable(skipped, func(i, j int) bool {
		if skipped[i].Kind != skipped[j].Kind {
			return skipped[i].Kind == "mcp"
		}
		if skipped[i].Runtime != skipped[j].Runtime {
			return skipped[i].Runtime < skipped[j].Runtime
		}
		return skipped[i].Name < skipped[j].Name
	})
	return out, skipped
}

// A name a person might give a server or skill: no paths, URLs, or whitespace runs.
var plainName = regexp.MustCompile(`^[A-Za-z0-9@][A-Za-z0-9 ._@/+-]{0,99}$`)

func validName(name string) bool {
	return plainName.MatchString(name) && !strings.Contains(name, "//") && !strings.Contains(name, "..")
}
