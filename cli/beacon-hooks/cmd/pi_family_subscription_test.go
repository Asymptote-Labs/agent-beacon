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
			subscribed := subscribedEventTypes(t, tc.source, tc.runtime)

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
			for _, name := range subscribedEventTypes(t, tc.source, tc.runtime.platform) {
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

// subscribedEventTypes reads the event list an extension subscribes to for one runtime.
//
// Parsed from the shipped file rather than duplicated here, so this test cannot pass against a list
// that only exists in the test.
//
// Two source shapes are read, because Beacon ships both. Oh My Pi has a source of its own and names
// its list outright. Pi's source is shared with Prime Agent, which subscribes to Pi's events plus
// two of its own, so it declares one list per distribution in an eventsByRuntime map and the
// installer substitutes the runtime name that selects between them. Resolving that map here is what
// keeps the check on the file that actually ships: reading only a flat array would have this test
// pass by finding nothing to disagree with.
func subscribedEventTypes(t *testing.T, path, runtime string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("extension source is unreadable: %v", err)
	}
	arrays := extensionEventArrays(data)

	var names []string
	if expression, ok := runtimeEventExpression(data, runtime); ok {
		// The expression is either one list's name or a spread of several. Both resolve the same
		// way: every identifier in it that names a list contributes that list's events.
		for _, match := range regexp.MustCompile(`[A-Za-z][A-Za-z0-9]*`).FindAllString(expression, -1) {
			names = append(names, arrays[match]...)
		}
		if len(names) == 0 {
			t.Fatalf("%s maps runtime %q to %q, which names no event list", path, runtime, expression)
		}
	} else {
		names = arrays["subscribedEvents"]
		if len(names) == 0 {
			t.Fatalf("%s has no subscribedEvents array and no entry for runtime %q; the mapper's "+
				"contract is with that list", path, runtime)
		}
	}
	return names
}

// extensionEventArrays collects every `const <name> = [...] as const` list in an extension source,
// keyed by the name it is declared under.
func extensionEventArrays(data []byte) map[string][]string {
	arrays := map[string][]string{}
	for _, declaration := range regexp.MustCompile(`(?s)const ([A-Za-z][A-Za-z0-9]*) = \[(.*?)\] as const`).FindAllSubmatch(data, -1) {
		var names []string
		for _, match := range regexp.MustCompile(`"([a-z_]+)"`).FindAllSubmatch(declaration[2], -1) {
			names = append(names, string(match[1]))
		}
		arrays[string(declaration[1])] = names
	}
	return arrays
}

// runtimeEventExpression returns what a shared source's eventsByRuntime map assigns to one runtime,
// and whether the source has such a map at all.
func runtimeEventExpression(data []byte, runtime string) (string, bool) {
	block := regexp.MustCompile(`(?s)const eventsByRuntime[^=]*= \{(.*?)\n\}`).FindSubmatch(data)
	if len(block) != 2 {
		return "", false
	}
	entry := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(runtime) + `:\s*(.+?),?\s*$`).FindSubmatch(block[1])
	if len(entry) != 2 {
		return "", false
	}
	return string(entry[1]), true
}

// The shared source's second list, which no mapper claims yet.
//
// Prime Agent subscribes to Pi's events plus two of its own, declared as a spread of both lists.
// Nothing above reads that form -- Pi's entry names a single list outright -- so without this the
// resolver could stop understanding a spread and the first symptom would be Prime Agent's own
// mapper contract, once it lands, checking an empty list and passing.
func TestPrimeAgentSubscribesToPiEventsPlusItsOwn(t *testing.T) {
	source := filepath.Join("..", "..", "..", "plugins", "pi-beacon", "src", "beacon.ts")

	want := append(subscribedEventTypes(t, source, "pi"), "session_compact", "refine_complete")
	sort.Strings(want)
	got := subscribedEventTypes(t, source, "prime")
	sort.Strings(got)

	if len(got) != len(want) {
		t.Fatalf("prime subscribes to %v, want Pi's events plus its own: %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("prime subscribes to %v, want Pi's events plus its own: %v", got, want)
		}
	}
}
