package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	openclawplugin "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks/assets/openclaw"
)

// OpenClaw Gateway is integrated as a Beacon-managed plugin, the Cline and Pi-family shape, with
// one structural difference: OpenClaw discovers a plugin as a *directory* rather than as a file.
//
// A plugin root is a directory holding `package.json`, `openclaw.plugin.json`, and the entrypoint
// the first of those declares. OpenClaw scans the immediate children of its extensions directories
// and reads each child's manifests, so Beacon owns the directory `beacon-endpoint` rather than a
// filename inside a directory somebody else also writes to -- the goose arrangement, for the same
// reason.
//
// The gateway also collects over OTLP, through OpenClaw's own `diagnostics-otel` plugin and
// `beacon endpoint integrations openclaw`. The two paths are complementary rather than
// alternatives: OTLP carries gateway-level traces and metrics, and by OpenClaw's default carries
// no prompt, tool, or file content at all, while this path carries the agent's actual work.
const (
	// openClawPlatform is the `--platform` value written into the hook invocation.
	openClawPlatform = "openclaw"

	// openClawPluginID is the directory Beacon owns and the id both manifests declare.
	//
	// It is also the id an operator addresses the plugin by -- `openclaw plugins enable
	// beacon-endpoint`, `plugins.entries.beacon-endpoint` in the config -- so it is a contract with
	// the operator's config file, not just a directory name. A test asserts the manifests agree
	// with this constant.
	openClawPluginID = "beacon-endpoint"

	// openClawEntryFileName is the plugin entrypoint, declared by package.json.
	openClawEntryFileName = "beacon.js"

	// openClawPackageFileName and openClawManifestFileName are the two manifests OpenClaw reads
	// before it loads any plugin code. A directory missing either is not a plugin, and OpenClaw
	// skips it during discovery without reporting anything.
	openClawPackageFileName  = "package.json"
	openClawManifestFileName = "openclaw.plugin.json"

	// OpenClawManagedPluginMarker identifies a plugin entry as one Beacon wrote.
	//
	// The version suffix is part of the contract with the plugin source. Bump it when the plugin's
	// behavior changes in a way that makes an older installed copy wrong, which is what lets a
	// repair recognize a stale file rather than leave it in place. Exported for the same reason
	// the Pi-family markers are: the harness probe and the inventory scanner both read it.
	OpenClawManagedPluginMarker = "beacon-managed-openclaw-plugin:v1"

	// openClawStateDirName is OpenClaw's state directory under the home directory.
	openClawStateDirName = ".openclaw"

	// openClawStateDirEnv relocates that state directory outright. OpenClaw reads it itself, so an
	// install performed from inside a shell that sets it lands where that gateway actually looks.
	openClawStateDirEnv = "OPENCLAW_STATE_DIR"

	// openClawConfigPathEnv names the config file directly, overriding the state directory for
	// that one file. Only read for the advisory config inspection below -- Beacon never writes it.
	openClawConfigPathEnv = "OPENCLAW_CONFIG_PATH"

	// openClawConfigFileName is the config OpenClaw writes by default. Read-only here; see
	// OpenClawConfigAdvice.
	openClawConfigFileName = "openclaw.json"
)

type OpenClawOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type OpenClawStatus struct {
	Installed  bool   `json:"installed"`
	BinaryPath string `json:"binary_path,omitempty"`
	PluginPath string `json:"plugin_path,omitempty"`
	Message    string `json:"message,omitempty"`
	// ConfigAdvice is what the operator still has to do in OpenClaw's own config, if anything. It
	// is advice rather than an action because Beacon does not write that file; see
	// OpenClawConfigAdvice.
	ConfigAdvice []string `json:"config_advice,omitempty"`
}

var openClawRuntime = hookRuntime{
	displayName: "OpenClaw Gateway",
	configPath:  OpenClawEntryPath,
	install:     installOpenClawPlugin,
	uninstall:   removeOpenClawPlugin,
	isInstalled: openClawInstalledAt,
}

func InstallOpenClaw(opts OpenClawOptions) (OpenClawStatus, error) {
	status, err := installRuntimeHooks(openClawRuntime, RuntimeOptions(opts))
	if err != nil {
		return OpenClawStatus{}, err
	}
	return openClawStatusFromRuntime(status), nil
}

func UninstallOpenClaw(opts OpenClawOptions) (OpenClawStatus, error) {
	status, err := uninstallRuntimeHooks(openClawRuntime, RuntimeOptions(opts))
	if err != nil {
		return OpenClawStatus{}, err
	}
	// Advice describes what is still missing for collection to work, which is meaningless once the
	// plugin is gone -- and printing "add this to your config" after an uninstall would read as an
	// instruction to re-enable what was just removed.
	out := openClawStatusFromRuntime(status)
	out.ConfigAdvice = nil
	return out, nil
}

func OpenClawHookStatus(opts OpenClawOptions) OpenClawStatus {
	return openClawStatusFromRuntime(runtimeHookStatus(openClawRuntime, RuntimeOptions(opts)))
}

func IsOpenClawInstalled(opts OpenClawOptions) bool {
	return isRuntimeInstalled(openClawRuntime, RuntimeOptions(opts))
}

// openClawStatusFromRuntime reports installed only when the plugin can actually reach the hook
// binary, and attaches whatever the operator still owes OpenClaw's config.
func openClawStatusFromRuntime(status runtimeStatus) OpenClawStatus {
	status = openClawReachableStatus(status)
	out := OpenClawStatus{
		Installed:  status.Installed,
		BinaryPath: status.BinaryPath,
		PluginPath: filepath.Dir(status.ConfigPath),
		Message:    status.Message,
	}
	if status.Installed {
		out.ConfigAdvice = OpenClawConfigAdvice()
	}
	return out
}

// openClawReachableStatus downgrades an "installed" verdict the directory on disk does not support.
//
// The marker on the entry is not enough. A plugin directory survives a Beacon uninstall, a
// partially applied update, or a home directory restored onto a machine where the binary lives
// elsewhere; in each case OpenClaw loads a plugin that spawns nothing, and reporting that as
// installed tells an operator telemetry is being collected when none is. The manifests are checked
// too, because OpenClaw skips a directory missing either one during discovery and says nothing
// about it -- a plugin that never loads looks exactly like a plugin that loaded and saw nothing.
func openClawReachableStatus(status runtimeStatus) runtimeStatus {
	if !status.Installed {
		return status
	}
	dir := filepath.Dir(status.ConfigPath)
	if status.BinaryPath == "" {
		return status
	}
	if _, err := os.Stat(status.BinaryPath); err != nil {
		status.Installed = false
		status.Message = fmt.Sprintf("OpenClaw plugin is installed, but Beacon hook binary is missing at %s",
			status.BinaryPath)
		return status
	}
	for _, name := range []string{openClawPackageFileName, openClawManifestFileName} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			status.Installed = false
			status.Message = fmt.Sprintf("OpenClaw plugin at %s is missing %s, so OpenClaw will not load it",
				dir, name)
			return status
		}
	}
	data, err := os.ReadFile(status.ConfigPath)
	if err != nil || !extensionReferencesBinary(string(data), status.BinaryPath) ||
		strings.Contains(string(data), "__BEACON_") {
		status.Installed = false
		status.Message = fmt.Sprintf("OpenClaw plugin at %s does not reference the active Beacon hook binary",
			status.ConfigPath)
	}
	return status
}

// installOpenClawPlugin writes the three files that make a directory an OpenClaw plugin.
//
// path is the entrypoint; the manifests go beside it. The refusal is keyed on the entry rather
// than on the directory so a directory Beacon created and an operator later edited by hand is
// still recognised as Beacon's, while a `beacon-endpoint` plugin somebody else installed from
// ClawHub is not overwritten.
func installOpenClawPlugin(path, binaryPath, logPath, configPath string) error {
	if existing, err := os.ReadFile(path); err == nil {
		if !strings.Contains(string(existing), OpenClawManagedPluginMarker) {
			return fmt.Errorf("refusing to overwrite unmanaged OpenClaw plugin at %s", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	entry, err := renderOpenClawEntry(binaryPath, logPath, configPath)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	// The manifests first, then the entry. OpenClaw's scan reads the manifests before it loads the
	// entry, so writing them last would leave a window in which a scan finds a directory it treats
	// as a broken plugin. Writing the entry last also makes the marker check above the last thing
	// to become true, so a half-written install is never reported as installed.
	for _, file := range []struct {
		name    string
		content string
	}{
		{openClawPackageFileName, openclawplugin.PackageManifest},
		{openClawManifestFileName, openclawplugin.PluginManifest},
	} {
		if err := os.WriteFile(filepath.Join(dir, file.name), []byte(file.content), 0644); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(entry), 0644)
}

// renderOpenClawEntry substitutes the hook invocation and the marker into the plugin source.
//
// Deliberately the same substitution managedExtension.render performs, spelled out here rather
// than shared, because that type models a runtime whose whole install is one file and this one is
// not. The argv goes in as a JSON array rather than a command line because the plugin spawns the
// binary directly, which removes the shell -- and with it the per-shell quoting problem
// endpointCommandPrefix documents -- from a gateway that runs on Windows as readily as on macOS.
func renderOpenClawEntry(binaryPath, logPath, configPath string) (string, error) {
	args := append(endpointCommandArgs(openClawPlatform, binaryPath, logPath, configPath), openClawPlatform+"-event")
	argv, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	source := strings.ReplaceAll(openclawplugin.Template, managedMarkerPlaceholder, OpenClawManagedPluginMarker)
	if !strings.Contains(source, argvPlaceholder) {
		return "", fmt.Errorf("OpenClaw plugin template is missing the %s placeholder", argvPlaceholder)
	}
	source = strings.ReplaceAll(source, argvPlaceholder, string(argv))
	if strings.Contains(source, "__BEACON_") {
		return "", fmt.Errorf("OpenClaw plugin template contains unresolved Beacon placeholders")
	}
	return source, nil
}

// removeOpenClawPlugin deletes the plugin directory, and only one Beacon wrote.
//
// The whole directory rather than the entry alone: a directory holding two manifests and no
// entrypoint is a plugin OpenClaw tries to load and cannot, which it reports as a broken plugin
// rather than as an absent one. Removal is file by file and then the directory, so an operator's
// own file dropped in there stops the rmdir rather than being deleted with it.
func removeOpenClawPlugin(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !strings.Contains(string(data), OpenClawManagedPluginMarker) {
		return false, nil
	}
	dir := filepath.Dir(path)
	for _, name := range []string{openClawEntryFileName, openClawPackageFileName, openClawManifestFileName} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return false, err
		}
	}
	// Fails harmlessly when the operator left something else in there, which is the intended
	// outcome: the plugin is gone either way, and their file is not.
	_ = os.Remove(dir)
	return true, nil
}

func openClawInstalledAt(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), OpenClawManagedPluginMarker)
}

// OpenClawEntryPath returns the plugin entrypoint Beacon manages for a given install level.
func OpenClawEntryPath(level Level) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		// Pass an empty home and let downstream resolution decide: project scope never
		// needs one, and user scope can resolve from OPENCLAW_STATE_DIR alone.
		return OpenClawEntryPathForHome("", level)
	}
	return OpenClawEntryPathForHome(home, level)
}

// OpenClawEntryPathForHome is OpenClawEntryPath against a caller-supplied home directory.
//
// Exported for inventory, which scans a home directory it was handed rather than the process's own
// -- a scan of another user's tree, or of a fixture. Sharing the resolution rather than rebuilding
// the path there is what keeps inventory from reporting on a file the installer does not write,
// since OPENCLAW_STATE_DIR moves it.
func OpenClawEntryPathForHome(home string, level Level) (string, error) {
	dir, err := openClawPluginDir(home, level)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, openClawEntryFileName), nil
}

// openClawPluginDir resolves the plugin root OpenClaw will discover.
//
// Two scopes, resolved asymmetrically because OpenClaw resolves them that way. Its global root is
// the extensions directory under the state directory -- `~/.openclaw/extensions` -- which
// OPENCLAW_STATE_DIR replaces outright. Its workspace root is `<workspace>/.openclaw/extensions`,
// with no environment override: the runtime joins the literal onto the working directory.
//
// Which one wins when both exist is OpenClaw's business and it is not symmetric either: a
// workspace copy does not shadow a bundled plugin, and `plugins.load.paths` outranks both. That
// does not affect Beacon, whose plugin id collides with nothing shipped, but it is why a project
// install is not simply "the same thing, closer".
func openClawPluginDir(home string, level Level) (string, error) {
	switch level {
	case "", LevelUser:
		stateDir, err := OpenClawStateDir(home)
		if err != nil {
			return "", err
		}
		return filepath.Join(stateDir, "extensions", openClawPluginID), nil
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, openClawStateDirName, "extensions", openClawPluginID), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}

// OpenClawStateDir returns the directory OpenClaw keeps its own state in for a caller-supplied
// home directory.
//
// Exported because discovery needs it as evidence in its own right, not just as a prefix of the
// plugin path. A gateway is commonly daemonized under an account whose PATH this process did not
// inherit, so an absent `openclaw` executable is not absence of OpenClaw -- and the directory
// OpenClaw creates on first run is what says otherwise. That directory is this one; the
// `extensions` directory beneath it does not exist until something installs a plugin, which on a
// fresh gateway may be never.
//
// Deriving this by walking up from the plugin path is what made that distinction easy to get
// wrong: the plugin path has a directory segment of its own, so "two levels up" lands on
// `extensions` here where it lands on the state root for the runtimes this was modelled on.
func OpenClawStateDir(home string) (string, error) {
	if stateDir := strings.TrimSpace(os.Getenv(openClawStateDirEnv)); stateDir != "" {
		return stateDir, nil
	}
	if home == "" {
		return "", fmt.Errorf("home directory is required to resolve the OpenClaw state directory")
	}
	return filepath.Join(home, openClawStateDirName), nil
}

// OpenClawConfigAdvice reports what the operator still owes OpenClaw's own config, if anything.
//
// Beacon does not write that file, and the reason is worth stating because every other runtime
// Beacon supports gets its settings merged. OpenClaw's config is "JSON or JSON5" by its own
// documentation, under either of two filenames, and JSON5 permits comments, trailing commas and
// unquoted keys -- none of which survive a round trip through a Go JSON encoder. Rewriting an
// operator's gateway config would silently strip their comments and reformat a file that
// configures every chat channel they run. A plugin directory Beacon owns outright is a footprint
// worth taking; their gateway config is not.
//
// So this reads the config and says what is missing, and `beacon endpoint hooks install` prints
// it. Two things can be missing:
//
//   - `plugins.entries.beacon-endpoint.hooks.allowConversationAccess`. OpenClaw requires every
//     non-bundled plugin to opt in explicitly before it will register a conversation hook, and
//     `llm_output` is one. Without it the plugin still collects sessions, prompts, tools,
//     commands, files and MCP activity, and collects no token usage and no agent messages. It is
//     advisory rather than fatal for that reason.
//   - `plugins.allow`. An operator using an allowlist has to name this plugin in it. Beacon only
//     mentions it when the key is already present: an allowlist introduced where none existed
//     would deny every other plugin the operator runs, which is a far worse outcome than the one
//     it would fix.
//
// A config that cannot be read or parsed produces no advice rather than a guess. JSON5 is the
// common case there, and telling an operator their config is missing a key that is in it -- three
// lines below a comment -- would be worse than saying nothing.
func OpenClawConfigAdvice() []string {
	data, err := os.ReadFile(openClawConfigPath())
	if err != nil {
		return nil
	}
	var config struct {
		Plugins struct {
			Allow   []string `json:"allow"`
			Entries map[string]struct {
				Enabled *bool `json:"enabled"`
				Hooks   struct {
					AllowConversationAccess *bool `json:"allowConversationAccess"`
				} `json:"hooks"`
			} `json:"entries"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil
	}

	var advice []string
	entry, hasEntry := config.Plugins.Entries[openClawPluginID]
	if !hasEntry || entry.Hooks.AllowConversationAccess == nil || !*entry.Hooks.AllowConversationAccess {
		advice = append(advice, fmt.Sprintf(
			"Add plugins.entries.%s.hooks.allowConversationAccess = true to %s to collect token usage and agent messages; everything else is collected without it.",
			openClawPluginID, openClawConfigPath()))
	}
	if len(config.Plugins.Allow) > 0 {
		allowed := false
		for _, id := range config.Plugins.Allow {
			if id == openClawPluginID {
				allowed = true
				break
			}
		}
		if !allowed {
			advice = append(advice, fmt.Sprintf(
				"Add %q to plugins.allow in %s; your config uses an allowlist, so the plugin will not load until it is named there.",
				openClawPluginID, openClawConfigPath()))
		}
	}
	return advice
}

// openClawConfigPath resolves the config file OpenClaw reads. Read-only; see OpenClawConfigAdvice.
func openClawConfigPath() string {
	if override := strings.TrimSpace(os.Getenv(openClawConfigPathEnv)); override != "" {
		return override
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// Only matters when OPENCLAW_STATE_DIR is unset; OpenClawStateDir reports that itself.
		home = ""
	}
	stateDir, err := OpenClawStateDir(home)
	if err != nil {
		return ""
	}
	return filepath.Join(stateDir, openClawConfigFileName)
}

func openClawEmbeddedPluginSourcePath() string {
	return filepath.Clean(filepath.Join("assets", "openclaw", openClawEntryFileName))
}

func openClawRootPluginSourcePath() string {
	return filepath.Clean(filepath.Join("..", "..", "..", "..", "..", "plugins", "openclaw-beacon", "src", openClawEntryFileName))
}
