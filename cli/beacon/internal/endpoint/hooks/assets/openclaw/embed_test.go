package openclawplugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rootSource returns a file in the plugin's source tree, which is the source of truth for the
// copies embedded beside this test.
func rootSource(name string) string {
	return filepath.Clean(filepath.Join("..", "..", "..", "..", "..", "..", "..", "plugins", "openclaw-beacon", "src", name))
}

// The checked-in plugin and its embedded copy are two files with the same content, and nothing but
// a test keeps them that way. They drift the moment somebody edits one and forgets `bun run sync`,
// and the drift is invisible until an install ships behavior nobody reviewed.
func TestEmbeddedPluginMatchesRootSource(t *testing.T) {
	for _, tc := range []struct {
		embedded string
		root     string
		content  string
	}{
		{"beacon.js", "beacon.js", Template},
		{"plugin.package.json", "plugin.package.json", PackageManifest},
		{"openclaw.plugin.json", "openclaw.plugin.json", PluginManifest},
	} {
		t.Run(tc.embedded, func(t *testing.T) {
			root, err := os.ReadFile(rootSource(tc.root))
			if err != nil {
				t.Fatalf("root source is unreadable: %v", err)
			}
			if string(root) != tc.content {
				t.Fatalf("plugins/openclaw-beacon/src/%s and its embedded copy have drifted; "+
					"run `bun run sync` in plugins/openclaw-beacon", tc.root)
			}
		})
	}
}

// The entry carries the two placeholders the installer substitutes. A template edit that renamed
// either would install a plugin that spawns nothing and reports success.
func TestEmbeddedPluginCarriesInstallerPlaceholders(t *testing.T) {
	for _, placeholder := range []string{`["__BEACON_ARGV__"]`, "__BEACON_MANAGED_MARKER__"} {
		if !strings.Contains(Template, placeholder) {
			t.Fatalf("the OpenClaw plugin entry is missing the %s placeholder", placeholder)
		}
	}
}

// Both manifests must be valid JSON before an installer writes them: OpenClaw rejects a plugin
// whose manifest does not parse, and it does so during discovery rather than at the call site, so
// a malformed file shows up as a plugin that simply never loads.
func TestEmbeddedManifestsAreValid(t *testing.T) {
	var pkg struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		OpenClaw struct {
			Extensions        []string `json:"extensions"`
			RuntimeExtensions []string `json:"runtimeExtensions"`
		} `json:"openclaw"`
	}
	if err := json.Unmarshal([]byte(PackageManifest), &pkg); err != nil {
		t.Fatalf("plugin package.json does not parse: %v", err)
	}
	// ESM. The entry uses `export default`, and OpenClaw imports it as a module.
	if pkg.Type != "module" {
		t.Fatalf(`package.json "type" = %q, want "module"`, pkg.Type)
	}
	// Declared under both entrypoint fields, because a plugin Beacon writes into the extensions
	// directory is reached through whichever one the host build takes.
	for _, entries := range [][]string{pkg.OpenClaw.Extensions, pkg.OpenClaw.RuntimeExtensions} {
		if len(entries) != 1 || entries[0] != "./beacon.js" {
			t.Fatalf("package.json entrypoints = %v, want [./beacon.js]", entries)
		}
	}

	var manifest struct {
		ID         string `json:"id"`
		Activation struct {
			OnStartup bool `json:"onStartup"`
		} `json:"activation"`
	}
	if err := json.Unmarshal([]byte(PluginManifest), &manifest); err != nil {
		t.Fatalf("openclaw.plugin.json does not parse: %v", err)
	}
	if manifest.ID != "beacon-endpoint" {
		t.Fatalf(`plugin id = %q, want "beacon-endpoint"`, manifest.ID)
	}
	// Without onStartup the gateway loads the plugin lazily, and the session and gateway-level
	// hooks would never be registered in time to see the events they exist for.
	if !manifest.Activation.OnStartup {
		t.Fatal("openclaw.plugin.json does not activate on startup; session hooks would never register")
	}
}

// The manifest's id and the entry's id are read at two different times -- the first before any
// plugin code loads, the second from the module -- and OpenClaw keys config, enablement and
// `plugins reload` on the manifest's. A mismatch loads a plugin the operator cannot address.
func TestPluginIDIsConsistentBetweenManifestAndEntry(t *testing.T) {
	if !strings.Contains(Template, `id: "beacon-endpoint"`) {
		t.Fatal("the plugin entry does not declare id beacon-endpoint, which openclaw.plugin.json does")
	}
}
