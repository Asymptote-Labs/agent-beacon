package hooks

import (
	"regexp"
	"strings"
	"testing"
)

// piFamilyRenderedRuntime extracts the distribution name the installer substituted.
//
// Read back out of the rendered source rather than trusted, because this value is what the
// extension selects its subscription list from: a rendering that left it as the placeholder, or
// wrote it unquoted, produces a file that either subscribes to the wrong events or does not parse.
// The optional carriage return is load-bearing for the same reason it is in the argv helper -- the
// extension source is a checked-in file, and a Windows checkout converts its line endings.
func piFamilyRenderedRuntime(t *testing.T, source string) string {
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
func TestRenderPiFamilyExtensionSubstitutesTheRuntimeName(t *testing.T) {
	source, err := renderPiExtension("/tmp/beacon-hooks", "/tmp/runtime.jsonl", "")
	if err != nil {
		t.Fatalf("renderPiExtension returned error: %v", err)
	}
	if got := piFamilyRenderedRuntime(t, source); got != piFamilyPi.platform {
		t.Fatalf("rendered beaconRuntime = %q, want %q", got, piFamilyPi.platform)
	}
	argv := piRenderedArgv(t, source)
	platform := ""
	for i, value := range argv {
		if value == "--platform" && i+1 < len(argv) {
			platform = argv[i+1]
		}
	}
	if platform != piFamilyPi.platform {
		t.Fatalf("rendered --platform = %q, want %q; the extension would subscribe as one runtime "+
			"and be mapped as another", platform, piFamilyPi.platform)
	}
}

// The same guard the argv placeholder has, for the same reason: a template edit that renamed this
// placeholder would otherwise install a file that quietly falls back to the shared event list
// instead of the one its mapper handles.
func TestRenderPiFamilyExtensionRejectsATemplateMissingTheRuntimePlaceholder(t *testing.T) {
	template := "// __BEACON_MANAGED_MARKER__\nconst beaconArgv: string[] = [\"__BEACON_ARGV__\"]\n"
	_, err := renderPiFamilyExtension(template, piFamilyPi, "/tmp/beacon-hooks", "", "")
	if err == nil {
		t.Fatal("renderPiFamilyExtension accepted a template with no runtime placeholder")
	}
	if !strings.Contains(err.Error(), "__BEACON_RUNTIME__") {
		t.Fatalf("error = %v, want it to name the missing placeholder", err)
	}
}

// One template, two products: whatever a runtime descriptor carries has to reach the rendered file,
// or a second distribution would silently install Pi's copy. This renders a descriptor that shares
// nothing with Pi's and asserts every field of it landed.
func TestRenderPiFamilyExtensionCarriesTheDescriptorIntoTheFile(t *testing.T) {
	other := piFamilyRuntime{
		platform:    "example",
		hookCommand: "example-event",
		marker:      "beacon-managed-example-extension:v9",
		displayName: "Example",
	}
	source, err := renderPiFamilyExtension(piFamilyExtensionTemplate, other, "/tmp/beacon-hooks", "", "")
	if err != nil {
		t.Fatalf("renderPiFamilyExtension returned error: %v", err)
	}
	if !strings.Contains(source, other.marker) {
		t.Fatal("rendered extension is missing the descriptor's marker, so uninstall would refuse to remove it")
	}
	if strings.Contains(source, PiManagedExtensionMarker) {
		t.Fatal("rendered extension carries Pi's marker; two distributions would claim each other's files")
	}
	if got := piFamilyRenderedRuntime(t, source); got != other.platform {
		t.Fatalf("rendered beaconRuntime = %q, want %q", got, other.platform)
	}
	argv := piRenderedArgv(t, source)
	if argv[len(argv)-1] != other.hookCommand {
		t.Fatalf("rendered argv ends with %q, want %q", argv[len(argv)-1], other.hookCommand)
	}
}

// The checked-in extension has to keep offering both subscription lists, because the Go side has no
// other way to tell that a rendered file will observe the events its mapper expects. A source edit
// that dropped Prime Agent's events would otherwise install a file that reports only Pi's.
func TestPiFamilyExtensionSourceCarriesBothSubscriptionLists(t *testing.T) {
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
		if !strings.Contains(piFamilyExtensionTemplate, snippet) {
			t.Fatalf("extension source no longer contains %q; a rendered install would subscribe to "+
				"a different set of events than its mapper handles", snippet)
		}
	}
}
