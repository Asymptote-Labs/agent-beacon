package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

// The contract both sides have always claimed and neither has checked.
//
// A managed extension subscribes to a list of event types; the mapper here handles a list of event
// types. Both files carry a comment saying the two lists must agree, and until this test the
// agreement was maintained by hand. The failure mode is the quiet one: a name misspelled on either
// side produces no error anywhere -- the runtime dispatches nothing, the mapper is never called,
// and the only symptom is a category of agent activity silently missing from the log.
//
// Reading the extension source across the tree mirrors how the installer package already checks its
// embedded copy against plugins/. This is the one place both halves of the contract are visible at
// once, since the extension sources live outside this module.
func TestManagedExtensionSubscriptionsMatchTheirMappers(t *testing.T) {
	for _, tc := range []struct {
		runtime   string
		source    string
		supported []string
	}{
		{"pi", filepath.Join("..", "..", "..", "plugins", "pi-beacon", "src", "beacon.ts"), supportedPiEventTypes()},
		{"omp", filepath.Join("..", "..", "..", "plugins", "omp-beacon", "src", "beacon.ts"), supportedOmpEventTypes()},
	} {
		t.Run(tc.runtime, func(t *testing.T) {
			subscribed := subscribedEventTypes(t, tc.source)

			want := append([]string(nil), tc.supported...)
			sort.Strings(want)
			got := append([]string(nil), subscribed...)
			sort.Strings(got)

			if len(got) != len(want) {
				t.Fatalf("%s extension subscribes to %v but the mapper handles %v", tc.runtime, got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("%s extension subscribes to %v but the mapper handles %v; a name on "+
						"either side that the other does not have produces no telemetry and no error",
						tc.runtime, got, want)
				}
			}
		})
	}
}

// Every subscribed type must actually produce telemetry. A type both sides agree on but that the
// mapper's switch falls through on is the same silent gap wearing a matching pair of lists.
func TestEverySubscribedTypeIsMappedToAnEvent(t *testing.T) {
	fixtures := ompPayloads()
	for _, tc := range []struct {
		runtime piFamily
		source  string
	}{
		{piRuntime, filepath.Join("..", "..", "..", "plugins", "pi-beacon", "src", "beacon.ts")},
		{ompRuntime, filepath.Join("..", "..", "..", "plugins", "omp-beacon", "src", "beacon.ts")},
	} {
		t.Run(tc.runtime.platform, func(t *testing.T) {
			for _, name := range subscribedEventTypes(t, tc.source) {
				payload, ok := fixtures[name]
				if !ok {
					t.Fatalf("no fixture for %q, which the %s extension subscribes to",
						name, tc.runtime.platform)
				}
				if events := tc.runtime.endpointEvents(cloneFields(payload), "sess-1"); len(events) == 0 {
					t.Fatalf("%s subscribes to %q but the mapper produces nothing for it",
						tc.runtime.platform, name)
				}
			}
		})
	}
}

// subscribedEventTypes reads the `subscribedEvents` array out of an extension source.
//
// Parsed from the shipped file rather than duplicated here, so this test cannot pass against a list
// that only exists in the test.
func subscribedEventTypes(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("extension source is unreadable: %v", err)
	}
	block := regexp.MustCompile(`(?s)const subscribedEvents = \[(.*?)\] as const`).FindSubmatch(data)
	if len(block) != 2 {
		t.Fatalf("%s has no subscribedEvents array; the mapper's contract is with that list", path)
	}
	var names []string
	for _, match := range regexp.MustCompile(`"([a-z_]+)"`).FindAllSubmatch(block[1], -1) {
		names = append(names, string(match[1]))
	}
	if len(names) == 0 {
		t.Fatalf("%s subscribes to no events", path)
	}
	return names
}
