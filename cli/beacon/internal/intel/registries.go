package intel

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Registries is what the machine's own launcher configuration says about where packages come
// from. A launcher reads more than its command line: npm reads .npmrc and npm_config_*, yarn and
// bun their own files, pip pip.conf and PIP_*, uv uv.toml and UV_*. When any of them points npm
// or PyPI at a registry or index other than the public one, a package of that ecosystem (or of
// that npm scope) may be private, so FromInventory holds it back like a --registry on the
// command line.
type Registries struct {
	// NPM is set when every npm package may come from another registry.
	NPM bool
	// NPMScopes are the npm scopes (as "@scope") set to another registry.
	NPMScopes map[string]bool
	// PyPI is set when a PyPI package may come from another index.
	PyPI bool
}

// RegistryEnv is where LocalRegistries looks: the user's home, the working directory a launcher
// would run in (empty to skip project files), the environment the launcher inherits, and
// whether to read the system-wide files under /etc and the package managers' prefixes.
type RegistryEnv struct {
	Home       string
	WorkingDir string
	Environ    []string
	System     bool
}

// CurrentRegistryEnv is this process's home, environment and working directory. An agent
// starts every launcher, user-scoped servers' included, with the same environment and in the
// same project, so the project's config applies whether or not project servers are listed.
func CurrentRegistryEnv() RegistryEnv {
	env := RegistryEnv{Environ: os.Environ(), System: true}
	env.Home, _ = os.UserHomeDir()
	env.WorkingDir, _ = os.Getwd()
	return env
}

// projectDirs are the working directory and every directory above it: npm, yarn, bun and uv
// each find a project's config by walking up from where they run.
func projectDirs(wd string) []string {
	if wd == "" {
		return nil
	}
	var dirs []string
	for dir := filepath.Clean(wd); ; {
		dirs = append(dirs, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			return dirs
		}
		dir = parent
	}
}

const maxRegistryConfigBytes = 1 << 20

// LocalRegistries reads the registry and index settings npm, yarn, bun, pip and uv would read.
// A file it cannot read counts as absent. Settings that name the public registry do not count.
func LocalRegistries(env RegistryEnv) Registries {
	reg := Registries{NPMScopes: map[string]bool{}}
	vars, lower := map[string]string{}, map[string]string{}
	for _, kv := range env.Environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			vars[k] = v
			lower[strings.ToLower(k)] = v
			reg.addEnv(k, v)
		}
	}
	// npm reads npm_config_* in any case, and Windows names variables without regard to case.
	getenv := func(k string) string {
		if v, ok := vars[k]; ok {
			return v
		}
		return lower[strings.ToLower(k)]
	}
	home := env.Home
	join := func(base string, parts ...string) string {
		if base == "" {
			return ""
		}
		return filepath.Join(append([]string{base}, parts...)...)
	}
	xdg := getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = join(home, ".config")
	}
	appData := getenv("APPDATA")
	system := func(path string) string {
		if !env.System {
			return ""
		}
		return path
	}

	// npm and pnpm: user, project and global npmrc.
	userNpmrc := getenv("npm_config_userconfig")
	if userNpmrc == "" {
		userNpmrc = join(home, ".npmrc")
	}
	projects := projectDirs(env.WorkingDir)
	inProjects := func(names ...string) []string {
		var paths []string
		for _, dir := range projects {
			for _, name := range names {
				paths = append(paths, filepath.Join(dir, name))
			}
		}
		return paths
	}
	npmrcs := append([]string{userNpmrc, getenv("npm_config_globalconfig")}, inProjects(".npmrc")...)
	// The global npmrc and npm's builtin one live under npm's prefix, which npm derives from the
	// node it runs on (nvm, fnm, Volta and system installs each have their own).
	for _, prefix := range npmPrefixes(getenv) {
		npmrcs = append(npmrcs, join(prefix, "etc", "npmrc"), join(prefix, "lib", "node_modules", "npm", "npmrc"),
			join(prefix, "node_modules", "npm", "npmrc"))
	}
	for _, path := range append(npmrcs, system("/etc/npmrc"),
		// pnpm's global config (pnpm config set --location=global), npmrc format.
		join(xdg, "pnpm", "rc"), join(home, "Library", "Preferences", "pnpm", "rc"), join(getenv("LOCALAPPDATA"), "pnpm", "config", "rc"),
		system("/usr/local/etc/npmrc"), system("/opt/homebrew/etc/npmrc"), join(appData, "npm", "etc", "npmrc")) {
		reg.readNpmrc(path)
	}
	// yarn and bun keep their registries in their own files.
	// YARN_RC_FILENAME renames the rc file yarn looks for in the project and home directories.
	yarnrc := ".yarnrc.yml"
	if name := getenv("YARN_RC_FILENAME"); name != "" && !strings.ContainsAny(name, `/\`) {
		yarnrc = name
	}
	for _, path := range append([]string{join(home, yarnrc), join(home, ".bunfig.toml"), join(xdg, ".bunfig.toml")},
		inProjects(yarnrc, "bunfig.toml")...) {
		if registryLine(path, isNPMRegistryLine) {
			reg.NPM = true
		}
	}
	// pip (and pipx, which runs pip): every config file pip reads.
	for _, path := range []string{getenv("PIP_CONFIG_FILE"), join(home, ".pip", "pip.conf"), join(xdg, "pip", "pip.conf"),
		join(home, "Library", "Application Support", "pip", "pip.conf"), join(appData, "pip", "pip.ini"), join(home, "pip", "pip.ini"),
		system("/etc/pip.conf"), system("/etc/xdg/pip/pip.conf"), system("/Library/Application Support/pip/pip.conf"),
		join(getenv("ProgramData"), "pip", "pip.ini"), join(getenv("VIRTUAL_ENV"), "pip.conf")} {
		if registryLine(path, isPyPIIndexLine) {
			reg.PyPI = true
		}
	}
	// uv: user, system and project configuration.
	for _, path := range append([]string{getenv("UV_CONFIG_FILE"), join(xdg, "uv", "uv.toml"), join(appData, "uv", "uv.toml"),
		system("/etc/uv/uv.toml"), system("/etc/xdg/uv/uv.toml"), join(getenv("ProgramData"), "uv", "uv.toml")},
		inProjects("uv.toml", "pyproject.toml")...) {
		if registryLine(path, isPyPIIndexLine) {
			reg.PyPI = true
		}
	}
	// Site-wide pip and uv files in each $XDG_CONFIG_DIRS directory.
	for _, dir := range filepath.SplitList(getenv("XDG_CONFIG_DIRS")) {
		if registryLine(join(dir, "pip", "pip.conf"), isPyPIIndexLine) || registryLine(join(dir, "uv", "uv.toml"), isPyPIIndexLine) {
			reg.PyPI = true
		}
	}
	return reg
}

// npmPrefixes are the places npm's global prefix may be: npm_config_prefix, or the
// installation of the first node on PATH (its parent directory's parent on Unix, its own
// directory on Windows), both as found and with symlinks resolved.
func npmPrefixes(getenv func(string) string) []string {
	if prefix := getenv("npm_config_prefix"); prefix != "" {
		return []string{prefix}
	}
	for _, dir := range filepath.SplitList(getenv("PATH")) {
		if dir == "" {
			continue
		}
		for _, name := range []string{"node", "node.exe"} {
			node := filepath.Join(dir, name)
			if info, err := os.Stat(node); err != nil || info.IsDir() {
				continue
			}
			prefixes := []string{filepath.Dir(dir), dir}
			if real, err := filepath.EvalSymlinks(node); err == nil && real != node {
				prefixes = append(prefixes, filepath.Dir(filepath.Dir(real)), filepath.Dir(real))
			}
			return prefixes
		}
	}
	return nil
}

// private reports whether a package lookup (npm:<name> or pypi:<name>) may come from another
// registry or index.
func (r Registries) private(q string) bool {
	switch {
	case strings.HasPrefix(q, "npm:"):
		if r.NPM {
			return true
		}
		name := strings.TrimPrefix(q, "npm:")
		if scope, _, ok := strings.Cut(name, "/"); ok && strings.HasPrefix(scope, "@") {
			return r.NPMScopes[scope]
		}
	case strings.HasPrefix(q, "pypi:"):
		return r.PyPI
	}
	return false
}

// addEnv records an inherited environment variable that sets a registry or index.
func (r *Registries) addEnv(key, value string) {
	switch strings.ToLower(key) {
	case "npm_config_userconfig", "npm_config_globalconfig", "yarn_rc_filename", "uv_config_file", "pip_config_file":
		// Names a config file, which LocalRegistries reads like the default one.
		return
	}
	npm, pypi := registryEnvKey(key)
	switch {
	case npm && !publicRegistry(value):
		k := strings.ToLower(key)
		if scope, ok := strings.CutSuffix(strings.TrimPrefix(k, "npm_config_"), ":registry"); ok && strings.HasPrefix(scope, "@") {
			r.NPMScopes[scope] = true
		} else {
			r.NPM = true
		}
	case pypi && !publicRegistry(value):
		r.PyPI = true
	}
}

func (r *Registries) readNpmrc(path string) {
	lines, ok := readConfigLines(path)
	if !ok {
		return
	}
	for _, line := range lines {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch {
		case key == "registry":
			if !publicRegistry(value) {
				r.NPM = true
			}
		case strings.HasPrefix(key, "@") && strings.HasSuffix(key, ":registry"):
			if !publicRegistry(value) {
				r.NPMScopes[strings.TrimSuffix(key, ":registry")] = true
			}
		case key == "userconfig" || key == "globalconfig":
			// A file that points npm at another config file: Beacon does not follow it, so
			// it cannot tell where packages come from.
			r.NPM = true
		}
	}
}

// registryEnvKey reports whether an environment variable can point npm-family launchers (npm)
// or pip-family launchers (pypi) at a registry, index or config file of its own.
func registryEnvKey(key string) (npm, pypi bool) {
	k := strings.ToLower(strings.TrimSpace(key))
	switch {
	// npm_config_registry, npm_config_@scope:registry, npm_config_userconfig.
	case strings.HasPrefix(k, "npm_config_") && (strings.HasSuffix(k, "registry") || k == "npm_config_userconfig" || k == "npm_config_globalconfig"):
		return true, false
	case k == "yarn_npm_registry_server" || k == "yarn_rc_filename" || k == "bun_config_registry":
		return true, false
	case k == "uv_index" || k == "uv_index_url" || k == "uv_default_index" || k == "uv_extra_index_url" ||
		k == "uv_find_links" || k == "uv_config_file":
		return false, true
	case k == "pip_index_url" || k == "pip_extra_index_url" || k == "pip_find_links" || k == "pip_config_file" ||
		k == "pipx_default_index_url":
		return false, true
	}
	return false, false
}

// publicRegistry reports whether a setting names the public npm registry or PyPI index, which
// leaves packages public. Anything else, including a value Beacon cannot read, does not.
func publicRegistry(value string) bool {
	v := strings.ToLower(strings.TrimSpace(strings.Trim(strings.TrimSpace(value), `"'`)))
	v = strings.TrimRight(v, "/")
	switch v {
	case "https://registry.npmjs.org", "https://registry.yarnpkg.com", "https://pypi.org/simple", "https://pypi.python.org/simple":
		return true
	}
	return false
}

func isNPMRegistryLine(line string) bool {
	if strings.HasPrefix(strings.TrimSpace(line), "[install.scopes") {
		return true
	}
	key, value, ok := cutKeyValue(line)
	if !ok {
		return false
	}
	switch strings.ToLower(key) {
	case "npmregistryserver", "registry":
		return !publicRegistry(value)
	case "npmscopes":
		return true
	}
	return false
}

func isPyPIIndexLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "[[index]]" || trimmed == "[[tool.uv.index]]" {
		return true
	}
	key, value, ok := cutKeyValue(line)
	if !ok {
		return false
	}
	switch strings.ReplaceAll(strings.ToLower(key), "_", "-") {
	case "index-url", "extra-index-url", "default-index", "index":
		return !publicRegistry(value)
	case "find-links":
		return true
	}
	return false
}

// cutKeyValue splits an ini, TOML or YAML line at its first '=' or ':'.
func cutKeyValue(line string) (key, value string, ok bool) {
	i := strings.IndexAny(line, "=:")
	if i <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
}

func registryLine(path string, match func(string) bool) bool {
	lines, ok := readConfigLines(path)
	if !ok {
		return false
	}
	for _, line := range lines {
		if match(line) {
			return true
		}
	}
	return false
}

// readConfigLines returns a config file's lines without blanks or comments.
func readConfigLines(path string) ([]string, bool) {
	if path == "" {
		return nil, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, false
	}
	var lines []string
	scanner := bufio.NewScanner(io.LimitReader(f, maxRegistryConfigBytes))
	scanner.Buffer(make([]byte, 64*1024), maxRegistryConfigBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		lines = append(lines, line)
	}
	return lines, true
}
