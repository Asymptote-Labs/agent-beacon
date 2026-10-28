package intel

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/inventory"
)

// What a machine has installed, as Beacon Intel lookups. Like cve.now's check_stack reads a
// lockfile, this reads the MCP servers and skills Beacon's inventory finds in each runtime's
// own config, and names each the way the index does.
//
// By default only a public identifier derived here leaves the machine: an npm or PyPI package
// named in a server's launch command and fetched from the public registry. Names that may be
// private (a server's configured name, a package from a private registry or index, a skill's
// name) are sent only when the caller opts in. Paths, URLs, environment, other arguments and
// the rest of a definition are never sent.

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

	ReasonBeacon         = "Beacon's own"
	ReasonNoPackage      = "no npm or PyPI package Beacon can read with confidence in its launch command; pass include_private_names to look it up by its configured name"
	ReasonRemote         = "a remote URL; pass include_private_names to look it up by its configured name"
	ReasonPrivatePackage = "its package comes from a private registry or index, so its name may be private; pass include_private_names to send it"
	ReasonPrivateSkill   = "a skill's name may be one a person gave a private skill; pass include_private_names to check skills by name"
	ReasonNoName         = "no name to look up"
)

// FromInventoryOptions tunes FromInventory.
type FromInventoryOptions struct {
	// IncludePrivateNames also sends names that may be private: a server's configured name when
	// its command names no package, a package from a private registry or index, and skill
	// names. Off by default, so only packages from the public npm and PyPI registries leave.
	IncludePrivateNames bool
	// BeaconSkills are skill names Beacon installs itself (agentskills.Names), never checked.
	BeaconSkills []string
}

var beaconServers = map[string]bool{"beacon": true, "beacon-managed": true}

// FromInventory turns an inventory scan into deduplicated lookups, servers first, each list in
// a stable order.
func FromInventory(res inventory.Result, opts FromInventoryOptions) ([]Installed, []Skipped) {
	beaconSkills := map[string]bool{}
	for _, name := range opts.BeaconSkills {
		beaconSkills[name] = true
	}
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
		if beaconServers[strings.ToLower(name)] {
			skipped = append(skipped, Skipped{Kind: "mcp", Runtime: s.Runtime, Name: name, Reason: ReasonBeacon})
			continue
		}
		command, args := commandOf(s.Definition)
		if q, basis, ok := PackageFromCommand(command, args); ok {
			if (basis == BasisPrivateNPM || basis == BasisPrivatePyPI) && !opts.IncludePrivateNames {
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
		if beaconSkills[name] {
			skipped = append(skipped, Skipped{Kind: "skill", Runtime: s.Runtime, Name: name, Reason: ReasonBeacon})
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

func commandOf(def map[string]interface{}) (string, []string) {
	if def == nil {
		return "", nil
	}
	command, _ := def["command"].(string)
	var args []string
	if raw, ok := def["args"].([]interface{}); ok {
		for _, a := range raw {
			if s, ok := a.(string); ok {
				args = append(args, s)
			}
		}
	}
	return command, args
}

var (
	npmName  = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9][a-z0-9._~-]*$`)
	pypiName = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$`)
	// A name a person might give a server or skill: no paths, URLs, or whitespace runs.
	plainName = regexp.MustCompile(`^[A-Za-z0-9@][A-Za-z0-9 ._@/+-]{0,99}$`)
)

func validName(name string) bool {
	return plainName.MatchString(name) && !strings.Contains(name, "//") && !strings.Contains(name, "..")
}

// PackageFromCommand names the npm or PyPI package a launch command runs, as a lookup
// (npm:<name> or pypi:<name>) without its version. ok is false for anything else: a local
// script, a binary, a container, or a command it cannot read with confidence.
func PackageFromCommand(command string, args []string) (q, basis string, ok bool) {
	// Some configs put the whole launch line in command.
	if len(args) == 0 && strings.ContainsAny(strings.TrimSpace(command), " \t") {
		fields := strings.Fields(command)
		command, args = fields[0], fields[1:]
	}
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(strings.TrimSpace(command), `\`, "/")))
	base = strings.TrimSuffix(strings.TrimSuffix(base, ".exe"), ".cmd")
	// Windows configs run launchers through the shell: cmd /c npx -y <package>.
	if base == "cmd" && len(args) >= 2 && (strings.EqualFold(args[0], "/c") || strings.EqualFold(args[0], "/k")) {
		return PackageFromCommand(args[1], args[2:])
	}
	rest := args
	switch base {
	case "npx", "bunx", "pnpx":
	case "pnpm", "yarn", "bun":
		if len(rest) == 0 || (rest[0] != "dlx" && rest[0] != "x") {
			return "", "", false
		}
		rest = rest[1:]
	case "npm":
		if len(rest) == 0 || (rest[0] != "exec" && rest[0] != "x") {
			return "", "", false
		}
		rest = rest[1:]
	case "uvx":
		return pypiFrom(rest)
	case "pipx":
		if len(rest) == 0 || rest[0] != "run" {
			return "", "", false
		}
		return pypiFrom(rest[1:])
	case "uv":
		if len(rest) < 2 || rest[0] != "tool" || rest[1] != "run" {
			return "", "", false
		}
		return pypiFrom(rest[2:])
	default:
		return "", "", false
	}
	return npmFrom(rest)
}

// The options a launcher takes before the package it runs. A flag in neither list stops the
// parse: whether it takes a value decides which word is the package, and guessing wrong sends
// a file name or a workspace for a package and leaves the real one unchecked. A package
// fetched from a registry or index the command names is not a public package, and its name
// may be private.
var (
	npmPrivateFlags  = flagSet("--registry")
	pypiPrivateFlags = flagSet("--index", "--index-url", "--extra-index-url", "--default-index", "-f", "--find-links")

	npmPackageFlags = flagSet("-p", "--package")
	npmValueFlags   = flagSet("--registry", "--cache", "--userconfig", "-w", "--workspace", "--node-options", "--loglevel", "--allow-build")
	npmBoolFlags    = flagSet("-y", "--yes", "--no", "--no-install", "-q", "--quiet", "-s", "--silent", "--prefer-offline", "--prefer-online", "--offline", "--ignore-existing", "--bun")

	pypiPackageFlags = flagSet("--from", "--spec")
	pypiValueFlags   = flagSet("-p", "--python", "--with", "--with-editable", "--with-requirements", "--index", "--index-url",
		"--extra-index-url", "--default-index", "-f", "--find-links", "--refresh-package", "-P", "--upgrade-package",
		"--reinstall-package", "--prerelease", "--resolution", "-c", "--constraints", "--overrides", "--env-file",
		"--directory", "--project", "--cache-dir", "--color", "--python-preference", "--exclude-newer", "--link-mode",
		"--keyring-provider", "--index-strategy", "--config-file", "--pip-args")
	pypiBoolFlags = flagSet("-q", "--quiet", "-v", "--verbose", "--offline", "-n", "--no-cache", "--isolated", "--refresh",
		"--system", "--native-tls", "--no-progress", "-U", "--upgrade", "--reinstall", "--no-build", "--system-site-packages")
)

func flagSet(flags ...string) map[string]bool {
	set := make(map[string]bool, len(flags))
	for _, f := range flags {
		set[f] = true
	}
	return set
}

// packageArg finds the package a launcher runs: the value of a package flag, or else the first
// argument that is not an option. ok is false when an unknown option makes that uncertain.
// private reports whether a flag in privateFlags appeared before the package.
func packageArg(args []string, packageFlags, valueFlags, boolFlags, privateFlags map[string]bool) (spec string, private, ok bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, hasValue := strings.Cut(a, "=")
		if !strings.HasPrefix(a, "-") {
			hasValue = false
		}
		if privateFlags[name] {
			private = true
		}
		switch {
		case a == "--":
			continue
		case packageFlags[a]:
			if i+1 < len(args) {
				return args[i+1], private, true
			}
			return "", private, false
		case hasValue:
			if packageFlags[name] {
				return value, private, true
			}
			if !valueFlags[name] && !boolFlags[name] {
				return "", private, false
			}
		case valueFlags[a]:
			i++
		case boolFlags[a]:
		case strings.HasPrefix(a, "-"):
			return "", private, false
		default:
			return a, private, true
		}
	}
	return "", private, false
}

func npmFrom(args []string) (string, string, bool) {
	spec, private, ok := packageArg(args, npmPackageFlags, npmValueFlags, npmBoolFlags, npmPrivateFlags)
	if !ok {
		return "", "", false
	}
	q, basis, ok := npmPackage(spec)
	if ok && private {
		basis = BasisPrivateNPM
	}
	return q, basis, ok
}

func npmPackage(spec string) (string, string, bool) {
	spec = strings.ToLower(strings.TrimSpace(spec))
	name := spec
	if strings.HasPrefix(spec, "@") {
		if at := strings.Index(spec[1:], "@"); at >= 0 {
			name = spec[:at+1]
		}
	} else if at := strings.Index(spec, "@"); at >= 0 {
		name = spec[:at]
	}
	if !npmName.MatchString(name) || len(name) > 214 {
		return "", "", false
	}
	return "npm:" + name, BasisNPM, true
}

func pypiFrom(args []string) (string, string, bool) {
	spec, private, ok := packageArg(args, pypiPackageFlags, pypiValueFlags, pypiBoolFlags, pypiPrivateFlags)
	if !ok {
		return "", "", false
	}
	q, basis, ok := pypiPackage(spec)
	if ok && private {
		basis = BasisPrivatePyPI
	}
	return q, basis, ok
}

func pypiPackage(spec string) (string, string, bool) {
	spec = strings.ToLower(strings.TrimSpace(spec))
	if cut := strings.IndexAny(spec, "[=<>!~;@ "); cut >= 0 {
		spec = spec[:cut]
	}
	// Lowercased only, as the index keys packages: registries list a name as it was published.
	if !pypiName.MatchString(spec) || len(spec) > 100 {
		return "", "", false
	}
	return "pypi:" + spec, BasisPyPI, true
}
