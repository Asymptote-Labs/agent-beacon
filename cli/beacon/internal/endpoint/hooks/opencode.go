package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	opencodeplugin "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks/assets/opencode"
)

const (
	opencodePluginFileName      = "beacon.ts"
	opencodeManagedPluginMarker = "beacon-managed-opencode-plugin:v1"
)

var opencodePluginTemplate = opencodeplugin.Template

type OpenCodeOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type OpenCodeStatus struct {
	Installed  bool   `json:"installed"`
	BinaryPath string `json:"binary_path,omitempty"`
	PluginPath string `json:"plugin_path,omitempty"`
	Message    string `json:"message,omitempty"`
	// IgnoredPluginPaths are Beacon plugin copies OpenCode loads but never runs, because another
	// copy with the same plugin ID loads before them. It includes PluginPath when that is one.
	IgnoredPluginPaths []string `json:"ignored_plugin_paths,omitempty"`
	// DefaultPluginPath is Beacon's user-level plugin in OpenCode's default config directory while
	// OPENCODE_CONFIG_DIR moves the one Beacon manages elsewhere. Sessions started without the
	// variable still use it, so install leaves it in place; uninstall removes it.
	DefaultPluginPath string `json:"default_plugin_path,omitempty"`
}

var opencodeRuntime = hookRuntime{
	displayName: "opencode",
	configPath:  opencodePluginPath,
	install:     installOpenCodePlugin,
	uninstall:   removeOpenCodePlugin,
	isInstalled: isOpenCodeInstalledAt,
}

func InstallOpenCode(opts OpenCodeOptions) (OpenCodeStatus, error) {
	status, err := installRuntimeHooks(opencodeRuntime, RuntimeOptions(opts))
	if err != nil {
		return OpenCodeStatus{}, err
	}
	out := opencodeStatusFromRuntime(status)
	out.DefaultPluginPath = opencodeDefaultDirPlugin(opts.Level)
	return out, nil
}

func UninstallOpenCode(opts OpenCodeOptions) (OpenCodeStatus, error) {
	status, err := uninstallRuntimeHooks(opencodeRuntime, RuntimeOptions(opts))
	if err != nil {
		return OpenCodeStatus{}, err
	}
	out := opencodeStatusFromRuntime(status)
	if path := opencodeDefaultDirPlugin(opts.Level); path != "" {
		if _, err := removeOpenCodePlugin(path); err != nil {
			return OpenCodeStatus{}, err
		}
		out.DefaultPluginPath = path
		out.Message += fmt.Sprintf("; also removed the Beacon plugin at %s", path)
	}
	return out, nil
}

// opencodeDefaultDirPlugin returns Beacon's user-level plugin in the default config directory when
// OPENCODE_CONFIG_DIR moves the one Beacon manages elsewhere, or "" when there is none. An earlier
// Beacon, or an install run without the variable, wrote it. Sessions started without the variable
// load only that copy, OpenCode 1.x sessions started with it load both, and nothing else reports
// it. The two directories are compared by identity, not by name, so a symlink or a case-folding
// filesystem never makes the managed copy look like a second one.
func opencodeDefaultDirPlugin(level Level) string {
	if (level != "" && level != LevelUser) || os.Getenv("OPENCODE_CONFIG_DIR") == "" {
		return ""
	}
	home, _ := os.UserHomeDir()
	dir, err := opencodeDefaultConfigDir(home)
	if err != nil {
		return ""
	}
	path := filepath.Join(dir, "plugins", opencodePluginFileName)
	current, err := opencodePluginPath(level)
	if err != nil || current == path {
		return ""
	}
	if a, err := os.Stat(filepath.Dir(current)); err == nil {
		if b, err := os.Stat(filepath.Dir(path)); err == nil && os.SameFile(a, b) {
			return ""
		}
	}
	if !isOpenCodeInstalledAt(path) {
		return ""
	}
	return path
}

func OpenCodeHookStatus(opts OpenCodeOptions) OpenCodeStatus {
	return opencodeStatusFromRuntime(runtimeHookStatus(opencodeRuntime, RuntimeOptions(opts)))
}

func IsOpenCodeInstalled(opts OpenCodeOptions) bool {
	return isRuntimeInstalled(opencodeRuntime, RuntimeOptions(opts))
}

func opencodeStatusFromRuntime(status runtimeStatus) OpenCodeStatus {
	out := OpenCodeStatus{
		Installed:  status.Installed,
		BinaryPath: status.BinaryPath,
		PluginPath: status.ConfigPath,
		Message:    status.Message,
	}
	if out.Installed && out.BinaryPath != "" {
		if _, err := os.Stat(out.BinaryPath); err != nil {
			out.Installed = false
			out.Message = fmt.Sprintf("opencode plugin is installed, but Beacon hook binary is missing at %s", out.BinaryPath)
			return out
		}
		data, err := os.ReadFile(out.PluginPath)
		// Search for the binary as the renderer wrote it: shell-quoted, then a %q string literal,
		// which doubles a Windows path's backslashes and escapes an apostrophe's quoting.
		quoted := fmt.Sprintf("%q", hookCommandQuote(out.BinaryPath))
		if err != nil || !strings.Contains(string(data), quoted[1:len(quoted)-1]) || strings.Contains(string(data), "__BEACON_") {
			out.Installed = false
			out.Message = fmt.Sprintf("opencode plugin at %s does not reference the active Beacon hook binary", out.PluginPath)
		}
	}
	if out.Installed {
		markIgnoredOpenCodePlugins(&out)
	}
	return out
}

// markIgnoredOpenCodePlugins reports the Beacon plugin copies OpenCode would never run. When the
// copy this status describes is one of them, it is not active even though its file is in place.
func markIgnoredOpenCodePlugins(out *OpenCodeStatus) {
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	home, _ := os.UserHomeDir()
	loaded := opencodeLoadedPlugins(home, dir)
	if len(loaded) < 2 && (len(loaded) == 0 || loaded[0] == out.PluginPath) {
		return
	}
	running := loaded[0]
	out.IgnoredPluginPaths = loaded[1:]
	if running == out.PluginPath {
		out.Message += fmt.Sprintf("; OpenCode never runs the other Beacon plugin copies at %s, because the one at %s loads first with the same plugin ID: remove them",
			strings.Join(out.IgnoredPluginPaths, ", "), running)
		return
	}
	out.Installed = false
	out.Message = fmt.Sprintf("OpenCode never runs the Beacon plugin at %s: the copy at %s loads first with the same plugin ID, and OpenCode runs only that one. Remove one of them.",
		out.PluginPath, running)
}

// opencodeSystemDir is OpenCode's platform system configuration directory
// (packages/core/src/config/discovery.ts systemDirectory). A variable so tests never read the host's.
var opencodeSystemDir = func() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/opencode"
	case "windows":
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "opencode")
	default:
		return "/etc/opencode"
	}
}

// opencodeLoadedPlugins returns the Beacon-managed plugin files OpenCode 2.x loads for a session
// started in dir, in the order it loads them. Every copy defines plugin ID beacon.endpoint and
// OpenCode runs only the first, dropping the rest without logging anything, so only the first
// copy's hook command ever receives telemetry.
//
// The order follows OpenCode's config loading (packages/core/src/config.ts, config/discovery.ts and
// plugin/source-directory.ts): the global config directory, then each .opencode directory from the
// filesystem root down to dir, then the system directory; within one directory, the entries of
// plugin/ and plugins/ sorted by path.
func opencodeLoadedPlugins(home, dir string) []string {
	global, err := OpenCodeConfigDir(home)
	if err != nil {
		global = ""
	}
	system := opencodeSystemDir()
	roots := []string{}
	if global != "" {
		roots = append(roots, global)
	}
	roots = append(roots, opencodeProjectDirs(dir, global, system)...)
	roots = append(roots, system)

	var loaded []string
	for _, root := range roots {
		var entries []string
		for _, name := range []string{"plugin", "plugins"} {
			children, err := os.ReadDir(filepath.Join(root, name))
			if err != nil {
				continue
			}
			for _, child := range children {
				entries = append(entries, filepath.Join(root, name, child.Name()))
			}
		}
		sort.Strings(entries)
		for _, entry := range entries {
			if ext := filepath.Ext(entry); (ext == ".ts" || ext == ".js") && isOpenCodeInstalledAt(entry) {
				loaded = append(loaded, entry)
			}
		}
	}
	return loaded
}

// opencodeProjectDirs lists the .opencode directories OpenCode reads for a session in dir,
// outermost first. Like OpenCode, it reads none when dir is one of the global config roots, and
// skips any that resolves to one.
func opencodeProjectDirs(dir string, globalRoots ...string) []string {
	resolve := func(path string) string {
		if real, err := filepath.EvalSymlinks(path); err == nil {
			return real
		}
		return filepath.Clean(path)
	}
	global := map[string]bool{}
	for _, root := range globalRoots {
		if root != "" {
			global[resolve(root)] = true
		}
	}
	if global[resolve(dir)] {
		return nil
	}
	var dirs []string
	for current := dir; ; {
		candidate := filepath.Join(current, ".opencode")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() && !global[resolve(candidate)] {
			dirs = append([]string{candidate}, dirs...)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return dirs
		}
		current = parent
	}
}

func opencodeEmbeddedPluginSourcePath() string {
	return filepath.Clean(filepath.Join("assets", "opencode", "beacon.ts"))
}

func opencodeRootPluginSourcePath() string {
	return filepath.Clean(filepath.Join("..", "..", "..", "..", "..", "plugins", "opencode-beacon", "src", "beacon.ts"))
}

func installOpenCodePlugin(path, binaryPath, logPath, configPath string) error {
	if existing, err := os.ReadFile(path); err == nil {
		if !strings.Contains(string(existing), opencodeManagedPluginMarker) {
			return fmt.Errorf("refusing to overwrite unmanaged opencode plugin at %s", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	plugin, err := renderOpenCodePlugin(binaryPath, logPath, configPath)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(plugin), 0644)
}

func removeOpenCodePlugin(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !strings.Contains(string(data), opencodeManagedPluginMarker) {
		return false, nil
	}
	return true, os.Remove(path)
}

func isOpenCodeInstalledAt(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	text := string(data)
	return strings.Contains(text, opencodeManagedPluginMarker)
}

func opencodePluginPath(level Level) (string, error) {
	dir, err := opencodePluginDir(level)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, opencodePluginFileName), nil
}

// OpenCodeConfigDir returns OpenCode's global config directory for the given home directory.
//
// A non-empty OPENCODE_CONFIG_DIR wins. OpenCode 2.x uses it instead of the default directory
// (packages/cli/src/index.ts and server-process.ts: `process.env.OPENCODE_CONFIG_DIR ? { config:
// process.env.OPENCODE_CONFIG_DIR } : {}`), so a plugin left in the default directory never loads.
// OpenCode 1.x reads it as one more config directory after the default (packages/opencode/src/
// config/paths.ts) and loads its plugins and opencode.json as well, so the same directory works
// for both.
//
// Otherwise OpenCode roots it at `$XDG_CONFIG_HOME/opencode` and falls back to `~/.config/opencode`
// (packages/util/src/global-roots.ts: `process.env.XDG_CONFIG_HOME || path.join(home, ".config")`).
// For both variables an empty value falls back and nothing is trimmed; a relative value is resolved
// against the working directory, so it is made absolute here. The home directory is only needed
// for the default, so either override resolves without one.
//
// Exported because discovery and the inventory scan report on the same plugin the installer
// writes and must not rebuild the path themselves.
func OpenCodeConfigDir(home string) (string, error) {
	if dir := os.Getenv("OPENCODE_CONFIG_DIR"); dir != "" {
		return filepath.Abs(dir)
	}
	return opencodeDefaultConfigDir(home)
}

// opencodeDefaultConfigDir is OpenCode's global config directory when OPENCODE_CONFIG_DIR is unset.
func opencodeDefaultConfigDir(home string) (string, error) {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		abs, err := filepath.Abs(base)
		if err != nil {
			return "", err
		}
		return filepath.Join(abs, "opencode"), nil
	}
	if home == "" {
		return "", fmt.Errorf("home directory is required to resolve opencode's config directory")
	}
	return filepath.Join(home, ".config", "opencode"), nil
}

func opencodePluginDir(level Level) (string, error) {
	switch level {
	case "", LevelUser:
		// OpenCodeConfigDir reports a missing home directory only when no override needs one.
		home, _ := os.UserHomeDir()
		dir, err := OpenCodeConfigDir(home)
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "plugins"), nil
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".opencode", "plugins"), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}

func renderOpenCodePlugin(binaryPath, logPath, configPath string) (string, error) {
	return renderOpenCodePluginTemplate(opencodePluginTemplate, binaryPath, logPath, configPath)
}

func renderOpenCodePluginTemplate(template, binaryPath, logPath, configPath string) (string, error) {
	commandPrefix := endpointCommandPrefix("opencode", binaryPath, logPath, configPath)
	source := strings.ReplaceAll(template, "__BEACON_MANAGED_MARKER__", opencodeManagedPluginMarker)
	source = strings.ReplaceAll(source, `"__BEACON_COMMAND__"`, fmt.Sprintf("%q", commandPrefix+" opencode-event"))
	if strings.Contains(source, "__BEACON_") {
		return "", fmt.Errorf("opencode plugin template contains unresolved Beacon placeholders")
	}
	return source, nil
}
