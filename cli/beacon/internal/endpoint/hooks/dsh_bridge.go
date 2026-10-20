package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Whether DeepSeek Harness can actually load the Claude Code hook bridge.
//
// Writing the hooks file and mounting the bridge is not enough on its own. The `dsh` CLI does not
// ship `@deepseek-ai/dsh-hooks-claude-code`: it is only a development dependency of the CLI
// package. A mount by package name therefore resolves only where the bridge has been installed --
// into a profile, with `dsh plugin --profile <name> add ...` -- and a mount nothing can resolve is
// not an error to dsh. The profile boots, prints "1 entry did not activate", and runs with no hooks
// at all, which is an install that reports success and captures nothing.
//
// The bridge also has to match the harness it runs in. Each release pins its dsh peers, including
// `@deepseek-ai/dsh-hook-protocol` (which the CLI does not ship either), to that exact dsh version,
// and both are published for every dsh release. A bridge for another version, or one installed
// without the protocol package, fails to import in the same quiet way.
//
// So before mounting anything Beacon checks, read-only, whether some route resolves a bridge and
// protocol of the running dsh's version:
//
//   - the dsh installation itself, for a release that ships the bridge as a dependency;
//   - each profile under $DSH_HOME/profiles, resolving the package the way Node does from the
//     profile directory, which is where a row naming the package is resolved from; and
//   - an existing patch row, in the home layer or a profile layer, whose `name` is an absolute path
//     to a bridge package and whose `config.configPath` is Beacon's hooks file. That is how a
//     declarative setup (a Nix store path, say) mounts the bridge, and Beacon must neither report it
//     as broken nor add a second mount beside it, which would run every hook twice.
//
// Beacon never installs packages itself: it is local-only, and a package install is a network
// fetch. When nothing resolves, the install refuses and says how the person can add the bridge.

const (
	// dshHookProtocolPackage is the bridge's protocol peer. The CLI does not ship it either.
	dshHookProtocolPackage = "@deepseek-ai/dsh-hook-protocol"
	// dshCLIPackage is the CLI package, whose package.json carries the running version.
	dshCLIPackage = "@deepseek-ai/dsh"
	// dshProfilesDirName is where profiles live under the Harness home.
	dshProfilesDirName = "profiles"
	// dshVersionTimeout bounds `dsh --version`, the fallback when the installation cannot be found
	// from the launcher on PATH.
	dshVersionTimeout = 10 * time.Second
)

// Bridge route statuses.
const (
	DshBridgeOK              = "ok"
	DshBridgeUnverified      = "unverified"       // resolves, but the dsh version is unknown
	DshBridgeMissing         = "missing"          // the bridge package does not resolve
	DshBridgeProtocolMissing = "protocol_missing" // the bridge resolves but its protocol peer does not
	DshBridgeVersionMismatch = "version_mismatch" // the bridge or protocol is for another dsh version
	DshBridgePeersMissing    = "peers_missing"    // a bridge mounted by path cannot resolve its dsh peers
)

// DshBridgeRoute is one place dsh could load the bridge from.
type DshBridgeRoute struct {
	// Kind is "install" (the dsh installation), "profile" or "path_mount".
	Kind string `json:"kind"`
	// Name is the profile name for a profile route.
	Name string `json:"name,omitempty"`
	// Path is the profile directory, or the patch layer holding a path mount.
	Path string `json:"path,omitempty"`
	// BridgePath is where the bridge package resolved.
	BridgePath      string `json:"bridge_path,omitempty"`
	BridgeVersion   string `json:"bridge_version,omitempty"`
	ProtocolVersion string `json:"protocol_version,omitempty"`
	Status          string `json:"status"`
	Detail          string `json:"detail,omitempty"`
}

// Loads reports whether dsh would load the bridge through this route.
func (r DshBridgeRoute) Loads() bool {
	return r.Status == DshBridgeOK || r.Status == DshBridgeUnverified
}

// DshBridgeReport is what Beacon found about the bridge for one Harness home.
type DshBridgeReport struct {
	// DshVersion is the running dsh version, empty when it could not be determined.
	DshVersion string `json:"dsh_version,omitempty"`
	// DshVersionError says why the version is unknown.
	DshVersionError string `json:"dsh_version_error,omitempty"`
	// Install is the dsh installation route, when the installation was found.
	Install *DshBridgeRoute `json:"install,omitempty"`
	// Profiles are the profiles under the Harness home.
	Profiles []DshBridgeRoute `json:"profiles,omitempty"`
	// PathMounts are rows that mount a bridge by absolute path at Beacon's hooks file.
	PathMounts []DshBridgeRoute `json:"path_mounts,omitempty"`
	// Ready is true when at least one route loads a matching bridge.
	Ready bool `json:"ready"`
}

// PathMounted reports whether a path mount already loads a matching bridge at Beacon's hooks file,
// in which case Beacon writes no mount of its own.
func (r DshBridgeReport) PathMounted() bool {
	for _, m := range r.PathMounts {
		if m.Loads() {
			return true
		}
	}
	return false
}

// installLoads reports whether the dsh installation itself provides the bridge, which makes a
// package-name mount load in every profile.
func (r DshBridgeReport) installLoads() bool {
	return r.Install != nil && r.Install.Loads()
}

// ProfilesWithoutBridge names the profiles that would log "did not activate" for a mount by package
// name. Empty when the installation provides the bridge, because then every profile resolves it.
func (r DshBridgeReport) ProfilesWithoutBridge() []string {
	if r.installLoads() {
		return nil
	}
	var names []string
	for _, p := range r.Profiles {
		if !p.Loads() {
			names = append(names, p.Name)
		}
	}
	return names
}

// Summary is one line for status output.
func (r DshBridgeReport) Summary() string {
	version := r.DshVersion
	if version == "" {
		version = "unknown"
	}
	switch {
	case r.PathMounted():
		for _, m := range r.PathMounts {
			if m.Loads() {
				return fmt.Sprintf("hook bridge %s mounted by path in %s (dsh %s)", m.BridgeVersion, m.Path, version)
			}
		}
	case r.installLoads():
		return fmt.Sprintf("hook bridge %s ships with dsh %s", r.Install.BridgeVersion, version)
	case r.Ready:
		var loads []string
		for _, p := range r.Profiles {
			if p.Loads() {
				loads = append(loads, p.Name)
			}
		}
		line := fmt.Sprintf("hook bridge loads in profile(s) %s (dsh %s)", strings.Join(loads, ", "), version)
		if missing := r.ProfilesWithoutBridge(); len(missing) > 0 {
			line += fmt.Sprintf("; profile(s) %s cannot load it and boot without hooks", strings.Join(missing, ", "))
		}
		return line
	}
	return fmt.Sprintf("hook bridge cannot be loaded by dsh %s: %s", version, r.problem())
}

// problem describes why no route loads, naming both versions on a mismatch.
func (r DshBridgeReport) problem() string {
	routes := append([]DshBridgeRoute{}, r.PathMounts...)
	if r.Install != nil {
		routes = append(routes, *r.Install)
	}
	routes = append(routes, r.Profiles...)
	for _, route := range routes {
		if route.Status == DshBridgeVersionMismatch || route.Status == DshBridgeProtocolMissing || route.Status == DshBridgePeersMissing {
			return route.Detail
		}
	}
	if len(r.Profiles) == 0 {
		return fmt.Sprintf("%s is not part of the dsh installation and there are no profiles under the Harness home", dshBridgePackage)
	}
	return fmt.Sprintf("%s is not part of the dsh installation and is not installed in any profile", dshBridgePackage)
}

// InstallCommand is the command a person can run to add the bridge to a profile, or empty when the
// dsh version is unknown and so cannot be pinned.
func (r DshBridgeReport) InstallCommand(profile string) string {
	if r.DshVersion == "" {
		return ""
	}
	if profile == "" {
		profile = r.suggestedProfile()
	}
	return fmt.Sprintf("dsh plugin --profile %s add %s@%s %s@%s", profile, dshBridgePackage, r.DshVersion, dshHookProtocolPackage, r.DshVersion)
}

// suggestedProfile names the profile a fix applies to when one stands out: a profile whose bridge
// is stale or incomplete, or the only profile there is.
func (r DshBridgeReport) suggestedProfile() string {
	for _, p := range r.Profiles {
		if p.Status == DshBridgeVersionMismatch || p.Status == DshBridgeProtocolMissing {
			return p.Name
		}
	}
	if len(r.Profiles) == 1 {
		return r.Profiles[0].Name
	}
	return "<profile>"
}

// DshBridgeUnavailableError is returned by InstallDsh when nothing can load a matching bridge.
type DshBridgeUnavailableError struct {
	Report DshBridgeReport
}

func (e *DshBridgeUnavailableError) Error() string {
	var b strings.Builder
	b.WriteString("DeepSeek Harness hook telemetry was not installed: ")
	b.WriteString(e.Report.problem())
	b.WriteString(". A mount dsh cannot load is skipped at boot with a warning, so the hooks would record nothing. ")
	b.WriteString("Beacon does not install packages. To add the bridge to a profile yourself, run ")
	if cmd := e.Report.InstallCommand(""); cmd != "" {
		b.WriteString("`" + cmd + "`")
	} else {
		b.WriteString("`dsh plugin --profile <profile> add " + dshBridgePackage + "@<dsh version> " + dshHookProtocolPackage + "@<dsh version>`")
	}
	b.WriteString(" (both at exactly the dsh version), then install the hooks again. Until then, `beacon endpoint dsh sync` collects DeepSeek Harness sessions from its session store")
	return b.String()
}

// dshInstallation is what Beacon could learn about the running dsh.
type dshInstallation struct {
	version string
	// modules is the installation's node_modules directory, empty when it was not found.
	modules string
	err     error
}

// probeDshInstallation finds the dsh installation, a seam for tests.
var probeDshInstallation = defaultProbeDshInstallation

// dshLauncherBinJS matches the CLI entry point a launcher script executes.
var dshLauncherBinJS = regexp.MustCompile(`(/[^\s"'$]*/node_modules/@deepseek-ai/dsh)/lib/bin\.js`)

// dshLauncherNext matches another absolute dsh launcher a wrapper script execs.
var dshLauncherNext = regexp.MustCompile(`(/[^\s"'$]*/bin/dsh)\b`)

func defaultProbeDshInstallation() dshInstallation {
	launcher, err := exec.LookPath("dsh")
	if err != nil {
		return dshInstallation{err: fmt.Errorf("dsh is not on PATH")}
	}
	if pkg := dshPackageFromLauncher(launcher, 0); pkg != "" {
		if version := packageVersion(pkg); version != "" {
			return dshInstallation{version: version, modules: filepath.Join(pkg, "node_modules")}
		}
	}
	// The installation could not be found from the launcher. Ask dsh itself; --version prints
	// the version and boots nothing.
	ctx, cancel := context.WithTimeout(context.Background(), dshVersionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, launcher, "--version").Output()
	if err != nil {
		return dshInstallation{err: fmt.Errorf("`dsh --version` failed: %v", err)}
	}
	version := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if version == "" {
		return dshInstallation{err: fmt.Errorf("`dsh --version` printed nothing")}
	}
	return dshInstallation{version: version}
}

// dshPackageFromLauncher follows a dsh launcher to the CLI package directory: a symlink straight to
// lib/bin.js (an npm global install), or a script that runs that file or execs another launcher (a
// wrapper, as Nix and similar package managers write). Empty when it cannot tell.
func dshPackageFromLauncher(path string, depth int) string {
	if depth > 4 {
		return ""
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if filepath.Base(path) == "bin.js" && filepath.Base(filepath.Dir(path)) == "lib" {
		return filepath.Dir(filepath.Dir(path))
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() > 256<<10 {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	text := string(data)
	if m := dshLauncherBinJS.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	for _, m := range dshLauncherNext.FindAllStringSubmatch(text, -1) {
		if next := m[1]; next != path {
			if pkg := dshPackageFromLauncher(next, depth+1); pkg != "" {
				return pkg
			}
		}
	}
	return ""
}

// InspectDshBridge reports, read-only, whether dsh can load a bridge that matches it.
func InspectDshBridge(home, hooksPath string) DshBridgeReport {
	inst := probeDshInstallation()
	report := DshBridgeReport{DshVersion: inst.version}
	if inst.err != nil {
		report.DshVersionError = inst.err.Error()
	}
	if inst.modules != "" {
		route := DshBridgeRoute{Kind: "install", Path: filepath.Dir(inst.modules)}
		if dir := filepath.Join(inst.modules, filepath.FromSlash(dshBridgePackage)); packageVersion(dir) != "" {
			evaluateBridge(&route, dir, report.DshVersion, inst.modules, true)
		} else {
			route.Status = DshBridgeMissing
			route.Detail = fmt.Sprintf("dsh %s does not ship %s", report.DshVersion, dshBridgePackage)
		}
		report.Install = &route
	}

	profilesDir := filepath.Join(home, dshProfilesDirName)
	entries, _ := os.ReadDir(profilesDir)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(profilesDir, entry.Name())
		route := DshBridgeRoute{Kind: "profile", Name: entry.Name(), Path: dir}
		if bridge := resolveNodePackage(dir, dshBridgePackage); bridge != "" {
			evaluateBridge(&route, bridge, report.DshVersion, inst.modules, true)
		} else if report.installLoads() {
			route = *report.Install
			route.Kind, route.Name, route.Path = "profile", entry.Name(), dir
		} else {
			route.Status = DshBridgeMissing
			route.Detail = fmt.Sprintf("%s is not installed in profile %s", dshBridgePackage, entry.Name())
		}
		report.Profiles = append(report.Profiles, route)
	}

	for _, layer := range dshPatchLayers(home) {
		for _, name := range dshPathMountNames(layer, hooksPath) {
			route := DshBridgeRoute{Kind: "path_mount", Path: layer}
			if pkg := packageRootFor(name, dshBridgePackage); pkg != "" {
				evaluateBridge(&route, pkg, report.DshVersion, inst.modules, false)
			} else {
				route.BridgePath = name
				route.Status = DshBridgeMissing
				route.Detail = fmt.Sprintf("%s in %s is not a %s package", name, layer, dshBridgePackage)
			}
			report.PathMounts = append(report.PathMounts, route)
		}
	}

	report.Ready = report.installLoads() || report.PathMounted()
	for _, p := range report.Profiles {
		if p.Loads() {
			report.Ready = true
		}
	}
	return report
}

// evaluateBridge fills a route from the bridge package at dir.
//
// The bridge imports its dsh peers, so each must resolve the way Node resolves it from the bridge's
// real location. A bridge installed into a profile or shipped with dsh also gets the packages the
// dsh installation ships, because dsh intercepts those imports for profile plugins. A bridge mounted
// by absolute path does not (hostPeers false): dsh 0.2.1-rc.2 fails to import such a bridge with
// "Cannot find package '@deepseek-ai/dsh-llm'" unless its peers resolve from where it sits, which
// is what a declarative package provides with node_modules links into the dsh installation.
func evaluateBridge(route *DshBridgeRoute, dir, dshVersion, installModules string, hostPeers bool) {
	route.BridgePath = dir
	route.BridgeVersion = packageVersion(dir)
	real := dir
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		real = r
	}
	resolve := func(name string, fromHost bool) string {
		if found := resolveNodePackage(real, name); found != "" {
			return found
		}
		if fromHost && installModules != "" {
			if candidate := filepath.Join(installModules, filepath.FromSlash(name)); packageVersion(candidate) != "" {
				return candidate
			}
		}
		return ""
	}
	// The protocol is never shipped with dsh, but a release that ships the bridge could ship it too,
	// so the installation fallback applies to every route that imports through dsh.
	protocol := resolve(dshHookProtocolPackage, hostPeers || route.Kind == "install")
	route.ProtocolVersion = packageVersion(protocol)
	var missingPeers []string
	for _, peer := range readPackageManifest(dir).peerNames() {
		if peer == dshHookProtocolPackage {
			continue
		}
		// With the installation unknown, packages dsh ships cannot be checked for a profile route;
		// they are assumed to be there, which is what dsh provides.
		if hostPeers && installModules == "" {
			continue
		}
		if resolve(peer, hostPeers) == "" {
			missingPeers = append(missingPeers, peer)
		}
	}
	switch {
	case protocol == "":
		route.Status = DshBridgeProtocolMissing
		route.Detail = fmt.Sprintf("%s %s is installed at %s but %s is not resolvable from it", dshBridgePackage, route.BridgeVersion, dir, dshHookProtocolPackage)
	case len(missingPeers) > 0:
		route.Status = DshBridgePeersMissing
		route.Detail = fmt.Sprintf("%s %s at %s cannot resolve %s from where it is; a bridge mounted by path needs its dsh peers next to it, for example node_modules links into the dsh installation",
			dshBridgePackage, route.BridgeVersion, dir, strings.Join(missingPeers, ", "))
	case dshVersion == "":
		route.Status = DshBridgeUnverified
		route.Detail = fmt.Sprintf("%s %s resolves, but the running dsh version could not be determined", dshBridgePackage, route.BridgeVersion)
	case route.BridgeVersion != dshVersion || route.ProtocolVersion != dshVersion:
		route.Status = DshBridgeVersionMismatch
		route.Detail = fmt.Sprintf("dsh is %s but %s is %s and %s is %s; both must match dsh exactly",
			dshVersion, dshBridgePackage, route.BridgeVersion, dshHookProtocolPackage, route.ProtocolVersion)
	default:
		route.Status = DshBridgeOK
	}
}

// resolveNodePackage looks name up the way Node resolves a bare specifier imported from dir: in
// node_modules of dir and of each of its ancestors. Empty when it does not resolve.
func resolveNodePackage(dir, name string) string {
	for current := filepath.Clean(dir); ; current = filepath.Dir(current) {
		if filepath.Base(current) != "node_modules" {
			candidate := filepath.Join(current, "node_modules", filepath.FromSlash(name))
			if packageVersion(candidate) != "" {
				return candidate
			}
		}
		if parent := filepath.Dir(current); parent == current {
			return ""
		}
	}
}

// packageRootFor finds the package named name that contains path (a package directory or a file
// inside one), or empty.
func packageRootFor(path, name string) string {
	current := filepath.Clean(path)
	for i := 0; i < 8; i++ {
		if packageName(current) == name {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return ""
}

type packageManifest struct {
	Name             string            `json:"name"`
	Version          string            `json:"version"`
	PeerDependencies map[string]string `json:"peerDependencies"`
}

// peerNames lists the declared peers in a stable order.
func (m packageManifest) peerNames() []string {
	names := make([]string, 0, len(m.PeerDependencies))
	for name := range m.PeerDependencies {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func readPackageManifest(dir string) packageManifest {
	if dir == "" {
		return packageManifest{}
	}
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return packageManifest{}
	}
	var manifest packageManifest
	_ = json.Unmarshal(data, &manifest)
	return manifest
}

func packageVersion(dir string) string { return readPackageManifest(dir).Version }
func packageName(dir string) string    { return readPackageManifest(dir).Name }

// dshPatchLayers lists the patch files dsh composes: the home layer and each profile's own.
func dshPatchLayers(home string) []string {
	layers := []string{filepath.Join(home, dshPatchFileName)}
	profiles, _ := filepath.Glob(filepath.Join(home, dshProfilesDirName, "*", dshPatchFileName))
	sort.Strings(profiles)
	return append(layers, profiles...)
}

// dshPathMountNames returns the absolute `name` of every row in a patch layer that mounts a plugin
// by path with `config.configPath` equal to hooksPath. Rows are read from the top-level list and
// from `insert` lists. A configPath written as an expression cannot be evaluated here and does not
// match.
func dshPathMountNames(layer, hooksPath string) []string {
	data, err := os.ReadFile(layer)
	if err != nil {
		return nil
	}
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.SequenceNode {
		return nil
	}
	want := filepath.Clean(hooksPath)
	var names []string
	consider := func(row *yaml.Node) {
		if row.Kind != yaml.MappingNode {
			return
		}
		name := mappingValue(row, "name")
		config := mappingValue(row, "config")
		if name == nil || name.Kind != yaml.ScalarNode || !filepath.IsAbs(name.Value) || config == nil {
			return
		}
		path := mappingValue(config, "configPath")
		if path == nil || path.Kind != yaml.ScalarNode || (path.Tag != "" && path.Tag != "!!str") {
			return
		}
		if filepath.Clean(path.Value) == want {
			names = append(names, name.Value)
		}
	}
	for _, element := range doc.Content[0].Content {
		consider(element)
		if insert := mappingValue(element, "insert"); insert != nil && insert.Kind == yaml.SequenceNode {
			for _, row := range insert.Content {
				consider(row)
			}
		}
	}
	return names
}

// dshHasPathMount reports whether any patch layer mounts a plugin by path at hooksPath, whatever its
// health. It is the path-mount half of "the hooks file is read".
func dshHasPathMount(hooksPath string) bool {
	for _, layer := range dshPatchLayers(filepath.Dir(hooksPath)) {
		if len(dshPathMountNames(layer, hooksPath)) > 0 {
			return true
		}
	}
	return false
}
