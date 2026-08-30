package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// A managed extension is a single TypeScript file Beacon writes into a runtime's extension
// directory, which forwards that runtime's events to the `beacon-hooks` binary.
//
// Two runtimes are integrated this shape today, Pi and Oh My Pi, and both arrived at it for the
// same reason: neither has a hooks configuration file to merge into and neither exports
// OpenTelemetry, so the extension API is the only observation surface either offers. Everything
// about handling that file is identical between them -- render the argv into a template, refuse to
// overwrite a file Beacon did not write, remove only a file carrying Beacon's marker, and report
// installed only when the installed file can actually reach the binary. Only the four facts below
// differ.
//
// Sharing this matters more than it saves lines. The install/uninstall/status trio has to agree
// about the marker and the render, and the failure when it does not is silent: an install that
// reports success and collects nothing, or an uninstall that leaves a file behind and reports it
// gone. One implementation cannot disagree with itself.
type managedExtension struct {
	// platform is the `--platform` value written into the hook invocation. It decides which mapper
	// in beacon-hooks reads the payloads and which harness the events are attributed to.
	platform string
	// displayName is how the runtime is named in status and error messages.
	displayName string
	// marker identifies a file as one Beacon wrote. See the exported per-runtime constants for why
	// it is versioned.
	marker string
	// template is the extension source with its placeholders still in it.
	template string
	// sharedTemplate marks a template that more than one runtime renders from, and which therefore
	// carries the runtime placeholder alongside the argv. Pi's source is shared with Prime Agent,
	// which ships the same @earendil-works/pi-coding-agent extension API under a rebranded config
	// directory; Oh My Pi's is its own file and carries no such placeholder.
	//
	// Declared per descriptor rather than inferred from whether the template happens to contain the
	// placeholder. A shared source that lost it would then render quietly, installing a file that
	// falls back to the common event set instead of the one its mapper handles -- and that failure
	// only shows up as events that never arrive.
	sharedTemplate bool
	// configPath resolves the file Beacon manages for an install level.
	configPath func(Level) (string, error)
}

// argvPlaceholder is the literal each extension source carries where the hook invocation goes.
//
// Spelled out as a constant so the renderer can verify it was present before substituting: a
// template edit that renamed it would otherwise install a file that spawns nothing and reports
// success.
const argvPlaceholder = `["__BEACON_ARGV__"]`

// managedMarkerPlaceholder is where the runtime's marker is substituted in. Keeping the marker out
// of the checked-in source is what lets one test assert the shipped file and its embedded twin are
// byte-identical while each install still carries a versioned marker.
const managedMarkerPlaceholder = "__BEACON_MANAGED_MARKER__"

// runtimePlaceholder is where a shared template's runtime name goes. It includes the surrounding
// quotes because the value substituted in is JSON-encoded and brings its own: the source holds
// `const beaconRuntime = "__BEACON_RUNTIME__"`, and the rendered file must hold a quoted string
// there rather than a bare identifier.
//
// The extension selects its event subscription from this value, and the hook adapter dispatches its
// mapper on the `--platform` flag, so both are rendered from the descriptor's one platform field --
// a value that feeds both cannot disagree with itself.
const runtimePlaceholder = `"__BEACON_RUNTIME__"`

func (m managedExtension) runtime() hookRuntime {
	return hookRuntime{
		displayName: m.displayName,
		configPath:  m.configPath,
		install:     m.install,
		uninstall:   m.remove,
		isInstalled: m.installedAt,
	}
}

// render substitutes the hook invocation into the extension source.
//
// The invocation goes in as a JSON array of argv, not as a command line, because the extension
// spawns the binary directly. That removes the shell -- and with it the per-shell quoting problem
// endpointCommandPrefix documents -- from runtimes that ship as compiled binaries on Windows as
// readily as on macOS. JSON encoding also means a path containing a quote or a backslash needs no
// special handling, which is exactly what a Windows path is.
func (m managedExtension) render(binaryPath, logPath, configPath string) (string, error) {
	args := append(endpointCommandArgs(m.platform, binaryPath, logPath, configPath), m.platform+"-event")
	argv, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	source := strings.ReplaceAll(m.template, managedMarkerPlaceholder, m.marker)
	// An ordered list rather than a map, so a template missing more than one placeholder always
	// names the same one first. A test that reads an error message should not depend on map
	// iteration order.
	substitutions := []struct{ placeholder, value string }{
		{argvPlaceholder, string(argv)},
	}
	if m.sharedTemplate {
		runtimeName, err := json.Marshal(m.platform)
		if err != nil {
			return "", err
		}
		substitutions = append(substitutions, struct{ placeholder, value string }{
			runtimePlaceholder, string(runtimeName),
		})
	}
	for _, substitution := range substitutions {
		if !strings.Contains(source, substitution.placeholder) {
			return "", fmt.Errorf("%s extension template is missing the %s placeholder", m.platform, substitution.placeholder)
		}
		source = strings.ReplaceAll(source, substitution.placeholder, substitution.value)
	}
	if strings.Contains(source, "__BEACON_") {
		return "", fmt.Errorf("%s extension template contains unresolved Beacon placeholders", m.platform)
	}
	return source, nil
}

func (m managedExtension) install(path, binaryPath, logPath, configPath string) error {
	if existing, err := os.ReadFile(path); err == nil {
		if !strings.Contains(string(existing), m.marker) {
			return fmt.Errorf("refusing to overwrite unmanaged %s extension at %s", m.displayName, path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	extension, err := m.render(binaryPath, logPath, configPath)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(extension), 0644)
}

func (m managedExtension) remove(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !strings.Contains(string(data), m.marker) {
		return false, nil
	}
	return true, os.Remove(path)
}

func (m managedExtension) installedAt(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), m.marker)
}

// reachableStatus downgrades an "installed" verdict that the file on disk does not support.
//
// The marker alone is not enough, for the same reasons it is not enough for Cline: an extension
// file survives a Beacon uninstall, a partially applied update, or a home directory restored onto a
// machine where the binary lives elsewhere. In each case the runtime loads an extension that spawns
// nothing, and reporting that as installed tells an operator telemetry is being collected when none
// is.
func (m managedExtension) reachableStatus(status runtimeStatus) runtimeStatus {
	if !status.Installed || status.BinaryPath == "" {
		return status
	}
	if _, err := os.Stat(status.BinaryPath); err != nil {
		status.Installed = false
		status.Message = fmt.Sprintf("%s extension is installed, but Beacon hook binary is missing at %s",
			m.displayName, status.BinaryPath)
		return status
	}
	data, err := os.ReadFile(status.ConfigPath)
	if err != nil || !extensionReferencesBinary(string(data), status.BinaryPath) ||
		strings.Contains(string(data), "__BEACON_") {
		status.Installed = false
		status.Message = fmt.Sprintf("%s extension at %s does not reference the active Beacon hook binary",
			m.displayName, status.ConfigPath)
	}
	return status
}

// extensionReferencesBinary reports whether a rendered extension spawns the given hook binary.
//
// The path is compared in the form the file actually holds it. The installer writes argv through
// json.Marshal, which escapes a backslash as two, so searching for the raw path finds nothing on
// Windows. Marshalling the path and stripping the surrounding quotes produces exactly the substring
// the file contains, on every platform, rather than a second escaping rule maintained by hand --
// the same fix clinePluginReferencesBinary documents from having gotten it wrong first.
func extensionReferencesBinary(source, binaryPath string) bool {
	if binaryPath == "" {
		return false
	}
	encoded, err := json.Marshal(binaryPath)
	if err != nil || len(encoded) < 2 {
		return false
	}
	return strings.Contains(source, string(encoded[1:len(encoded)-1]))
}
