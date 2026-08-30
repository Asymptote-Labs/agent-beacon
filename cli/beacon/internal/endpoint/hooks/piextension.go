package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	piextension "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks/assets/pi"
)

// Beacon observes more than one product through the Pi coding agent's extension API.
//
// Pi (pi.dev) is the original. Prime Agent (Prime Intellect) ships the same
// @earendil-works/pi-coding-agent package with a rebranded config directory, so it exposes the same
// `on(event, handler)` surface, the same event objects, and the same default-export contract under
// a different root. Neither has a hooks configuration file to merge into or OpenTelemetry support
// to point at the local collector, so for both the integration is plugin-shaped: Beacon writes one
// extension file that forwards runtime events to the `beacon-hooks` binary.
//
// The pieces below are what the two installs share. What differs between them -- the extension
// path, the managed marker, the hook subcommand, and the runtime name written into the file -- is
// carried by piFamilyRuntime rather than duplicated into a second copy of this logic. A second copy
// is exactly what would rot: the Windows-path bug that piFamilyExtensionReferencesBinary documents
// was fixed twice already, once for Cline and once for Pi, before there was one place to fix it.
const (
	piFamilyExtensionFileName = "beacon.ts"

	// piFamilyArgvPlaceholder is the literal the extension source carries where the hook invocation
	// goes. Spelled out as a constant so the renderer can verify it was present before substituting:
	// a template edit that renamed it would otherwise install a file that spawns nothing and reports
	// success.
	piFamilyArgvPlaceholder = `["__BEACON_ARGV__"]`

	// piFamilyRuntimePlaceholder is where the distribution's name goes. The extension selects its
	// subscription list from it, so a rendering that left it unsubstituted would subscribe to the
	// fallback set rather than the one the matching mapper handles.
	piFamilyRuntimePlaceholder = `"__BEACON_RUNTIME__"`
)

// piFamilyExtensionTemplate is the single checked-in extension source both runtimes are rendered
// from. The root source of truth is plugins/pi-beacon/src/beacon.ts; a test fails if the two drift.
var piFamilyExtensionTemplate = piextension.Template

// piFamilyRuntime is everything that distinguishes one distribution's install from another's.
//
// platform is doing double duty on purpose: it is the `--platform` value the hook binary is invoked
// with *and* the runtime name substituted into the extension, because the extension's event list
// and the mapper that receives those events have to agree, and one value that feeds both cannot
// disagree with itself.
type piFamilyRuntime struct {
	platform    string
	hookCommand string
	marker      string
	displayName string
}

// renderPiFamilyExtension substitutes the marker, the runtime name, and the hook invocation into
// the extension source.
//
// The invocation goes in as a JSON array of argv, not as a command line, because the extension
// spawns the binary directly. That removes the shell -- and with it the per-shell quoting problem
// endpointCommandPrefix documents -- from runtimes that ship as a Bun binary on Windows as readily
// as on macOS. JSON encoding also means a path containing a quote or a backslash needs no special
// handling, which is exactly what a Windows path is.
func renderPiFamilyExtension(template string, rt piFamilyRuntime, binaryPath, logPath, configPath string) (string, error) {
	args := append(endpointCommandArgs(rt.platform, binaryPath, logPath, configPath), rt.hookCommand)
	argv, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	runtimeName, err := json.Marshal(rt.platform)
	if err != nil {
		return "", err
	}
	source := strings.ReplaceAll(template, "__BEACON_MANAGED_MARKER__", rt.marker)
	// An ordered list rather than a map, so a template missing both placeholders always names the
	// same one first. A test that reads an error message should not depend on map iteration order.
	for _, substitution := range []struct{ placeholder, value string }{
		{piFamilyArgvPlaceholder, string(argv)},
		{piFamilyRuntimePlaceholder, string(runtimeName)},
	} {
		if !strings.Contains(source, substitution.placeholder) {
			return "", fmt.Errorf("%s extension template is missing the %s placeholder", rt.displayName, substitution.placeholder)
		}
		source = strings.ReplaceAll(source, substitution.placeholder, substitution.value)
	}
	if strings.Contains(source, "__BEACON_") {
		return "", fmt.Errorf("%s extension template contains unresolved Beacon placeholders", rt.displayName)
	}
	return source, nil
}

// installPiFamilyExtension writes the rendered extension, refusing to clobber a file Beacon did not
// write. The runtime loads exactly one file per path, so overwriting somebody else's extension of
// the same name would delete their work rather than sit beside it.
func installPiFamilyExtension(template string, rt piFamilyRuntime, path, binaryPath, logPath, configPath string) error {
	if existing, err := os.ReadFile(path); err == nil {
		if !strings.Contains(string(existing), rt.marker) {
			return fmt.Errorf("refusing to overwrite unmanaged %s extension at %s", rt.displayName, path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	extension, err := renderPiFamilyExtension(template, rt, binaryPath, logPath, configPath)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(extension), 0644)
}

func removePiFamilyExtension(marker, path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !strings.Contains(string(data), marker) {
		return false, nil
	}
	return true, os.Remove(path)
}

func isPiFamilyExtensionInstalledAt(marker, path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), marker)
}

// piFamilyExtensionReferencesBinary reports whether a rendered extension spawns the given hook
// binary.
//
// The path is compared in the form the file actually holds it. The installer writes argv through
// json.Marshal, which escapes a backslash as two, so searching for the raw path finds nothing on
// Windows. Marshalling the path and stripping the surrounding quotes produces exactly the substring
// the file contains, on every platform, rather than a second escaping rule maintained by hand --
// the same fix clinePluginReferencesBinary documents from having gotten it wrong first.
func piFamilyExtensionReferencesBinary(source, binaryPath string) bool {
	if binaryPath == "" {
		return false
	}
	encoded, err := json.Marshal(binaryPath)
	if err != nil || len(encoded) < 2 {
		return false
	}
	return strings.Contains(source, string(encoded[1:len(encoded)-1]))
}

// piFamilyExtensionHealth decides whether an install that carries the marker is actually collecting.
//
// The marker alone is not enough, for the same reasons it is not enough for Cline: an extension file
// survives a Beacon uninstall, a partially applied update, or a home directory restored onto a
// machine where the binary lives elsewhere. In each case the runtime loads an extension that spawns
// nothing, and reporting that as installed tells an operator telemetry is being collected when none
// is.
func piFamilyExtensionHealth(rt piFamilyRuntime, extensionPath, binaryPath string) (bool, string) {
	if _, err := os.Stat(binaryPath); err != nil {
		return false, fmt.Sprintf("%s extension is installed, but Beacon hook binary is missing at %s", rt.displayName, binaryPath)
	}
	data, err := os.ReadFile(extensionPath)
	if err != nil || !piFamilyExtensionReferencesBinary(string(data), binaryPath) || strings.Contains(string(data), "__BEACON_") {
		return false, fmt.Sprintf("%s extension at %s does not reference the active Beacon hook binary", rt.displayName, extensionPath)
	}
	return true, ""
}

// The two copies of the extension source a test compares. The embedded one is what ships in the
// binary; the root one is what a contributor edits and `bun run sync` copies over. Both runtimes
// render from the same file, so one drift check covers both.
func piFamilyEmbeddedExtensionSourcePath() string {
	return filepath.Clean(filepath.Join("assets", "pi", "beacon.ts"))
}

func piFamilyRootExtensionSourcePath() string {
	return filepath.Clean(filepath.Join("..", "..", "..", "..", "..", "plugins", "pi-beacon", "src", "beacon.ts"))
}
