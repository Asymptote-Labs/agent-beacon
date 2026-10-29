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
// private (a server's configured name, a package from a registry or index named by the command,
// the server's env, or the launcher's own config and environment, a skill's name) are sent only when the caller opts in. Paths, URLs, environment, other arguments and
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
	// Registries is what the machine's launcher configuration says about registries and
	// indexes (LocalRegistries); a package it may route elsewhere is held back as private.
	Registries Registries
	// BeaconSkill reports whether a skill is one Beacon installed itself (an agentskills name
	// in a directory carrying Beacon's marker), never checked. A skill that only shares the
	// name is checked like any other.
	BeaconSkill func(inventory.Skill) bool
}

// beaconServer reports whether a configured server is Beacon's own: the local `beacon` server
// (the beacon binary) or Beacon Cloud MCP (`beacon-managed`, a URL with no command). A server
// that only takes one of the names is checked like any other.
func beaconServer(name, command string, args []string, s inventory.MCPServer) bool {
	switch strings.ToLower(name) {
	case "beacon":
		program, _ := launcher(command, args)
		return launcherBase(program) == "beacon"
	case "beacon-managed":
		return s.URLPresent && !s.CommandPresent && command == ""
	}
	return false
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
		command, args := commandOf(s.Definition)
		if beaconServer(name, command, args, s) {
			skipped = append(skipped, Skipped{Kind: "mcp", Runtime: s.Runtime, Name: name, Reason: ReasonBeacon})
			continue
		}
		if q, basis, ok := PackageFromCommand(command, args); ok {
			program, programArgs := launcher(command, args)
			if registryFromEnv(s.Definition) || opts.Registries.forServer(program, programArgs, s.Definition).private(q) {
				basis = privateBasis(basis)
			}
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
		if opts.BeaconSkill != nil && opts.BeaconSkill(s) {
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

// privateBasis is the basis of a package fetched from a registry or index other than the
// public one.
func privateBasis(basis string) string {
	switch basis {
	case BasisNPM:
		return BasisPrivateNPM
	case BasisPyPI:
		return BasisPrivatePyPI
	}
	return basis
}

// registryFromEnv reports whether a server's own definition can point its launcher at a
// registry, index or config file: a registry variable in its env block, or an env file Beacon
// does not read. Only the keys are read; their values never leave here.
func registryFromEnv(def map[string]interface{}) bool {
	if _, ok := def["envFile"]; ok {
		return true
	}
	if _, ok := def["env_file"]; ok {
		return true
	}
	env, _ := def["env"].(map[string]interface{})
	for key := range env {
		if npm, pypi := registryEnvKey(key); npm || pypi {
			return true
		}
	}
	return false
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

// launcher is the program a launch command runs and its arguments: a whole launch line in
// command is split, and Windows' cmd /c or /k wrapper (cmd /c npx -y <package>) is unwrapped.
func launcher(command string, args []string) (string, []string) {
	for {
		if len(args) == 0 && strings.ContainsAny(strings.TrimSpace(command), " \t") {
			fields := strings.Fields(command)
			command, args = fields[0], fields[1:]
		}
		if launcherBase(command) == "cmd" && len(args) >= 2 && (strings.EqualFold(args[0], "/c") || strings.EqualFold(args[0], "/k")) {
			command, args = args[1], args[2:]
			continue
		}
		return command, args
	}
}

func launcherBase(command string) string {
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(strings.TrimSpace(command), `\`, "/")))
	return strings.TrimSuffix(strings.TrimSuffix(base, ".exe"), ".cmd")
}

// PackageFromCommand names the npm or PyPI package a launch command runs, as a lookup
// (npm:<name> or pypi:<name>) without its version. ok is false for anything else: a local
// script, a binary, a container, or a command it cannot read with confidence.
func PackageFromCommand(command string, args []string) (q, basis string, ok bool) {
	command, args = launcher(command, args)
	base := launcherBase(command)
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
		// npm exec and npm x read npm's options after the package too, up to --.
		return npmFrom(rest[1:], true)
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
	return npmFrom(rest, false)
}

// The options a launcher takes before the package it runs. A flag in neither list stops the
// parse: whether it takes a value decides which word is the package, and guessing wrong sends
// a file name or a workspace for a package and leaves the real one unchecked. A package
// fetched from a registry or index the command names is not a public package, and its name
// may be private.
var (
	// A config file or pip's own arguments can name a registry or index too, so they count.
	npmPrivateFlags  = flagSet("--registry", "--userconfig")
	pypiPrivateFlags = flagSet("--index", "--index-url", "--extra-index-url", "--default-index", "-f", "--find-links",
		"--config-file", "--pip-args")

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
// private reports whether a flag in privateFlags appeared anywhere among the launcher's own
// options, including after a package flag: `uvx --from pkg --index-url <url> cmd` fetches pkg
// from that index too. With optionsAnywhere (npm exec and npm x), the launcher's options run
// past the first plain word up to --, so they are read there too: a registry or package flag
// after the package still counts, and an unknown option still stops the parse.
func packageArg(args []string, packageFlags, valueFlags, boolFlags, privateFlags map[string]bool, optionsAnywhere bool) (spec string, private, ok bool) {
	found, positional := false, false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" && optionsAnywhere {
			if !found && !positional && i+1 < len(args) {
				spec, positional = args[i+1], true
			}
			break
		}
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
			if i+1 >= len(args) {
				return "", private, false
			}
			i++
			if !found {
				spec, found = args[i], true
			}
		case hasValue:
			if packageFlags[name] {
				if !found {
					spec, found = value, true
				}
				continue
			}
			if !valueFlags[name] && !boolFlags[name] {
				return "", private, false
			}
		case valueFlags[a]:
			i++
		case boolFlags[a]:
		case strings.HasPrefix(a, "-"):
			return "", private, false
		case positional:
			// A later plain word is an argument to the command.
		default:
			// The first plain word is the package, or the command a package flag's package
			// provides; for every launcher but npm exec, the launcher's options end here.
			if !found {
				spec = a
			}
			if !optionsAnywhere {
				return spec, private, true
			}
			positional = true
		}
	}
	return spec, private, found || positional
}

func npmFrom(args []string, optionsAnywhere bool) (string, string, bool) {
	spec, private, ok := packageArg(args, npmPackageFlags, npmValueFlags, npmBoolFlags, npmPrivateFlags, optionsAnywhere)
	if !ok {
		return "", "", false
	}
	q, basis, ok := npmPackage(spec)
	if ok && private {
		basis = BasisPrivateNPM
	}
	return q, basis, ok
}

// archiveSuffixes name a local package file rather than a registry package.
var archiveSuffixes = []string{".tgz", ".tar.gz", ".tar.bz2", ".tar.xz", ".zip", ".whl", ".egg"}

func isArchive(spec string) bool {
	for _, suffix := range archiveSuffixes {
		if strings.HasSuffix(spec, suffix) {
			return true
		}
	}
	return false
}

// npmPackage reads a registry spec, <name> or <name>@<version, range or tag>. A spec that
// fetches from anywhere else (a git or tarball URL, a file, an npm: alias) is not a public
// registry package, and its name may be private, so ok is false.
func npmPackage(spec string) (string, string, bool) {
	spec = strings.ToLower(strings.TrimSpace(spec))
	name, version := spec, ""
	if strings.HasPrefix(spec, "@") {
		if at := strings.Index(spec[1:], "@"); at >= 0 {
			name, version = spec[:at+1], spec[at+2:]
		}
	} else if at := strings.Index(spec, "@"); at >= 0 {
		name, version = spec[:at], spec[at+1:]
	}
	if strings.ContainsAny(version, ":/\\") || isArchive(name) || isArchive(version) {
		return "", "", false
	}
	if !npmName.MatchString(name) || len(name) > 214 {
		return "", "", false
	}
	return "npm:" + name, BasisNPM, true
}

func pypiFrom(args []string) (string, string, bool) {
	spec, private, ok := packageArg(args, pypiPackageFlags, pypiValueFlags, pypiBoolFlags, pypiPrivateFlags, false)
	if !ok {
		return "", "", false
	}
	q, basis, ok := pypiPackage(spec)
	if ok && private {
		basis = BasisPrivatePyPI
	}
	return q, basis, ok
}

// pypiPackage reads a PEP 508 requirement for a registry package, or uv's <name>@<version>
// (and @latest). A direct reference (name @ url), a URL, a path or a local wheel or sdist is
// not a public PyPI package, and its name may be private, so ok is false.
func pypiPackage(spec string) (string, string, bool) {
	spec = strings.ToLower(strings.TrimSpace(spec))
	if name, version, ok := strings.Cut(spec, "@"); ok {
		if version == "" || strings.ContainsAny(name, " \t") || strings.ContainsAny(version, ":/\\ \t@") {
			return "", "", false
		}
		spec = name
	}
	if strings.ContainsAny(spec, "/\\") || strings.Contains(spec, "://") || strings.HasPrefix(spec, "git+") || isArchive(spec) {
		return "", "", false
	}
	if cut := strings.IndexAny(spec, "[=<>!~; "); cut >= 0 {
		spec = spec[:cut]
	}
	// Lowercased only, as the index keys packages: registries list a name as it was published.
	if !pypiName.MatchString(spec) || len(spec) > 100 {
		return "", "", false
	}
	return "pypi:" + spec, BasisPyPI, true
}
