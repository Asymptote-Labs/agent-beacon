// Package launchpkg names the npm or PyPI package an MCP server's launch command runs, and tells
// whether its launcher may fetch that package from somewhere other than the public registry or
// index. The inventory scan records it, so a snapshot names a server the way Beacon Intel does
// without carrying the server's arguments, and Beacon Intel's installed check reads the record.
package launchpkg

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Package is the npm or PyPI package a launch command runs.
type Package struct {
	// Ref names it as Beacon Intel looks it up: npm:<name> or pypi:<name>, without a version.
	Ref string `json:"ref"`
	// Private is set when the command, the server's env or its launcher's own configuration may
	// fetch it from a registry or index other than the public one, or when Beacon could not read
	// that configuration. Its name may then be private too.
	Private bool `json:"private"`
}

// Resolve names the package a server's redacted definition runs. registries is what the
// machine's launcher configuration says, and projectDirs are the directories the runtime starts
// the server in (Claude Code's local scope), whose project config its launcher reads too.
func Resolve(def map[string]interface{}, projectDirs []string, registries Registries) (Package, bool) {
	command, args := Command(def)
	p, ok := FromCommand(command, args)
	if !ok {
		return Package{}, false
	}
	program, programArgs := launcher(command, args)
	if registryFromEnv(def) || registries.forServer(program, programArgs, def, projectDirs).private(p.Ref) {
		p.Private = true
	}
	return p, true
}

// Query is one server's question: its redacted launch definition and the directories the runtime
// starts it in.
type Query struct {
	Launch      map[string]interface{} `json:"launch"`
	ProjectDirs []string               `json:"project_dirs,omitempty"`
}

// Answer is a Query's package; OK is false when its command runs none.
type Answer struct {
	Package Package `json:"package"`
	OK      bool    `json:"ok"`
}

// ResolveAll answers each query against the launcher configuration env names, read once. It reads
// that configuration with this process's own rights, so it must run as the user whose launchers
// read it: see the inventory scan's ResolvePackages.
func ResolveAll(env RegistryEnv, queries []Query) []Answer {
	if len(queries) == 0 {
		return nil
	}
	registries := LocalRegistries(env)
	answers := make([]Answer, len(queries))
	for i, q := range queries {
		answers[i].Package, answers[i].OK = Resolve(q.Launch, q.ProjectDirs, registries)
	}
	return answers
}

// Command is a definition's command and its string arguments.
func Command(def map[string]interface{}) (string, []string) {
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

// Program is the program a definition runs, by base name: lowercased, without .exe or .cmd, a
// whole launch line in command split and a Windows cmd /c wrapper unwrapped.
func Program(def map[string]interface{}) string {
	program, _ := launcher(Command(def))
	return launcherBase(program)
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
)

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

// FromCommand names the npm or PyPI package a launch command runs, from the command alone: it
// is private only when the command itself names another registry or index. ok is false for
// anything else: a local script, a binary, a container, or a command it cannot read with
// confidence.
func FromCommand(command string, args []string) (Package, bool) {
	command, args = launcher(command, args)
	base := launcherBase(command)
	rest := args
	switch base {
	case "npx", "bunx", "pnpx":
	case "pnpm", "yarn", "bun":
		if len(rest) == 0 || (rest[0] != "dlx" && rest[0] != "x") {
			return Package{}, false
		}
		rest = rest[1:]
	case "npm":
		if len(rest) == 0 || (rest[0] != "exec" && rest[0] != "x") {
			return Package{}, false
		}
		// npm exec and npm x read npm's options after the package too, up to --.
		return npmFrom(rest[1:], true)
	case "uvx":
		return pypiFrom(rest)
	case "pipx":
		if len(rest) == 0 || rest[0] != "run" {
			return Package{}, false
		}
		return pypiFrom(rest[1:])
	case "uv":
		if len(rest) < 2 || rest[0] != "tool" || rest[1] != "run" {
			return Package{}, false
		}
		return pypiFrom(rest[2:])
	default:
		return Package{}, false
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

func npmFrom(args []string, optionsAnywhere bool) (Package, bool) {
	spec, private, ok := packageArg(args, npmPackageFlags, npmValueFlags, npmBoolFlags, npmPrivateFlags, optionsAnywhere)
	if !ok {
		return Package{}, false
	}
	ref, ok := npmPackage(spec)
	return Package{Ref: ref, Private: private}, ok
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
func npmPackage(spec string) (string, bool) {
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
		return "", false
	}
	if !npmName.MatchString(name) || len(name) > 214 {
		return "", false
	}
	return "npm:" + name, true
}

func pypiFrom(args []string) (Package, bool) {
	spec, private, ok := packageArg(args, pypiPackageFlags, pypiValueFlags, pypiBoolFlags, pypiPrivateFlags, false)
	if !ok {
		return Package{}, false
	}
	ref, ok := pypiPackage(spec)
	return Package{Ref: ref, Private: private}, ok
}

// pypiPackage reads a PEP 508 requirement for a registry package, or uv's <name>@<version>
// (and @latest). A direct reference (name @ url), a URL, a path or a local wheel or sdist is
// not a public PyPI package, and its name may be private, so ok is false.
func pypiPackage(spec string) (string, bool) {
	spec = strings.ToLower(strings.TrimSpace(spec))
	if name, version, ok := strings.Cut(spec, "@"); ok {
		if version == "" || strings.ContainsAny(name, " \t") || strings.ContainsAny(version, ":/\\ \t@") {
			return "", false
		}
		spec = name
	}
	if strings.ContainsAny(spec, "/\\") || strings.Contains(spec, "://") || strings.HasPrefix(spec, "git+") || isArchive(spec) {
		return "", false
	}
	if cut := strings.IndexAny(spec, "[=<>!~; "); cut >= 0 {
		spec = spec[:cut]
	}
	// Lowercased only, as the index keys packages: registries list a name as it was published.
	if !pypiName.MatchString(spec) || len(spec) > 100 {
		return "", false
	}
	return "pypi:" + spec, true
}
