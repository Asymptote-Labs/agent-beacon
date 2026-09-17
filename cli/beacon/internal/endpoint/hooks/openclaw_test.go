package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openClawInstall runs a user-level install against a temporary home and returns the plugin
// directory it wrote.
func openClawInstall(t *testing.T) (OpenClawStatus, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(openClawStateDirEnv, "")
	t.Setenv(openClawConfigPathEnv, "")

	status, err := InstallOpenClaw(OpenClawOptions{
		Level:    LevelUser,
		LogPath:  filepath.Join(home, "runtime.jsonl"),
		UserMode: true,
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	return status, filepath.Join(home, ".openclaw", "extensions", "beacon-endpoint")
}

// OpenClaw discovers a plugin as a directory and skips one missing either manifest, without
// reporting anything. An install that wrote only the entry would look like a working install and
// collect nothing.
func TestOpenClawInstallWritesTheWholePluginDirectory(t *testing.T) {
	status, dir := openClawInstall(t)

	if !status.Installed {
		t.Fatalf("install reported not installed: %s", status.Message)
	}
	if status.PluginPath != dir {
		t.Fatalf("plugin path = %q, want %q", status.PluginPath, dir)
	}
	for _, name := range []string{"beacon.js", "package.json", "openclaw.plugin.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("install did not write %s: %v", name, err)
		}
	}
}

func TestOpenClawInstallRendersTheHookInvocation(t *testing.T) {
	status, dir := openClawInstall(t)

	entry, err := os.ReadFile(filepath.Join(dir, "beacon.js"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(entry)
	if strings.Contains(source, "__BEACON_") {
		t.Fatal("installed plugin still carries an unresolved Beacon placeholder")
	}
	if !strings.Contains(source, OpenClawManagedPluginMarker) {
		t.Fatal("installed plugin carries no Beacon marker")
	}
	// The argv is JSON, so the binary path appears JSON-escaped. Asserted through the same helper
	// status uses, which is what makes a Windows path with backslashes compare correctly.
	if !extensionReferencesBinary(source, status.BinaryPath) {
		t.Fatalf("installed plugin does not spawn %s", status.BinaryPath)
	}
	for _, want := range []string{`"--platform"`, `"openclaw"`, `"openclaw-event"`} {
		if !strings.Contains(source, want) {
			t.Fatalf("installed plugin argv is missing %s:\n%s", want, firstLines(source, 40))
		}
	}
}

// The manifests are written verbatim, so an install is byte-identical to the reviewed source.
func TestOpenClawInstallWritesTheReviewedManifests(t *testing.T) {
	_, dir := openClawInstall(t)

	var pkg struct {
		Type     string `json:"type"`
		OpenClaw struct {
			Extensions []string `json:"extensions"`
		} `json:"openclaw"`
	}
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		t.Fatalf("installed package.json does not parse: %v", err)
	}
	if pkg.Type != "module" || len(pkg.OpenClaw.Extensions) != 1 || pkg.OpenClaw.Extensions[0] != "./beacon.js" {
		t.Fatalf("installed package.json does not declare the entry: %+v", pkg)
	}

	var manifest struct {
		ID string `json:"id"`
	}
	data, err = os.ReadFile(filepath.Join(dir, "openclaw.plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("installed openclaw.plugin.json does not parse: %v", err)
	}
	// The id an operator addresses the plugin by, in `openclaw plugins enable` and in the config
	// key the advice below names. It is a contract with their config file, not a directory name.
	if manifest.ID != openClawPluginID {
		t.Fatalf("installed manifest id = %q, want %q", manifest.ID, openClawPluginID)
	}
}

func TestOpenClawInstallIsIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(openClawStateDirEnv, "")

	opts := OpenClawOptions{Level: LevelUser, LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}
	if _, err := InstallOpenClaw(opts); err != nil {
		t.Fatalf("first install: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(home, ".openclaw", "extensions", "beacon-endpoint", "beacon.js"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InstallOpenClaw(opts); err != nil {
		t.Fatalf("second install: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(home, ".openclaw", "extensions", "beacon-endpoint", "beacon.js"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("a repeated install rewrote the plugin differently")
	}
}

// A `beacon-endpoint` plugin somebody else installed is not Beacon's to replace.
func TestOpenClawInstallRefusesAnUnmanagedPlugin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(openClawStateDirEnv, "")

	dir := filepath.Join(home, ".openclaw", "extensions", "beacon-endpoint")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	theirs := "export default { id: 'beacon-endpoint', register() {} }\n"
	if err := os.WriteFile(filepath.Join(dir, "beacon.js"), []byte(theirs), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := InstallOpenClaw(OpenClawOptions{Level: LevelUser, LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true})
	if err == nil {
		t.Fatal("install overwrote a plugin Beacon did not write")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "beacon.js"))
	if string(data) != theirs {
		t.Fatal("the unmanaged plugin was modified")
	}
}

// Uninstall removes the whole directory. A directory holding manifests and no entry is a plugin
// OpenClaw tries to load and cannot, which it reports as broken rather than as absent.
func TestOpenClawUninstallRemovesTheDirectory(t *testing.T) {
	_, dir := openClawInstall(t)

	status, err := UninstallOpenClaw(OpenClawOptions{Level: LevelUser, UserMode: true})
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if status.Installed {
		t.Fatalf("still installed after uninstall: %s", status.Message)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("plugin directory survived uninstall: %v", err)
	}
}

// An operator's own file in the plugin directory stops the rmdir rather than being deleted with
// Beacon's files.
func TestOpenClawUninstallKeepsAnOperatorFile(t *testing.T) {
	_, dir := openClawInstall(t)
	theirs := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(theirs, []byte("mine\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := UninstallOpenClaw(OpenClawOptions{Level: LevelUser, UserMode: true}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(theirs); err != nil {
		t.Fatalf("uninstall deleted a file Beacon did not write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "beacon.js")); !os.IsNotExist(err) {
		t.Fatal("uninstall left the plugin entry behind")
	}
}

func TestOpenClawUninstallLeavesAnUnmanagedPlugin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(openClawStateDirEnv, "")

	dir := filepath.Join(home, ".openclaw", "extensions", "beacon-endpoint")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	theirs := "export default { id: 'beacon-endpoint', register() {} }\n"
	if err := os.WriteFile(filepath.Join(dir, "beacon.js"), []byte(theirs), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := UninstallOpenClaw(OpenClawOptions{Level: LevelUser, UserMode: true}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "beacon.js"))
	if err != nil || string(data) != theirs {
		t.Fatal("uninstall removed a plugin Beacon did not write")
	}
}

// A directory OpenClaw will skip must not be reported as installed. This is the failure the probe
// exists for: OpenClaw says nothing about a plugin it skipped, so "never loaded" and "loaded and
// saw nothing" are indistinguishable from the log alone.
func TestOpenClawStatusRejectsAMissingManifest(t *testing.T) {
	_, dir := openClawInstall(t)
	if err := os.Remove(filepath.Join(dir, "openclaw.plugin.json")); err != nil {
		t.Fatal(err)
	}

	status := OpenClawHookStatus(OpenClawOptions{Level: LevelUser, UserMode: true})
	if status.Installed {
		t.Fatal("status reported installed with a manifest missing")
	}
	if !strings.Contains(status.Message, "openclaw.plugin.json") {
		t.Fatalf("status does not name the missing manifest: %s", status.Message)
	}
}

func TestOpenClawStatusRejectsAMissingBinary(t *testing.T) {
	status, _ := openClawInstall(t)
	if err := os.Remove(status.BinaryPath); err != nil {
		t.Fatal(err)
	}

	got := OpenClawHookStatus(OpenClawOptions{Level: LevelUser, UserMode: true})
	if got.Installed {
		t.Fatal("status reported installed with the hook binary gone")
	}
}

// OPENCLAW_STATE_DIR relocates the whole state directory, and OpenClaw reads it itself. An install
// that ignored it would write where that gateway does not look and report success.
func TestOpenClawStateDirOverrideMovesThePlugin(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(t.TempDir(), "gateway-state")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(openClawStateDirEnv, state)

	if _, err := InstallOpenClaw(OpenClawOptions{Level: LevelUser, LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "extensions", "beacon-endpoint", "beacon.js")); err != nil {
		t.Fatalf("install ignored %s: %v", openClawStateDirEnv, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".openclaw")); !os.IsNotExist(err) {
		t.Fatal("install also wrote to the default location")
	}
}

func TestOpenClawProjectLevelPathIsTheWorkspaceRoot(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	t.Setenv(openClawStateDirEnv, "")

	path, err := OpenClawEntryPath(LevelProject)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(wd, ".openclaw", "extensions", "beacon-endpoint", "beacon.js")
	if path != want {
		t.Fatalf("project entry path = %q, want %q", path, want)
	}
}

// OPENCLAW_STATE_DIR is a global-scope override. OpenClaw joins the workspace literal for the
// project scope with no override at all, so applying it to both would write the project plugin
// where nothing reads it.
func TestOpenClawStateDirDoesNotMoveTheProjectPlugin(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	t.Setenv(openClawStateDirEnv, filepath.Join(t.TempDir(), "elsewhere"))

	path, err := OpenClawEntryPath(LevelProject)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, wd) {
		t.Fatalf("project entry path = %q, want one under %q", path, wd)
	}
}

// The checked-in plugin and its embedded copy are two files with the same content, and nothing but
// a test keeps them that way.
func TestOpenClawEmbeddedPluginMatchesRootSource(t *testing.T) {
	embedded, err := os.ReadFile(openClawEmbeddedPluginSourcePath())
	if err != nil {
		t.Fatalf("embedded plugin source is unreadable: %v", err)
	}
	root, err := os.ReadFile(openClawRootPluginSourcePath())
	if err != nil {
		t.Fatalf("root plugin source is unreadable: %v", err)
	}
	if string(embedded) != string(root) {
		t.Fatal("plugins/openclaw-beacon/src/beacon.js and its embedded copy have drifted; " +
			"run `bun run sync` in plugins/openclaw-beacon")
	}
}

// Conversation hooks are withheld from a non-bundled plugin until the operator grants access, so
// an otherwise perfect install collects no tokens. Advice is how that gap becomes visible.
func TestOpenClawConfigAdviceAsksForConversationAccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(openClawStateDirEnv, "")
	t.Setenv(openClawConfigPathEnv, "")
	writeOpenClawConfig(t, home, `{"plugins":{"entries":{}}}`)

	advice := OpenClawConfigAdvice()
	if len(advice) != 1 || !strings.Contains(advice[0], "allowConversationAccess") {
		t.Fatalf("advice = %v, want one line about allowConversationAccess", advice)
	}
}

func TestOpenClawConfigAdviceIsSilentWhenAccessIsGranted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(openClawStateDirEnv, "")
	t.Setenv(openClawConfigPathEnv, "")
	writeOpenClawConfig(t, home,
		`{"plugins":{"entries":{"beacon-endpoint":{"enabled":true,"hooks":{"allowConversationAccess":true}}}}}`)

	if advice := OpenClawConfigAdvice(); len(advice) != 0 {
		t.Fatalf("advice = %v, want none", advice)
	}
}

// An allowlist that does not name the plugin stops it loading entirely, which is worth saying.
func TestOpenClawConfigAdviceNamesAnExcludingAllowlist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(openClawStateDirEnv, "")
	t.Setenv(openClawConfigPathEnv, "")
	writeOpenClawConfig(t, home,
		`{"plugins":{"allow":["codex"],"entries":{"beacon-endpoint":{"hooks":{"allowConversationAccess":true}}}}}`)

	advice := OpenClawConfigAdvice()
	if len(advice) != 1 || !strings.Contains(advice[0], "plugins.allow") {
		t.Fatalf("advice = %v, want one line about plugins.allow", advice)
	}
}

// Beacon never introduces an allowlist. A config with no `allow` key permits every plugin; adding
// one to fix Beacon's load would deny every other plugin the operator runs.
func TestOpenClawConfigAdviceDoesNotMentionAnAbsentAllowlist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(openClawStateDirEnv, "")
	t.Setenv(openClawConfigPathEnv, "")
	writeOpenClawConfig(t, home,
		`{"plugins":{"entries":{"beacon-endpoint":{"hooks":{"allowConversationAccess":true}}}}}`)

	for _, line := range OpenClawConfigAdvice() {
		if strings.Contains(line, "plugins.allow") {
			t.Fatalf("advice suggests creating an allowlist: %q", line)
		}
	}
}

// A JSON5 config -- comments, trailing commas -- is one OpenClaw accepts and Go's decoder does
// not. Guessing at what it contains would mean telling an operator to add a key that is already
// three lines below a comment.
func TestOpenClawConfigAdviceIsSilentOnAnUnparseableConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(openClawStateDirEnv, "")
	t.Setenv(openClawConfigPathEnv, "")
	writeOpenClawConfig(t, home, "{\n  // the gateway\n  plugins: { entries: {} },\n}\n")

	if advice := OpenClawConfigAdvice(); len(advice) != 0 {
		t.Fatalf("advice = %v, want none for a config Beacon could not read", advice)
	}
}

func TestOpenClawConfigAdviceIsSilentWithNoConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(openClawStateDirEnv, "")
	t.Setenv(openClawConfigPathEnv, "")

	if advice := OpenClawConfigAdvice(); len(advice) != 0 {
		t.Fatalf("advice = %v, want none before OpenClaw has written a config", advice)
	}
}

// Beacon reads OpenClaw's config and never writes it: it is "JSON or JSON5" by OpenClaw's own
// documentation, and a Go round trip would strip an operator's comments out of the file that
// configures every chat channel they run.
func TestOpenClawInstallDoesNotTouchTheGatewayConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(openClawStateDirEnv, "")
	t.Setenv(openClawConfigPathEnv, "")
	original := "{\n  // hand written\n  \"plugins\": { \"entries\": {} }\n}\n"
	writeOpenClawConfig(t, home, original)

	if _, err := InstallOpenClaw(OpenClawOptions{Level: LevelUser, LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}); err != nil {
		t.Fatalf("install: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".openclaw", openClawConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("install rewrote the gateway config:\n%s", data)
	}
}

func writeOpenClawConfig(t *testing.T, home, content string) {
	t.Helper()
	dir := filepath.Join(home, ".openclaw")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, openClawConfigFileName), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// A gateway running as a service unit routinely has no HOME and can still have been given a state
// directory. Refusing to resolve the path in that case ignores the override exactly where it is
// most likely to be in use, so the install and the discovery probe both abort for a machine that
// is perfectly well configured.
func TestOpenClawStateDirOverrideWorksWithoutAHomeDirectory(t *testing.T) {
	state := filepath.Join(t.TempDir(), "gateway-state")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv(openClawStateDirEnv, state)

	path, err := OpenClawEntryPath(LevelUser)
	if err != nil {
		t.Fatalf("OpenClawEntryPath with no home but %s set: %v", openClawStateDirEnv, err)
	}
	want := filepath.Join(state, "extensions", "beacon-endpoint", "beacon.js")
	if path != want {
		t.Fatalf("entry path = %q, want %q", path, want)
	}
}

// With neither a home directory nor the override there is genuinely nothing to resolve from, and
// the error should name the missing home rather than something further down.
func TestOpenClawEntryPathReportsTheMissingHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv(openClawStateDirEnv, "")

	if _, err := OpenClawEntryPath(LevelUser); err == nil {
		t.Fatal("expected an error with no home directory and no state-dir override")
	}
}

// A project install resolves from the working directory, so it never needed a home directory.
func TestOpenClawProjectPathWorksWithoutAHomeDirectory(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv(openClawStateDirEnv, "")

	path, err := OpenClawEntryPath(LevelProject)
	if err != nil {
		t.Fatalf("project entry path with no home: %v", err)
	}
	if want := filepath.Join(wd, ".openclaw", "extensions", "beacon-endpoint", "beacon.js"); path != want {
		t.Fatalf("project entry path = %q, want %q", path, want)
	}
}
