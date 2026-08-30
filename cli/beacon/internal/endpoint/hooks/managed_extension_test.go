package hooks

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// managedExtensions is every runtime integrated as a Beacon-managed extension file. Tests that
// assert a property of that shape rather than of one runtime walk this list, so a runtime added
// later inherits the same guarantees instead of quietly opting out of them.
func managedExtensions() []managedExtension {
	return []managedExtension{piExtension, ompExtension}
}

// testExtension builds a managedExtension over a caller-supplied template, so the render contract
// can be tested against a deliberately broken source without shipping one.
func testExtension(template string) managedExtension {
	return managedExtension{
		platform:    "pi",
		displayName: "Pi",
		marker:      "beacon-managed-test-extension:v1",
		template:    template,
		configPath:  PiExtensionPath,
	}
}

// The placeholder check is what turns a template rename into a loud failure instead of an install
// that reports success and spawns nothing.
func TestRenderRejectsATemplateMissingItsPlaceholder(t *testing.T) {
	ext := testExtension("// __BEACON_MANAGED_MARKER__\nconst x = 1\n")
	if _, err := ext.render("/tmp/beacon-hooks", "", ""); err == nil {
		t.Fatal("render accepted a template with no argv placeholder; an extension that spawns " +
			"nothing would have installed and reported success")
	}
}

func TestRenderRejectsUnresolvedPlaceholders(t *testing.T) {
	ext := testExtension("// __BEACON_MANAGED_MARKER__\nconst beaconArgv: string[] = [\"__BEACON_ARGV__\"]\n" +
		"const leftover = \"__BEACON_SOMETHING_ELSE__\"\n")
	if _, err := ext.render("/tmp/beacon-hooks", "", ""); err == nil {
		t.Fatal("render accepted a template with an unresolved placeholder")
	}
}

// Every shipped extension source must carry both placeholders. A source that lost one would fail
// only at install time, on a user's machine, with the runtime already configured to load it.
func TestShippedExtensionSourcesCarryTheirPlaceholders(t *testing.T) {
	for _, ext := range managedExtensions() {
		t.Run(ext.platform, func(t *testing.T) {
			if !strings.Contains(ext.template, argvPlaceholder) {
				t.Fatalf("%s extension source is missing %s", ext.platform, argvPlaceholder)
			}
			if !strings.Contains(ext.template, managedMarkerPlaceholder) {
				t.Fatalf("%s extension source is missing %s", ext.platform, managedMarkerPlaceholder)
			}
		})
	}
}

// A rendered extension must carry its own runtime's marker, its own `--platform`, and its own
// event subcommand. Getting any of the three wrong sends one runtime's payloads to the other's
// mapper, which would attribute Oh My Pi activity to Pi in every harness-grouped query.
func TestRenderedExtensionsCarryTheirOwnRuntimeIdentity(t *testing.T) {
	for _, ext := range managedExtensions() {
		t.Run(ext.platform, func(t *testing.T) {
			rendered, err := ext.render("/opt/beacon/bin/beacon-hooks", "/var/log/beacon/runtime.jsonl", "")
			if err != nil {
				t.Fatalf("render returned error: %v", err)
			}
			for _, want := range []string{
				ext.marker,
				`"--platform","` + ext.platform + `"`,
				`"` + ext.platform + `-event"`,
			} {
				if !strings.Contains(rendered, want) {
					t.Fatalf("%s rendered extension does not contain %q", ext.platform, want)
				}
			}
			for _, other := range managedExtensions() {
				if other.platform == ext.platform {
					continue
				}
				if strings.Contains(rendered, other.marker) {
					t.Fatalf("%s rendered extension carries %s's marker; an uninstall keyed on "+
						"either marker would remove the wrong file", ext.platform, other.platform)
				}
				if strings.Contains(rendered, `"`+other.platform+`-event"`) {
					t.Fatalf("%s rendered extension invokes %s-event; one runtime's payloads would "+
						"be attributed to the other", ext.platform, other.platform)
				}
			}
		})
	}
}

// Every runtime's marker must be unique. They are what install, uninstall, status, harness
// discovery and inventory all key on, and two runtimes sharing one means an uninstall of either
// removes the file of whichever it finds first.
func TestManagedExtensionMarkersAreDistinct(t *testing.T) {
	seen := map[string]string{}
	for _, ext := range managedExtensions() {
		if owner, ok := seen[ext.marker]; ok {
			t.Fatalf("%s and %s share the marker %q", owner, ext.platform, ext.marker)
		}
		seen[ext.marker] = ext.platform
	}
}

// Beacon must never overwrite a file it did not write. The extension filename is `beacon.ts` in a
// directory the user also owns, so somebody else's extension can legitimately sit at that path.
func TestInstallRefusesToOverwriteAnUnmanagedExtension(t *testing.T) {
	for _, ext := range managedExtensions() {
		t.Run(ext.platform, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "beacon.ts")
			foreign := "// somebody else's extension\nexport default function () {}\n"
			if err := os.WriteFile(path, []byte(foreign), 0644); err != nil {
				t.Fatal(err)
			}

			if err := ext.install(path, "/opt/beacon/bin/beacon-hooks", "", ""); err == nil {
				t.Fatal("install overwrote an extension Beacon did not write")
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != foreign {
				t.Fatalf("the unmanaged extension was modified: %q", string(data))
			}
		})
	}
}

// Uninstall removes only a file carrying this runtime's marker. A file at the same path without it
// belongs to someone else, and removing it would delete a user's own extension.
func TestRemoveLeavesAnUnmanagedExtensionAlone(t *testing.T) {
	for _, ext := range managedExtensions() {
		t.Run(ext.platform, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "beacon.ts")
			if err := os.WriteFile(path, []byte("// not ours\n"), 0644); err != nil {
				t.Fatal(err)
			}

			removed, err := ext.remove(path)
			if err != nil {
				t.Fatalf("remove returned error: %v", err)
			}
			if removed {
				t.Fatal("remove reported it deleted an extension Beacon did not write")
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("the unmanaged extension was deleted: %v", err)
			}
		})
	}
}

// One runtime's uninstall must not remove another's extension, even at the same path. This is what
// the distinct markers buy, asserted rather than assumed.
func TestRemoveIgnoresAnotherRuntimesExtension(t *testing.T) {
	for _, ext := range managedExtensions() {
		for _, other := range managedExtensions() {
			if other.platform == ext.platform {
				continue
			}
			t.Run(ext.platform+"/"+other.platform, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "beacon.ts")
				rendered, err := other.render("/opt/beacon/bin/beacon-hooks", "", "")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(rendered), 0644); err != nil {
					t.Fatal(err)
				}

				if removed, err := ext.remove(path); err != nil || removed {
					t.Fatalf("%s uninstall removed %s's extension (removed=%t, err=%v)",
						ext.platform, other.platform, removed, err)
				}
				if ext.installedAt(path) {
					t.Fatalf("%s reports itself installed over %s's extension",
						ext.platform, other.platform)
				}
			})
		}
	}
}

// Install then uninstall must leave nothing behind, for every runtime.
func TestInstallThenRemoveRoundTrips(t *testing.T) {
	for _, ext := range managedExtensions() {
		t.Run(ext.platform, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "beacon.ts")

			if err := ext.install(path, "/opt/beacon/bin/beacon-hooks", "/var/log/beacon/runtime.jsonl", ""); err != nil {
				t.Fatalf("install returned error: %v", err)
			}
			if !ext.installedAt(path) {
				t.Fatal("installedAt does not recognize the file install just wrote")
			}

			removed, err := ext.remove(path)
			if err != nil || !removed {
				t.Fatalf("remove(removed=%t, err=%v)", removed, err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("the extension survived uninstall: %v", err)
			}
		})
	}
}

// Reinstalling over Beacon's own file is an upgrade, not a refusal.
func TestInstallOverwritesBeaconsOwnExtension(t *testing.T) {
	for _, ext := range managedExtensions() {
		t.Run(ext.platform, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "beacon.ts")

			if err := ext.install(path, "/old/beacon-hooks", "", ""); err != nil {
				t.Fatalf("first install returned error: %v", err)
			}
			if err := ext.install(path, "/new/beacon-hooks", "", ""); err != nil {
				t.Fatalf("reinstall returned error: %v", err)
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !extensionReferencesBinary(string(data), "/new/beacon-hooks") {
				t.Fatal("reinstall did not repoint the extension at the current hook binary")
			}
			if extensionReferencesBinary(string(data), "/old/beacon-hooks") {
				t.Fatal("the stale binary path survived reinstall")
			}
		})
	}
}

// A marker on disk is not enough to report telemetry as collected. An extension file survives a
// Beacon uninstall, a partial update, or a home directory restored onto a machine where the binary
// lives elsewhere; in each case the runtime loads an extension that spawns nothing.
func TestReachableStatusDowngradesAnUnreachableInstall(t *testing.T) {
	for _, ext := range managedExtensions() {
		t.Run(ext.platform, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "beacon.ts")
			binary := filepath.Join(dir, "beacon-hooks")

			if err := ext.install(path, binary, "", ""); err != nil {
				t.Fatal(err)
			}

			// The binary the extension names is not on disk.
			status := ext.reachableStatus(runtimeStatus{Installed: true, BinaryPath: binary, ConfigPath: path})
			if status.Installed {
				t.Fatal("reported installed while the hook binary it spawns is missing")
			}

			if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0755); err != nil {
				t.Fatal(err)
			}
			status = ext.reachableStatus(runtimeStatus{Installed: true, BinaryPath: binary, ConfigPath: path})
			if !status.Installed {
				t.Fatalf("reported not installed for a reachable install: %s", status.Message)
			}

			// An extension pointing at some other binary is not this install.
			other := filepath.Join(dir, "other-hooks")
			if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0755); err != nil {
				t.Fatal(err)
			}
			status = ext.reachableStatus(runtimeStatus{Installed: true, BinaryPath: other, ConfigPath: path})
			if status.Installed {
				t.Fatal("reported installed for an extension that spawns a different binary")
			}
		})
	}
}

// renderedRuntimeName extracts the distribution name the installer substituted into a shared
// template.
//
// Read back out of the rendered source rather than trusted, because this value is what the
// extension selects its subscription list from: a rendering that left it as the placeholder, or
// wrote it unquoted, produces a file that either subscribes to the wrong events or does not parse.
// The optional carriage return is load-bearing for the same reason it is in the argv helper -- the
// extension source is a checked-in file, and a Windows checkout converts its line endings.
func renderedRuntimeName(t *testing.T, source string) string {
	t.Helper()
	match := regexp.MustCompile(`(?m)^const beaconRuntime = "([^"]*)"\r?$`).FindStringSubmatch(source)
	if len(match) != 2 {
		t.Fatalf("no beaconRuntime assignment in rendered extension:\n%s", source)
	}
	return match[1]
}

// The runtime name and the --platform flag must be the same string. The extension picks its event
// subscription from the former and the hook adapter dispatches its mapper on the latter, so a
// rendering where they disagreed would subscribe to one runtime's events and map them as another's.
func TestRenderSubstitutesTheRuntimeNameIntoASharedTemplate(t *testing.T) {
	source, err := piExtension.render("/tmp/beacon-hooks", "/tmp/runtime.jsonl", "")
	if err != nil {
		t.Fatalf("piExtension.render returned error: %v", err)
	}
	if got := renderedRuntimeName(t, source); got != piExtension.platform {
		t.Fatalf("rendered beaconRuntime = %q, want %q", got, piExtension.platform)
	}
	argv := piRenderedArgv(t, source)
	platform := ""
	for i, value := range argv {
		if value == "--platform" && i+1 < len(argv) {
			platform = argv[i+1]
		}
	}
	if platform != piExtension.platform {
		t.Fatalf("rendered --platform = %q, want %q; the extension would subscribe as one runtime "+
			"and be mapped as another", platform, piExtension.platform)
	}
}

// The same guard the argv placeholder has, for the same reason: a template edit that renamed this
// placeholder would otherwise install a file that quietly falls back to the shared event list
// instead of the one its mapper handles. Only a descriptor that declares its template shared is
// held to it -- Oh My Pi renders from its own source, which carries no runtime placeholder.
func TestRenderRejectsASharedTemplateMissingTheRuntimePlaceholder(t *testing.T) {
	template := "// __BEACON_MANAGED_MARKER__\nconst beaconArgv: string[] = [\"__BEACON_ARGV__\"]\n"
	shared := testExtension(template)
	shared.sharedTemplate = true
	_, err := shared.render("/tmp/beacon-hooks", "", "")
	if err == nil {
		t.Fatal("render accepted a shared template with no runtime placeholder")
	}
	if !strings.Contains(err.Error(), "__BEACON_RUNTIME__") {
		t.Fatalf("error = %v, want it to name the missing placeholder", err)
	}
	// The same template is fine for a runtime that does not share its source, which is what keeps
	// this requirement from reaching Oh My Pi's own file.
	if _, err := testExtension(template).render("/tmp/beacon-hooks", "", ""); err != nil {
		t.Fatalf("render rejected an unshared template with no runtime placeholder: %v", err)
	}
}

// One template, two products: whatever a descriptor carries has to reach the rendered file, or a
// second distribution would silently install Pi's copy. This renders a descriptor that shares
// nothing with Pi's and asserts every field of it landed.
func TestRenderCarriesTheDescriptorIntoASharedFile(t *testing.T) {
	other := managedExtension{
		platform:       "example",
		displayName:    "Example",
		marker:         "beacon-managed-example-extension:v9",
		template:       piExtension.template,
		sharedTemplate: true,
		configPath:     PiExtensionPath,
	}
	source, err := other.render("/tmp/beacon-hooks", "", "")
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	if !strings.Contains(source, other.marker) {
		t.Fatal("rendered extension is missing the descriptor's marker, so uninstall would refuse to remove it")
	}
	if strings.Contains(source, PiManagedExtensionMarker) {
		t.Fatal("rendered extension carries Pi's marker; two distributions would claim each other's files")
	}
	if got := renderedRuntimeName(t, source); got != other.platform {
		t.Fatalf("rendered beaconRuntime = %q, want %q", got, other.platform)
	}
	argv := piRenderedArgv(t, source)
	if want := other.platform + "-event"; argv[len(argv)-1] != want {
		t.Fatalf("rendered argv ends with %q, want %q", argv[len(argv)-1], want)
	}
}

// The checked-in Pi source has to keep offering both subscription lists, because the Go side has no
// other way to tell that a rendered file will observe the events its mapper expects. A source edit
// that dropped Prime Agent's events would otherwise install a file that reports only Pi's.
func TestSharedPiExtensionSourceCarriesBothSubscriptionLists(t *testing.T) {
	for _, snippet := range []string{
		`"session_start"`,
		`"tool_call"`,
		`"tool_result"`,
		`"user_bash"`,
		`"message_end"`,
		`"session_compact"`,
		`"refine_complete"`,
		`pi: sharedEvents`,
		`prime: [...sharedEvents, ...primeOnlyEvents]`,
	} {
		if !strings.Contains(piExtension.template, snippet) {
			t.Fatalf("extension source no longer contains %q; a rendered install would subscribe to "+
				"a different set of events than its mapper handles", snippet)
		}
	}
}
