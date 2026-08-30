package asymptoteobserve

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateFidelityValues(t *testing.T) {
	for _, fidelity := range []string{"", FidelityObserved, FidelityInferred} {
		t.Run("accepts_"+fidelity, func(t *testing.T) {
			event := NewEvent(NewEventOptions{
				Action:   "tool.invoked",
				Harness:  HarnessInfo{Name: "cursor"},
				Fidelity: fidelity,
			})
			if err := event.Validate(); err != nil {
				t.Fatalf("Validate rejected fidelity %q: %v", fidelity, err)
			}
		})
	}

	// "probably" is the shape of the mistake worth guarding: a third value invented at a call
	// site, which would leave consumers filtering on a vocabulary that no longer covers the log.
	event := NewEvent(NewEventOptions{Action: "tool.invoked", Harness: HarnessInfo{Name: "cursor"}, Fidelity: "probably"})
	if err := event.Validate(); err == nil || !strings.Contains(err.Error(), "event.fidelity must be observed or inferred") {
		t.Fatalf("Validate error = %v, want fidelity error", err)
	}
}

func TestValidateCollectionMethodValues(t *testing.T) {
	for _, method := range []string{"", CollectionMethodHook, CollectionMethodOTLP, CollectionMethodPlugin, CollectionMethodPoll} {
		t.Run("accepts_"+method, func(t *testing.T) {
			event := NewEvent(NewEventOptions{
				Action:  "tool.invoked",
				Harness: HarnessInfo{Name: "cursor", CollectionMethod: method},
			})
			if err := event.Validate(); err != nil {
				t.Fatalf("Validate rejected collection_method %q: %v", method, err)
			}
		})
	}

	event := NewEvent(NewEventOptions{Action: "tool.invoked", Harness: HarnessInfo{Name: "cursor", CollectionMethod: "webhook"}})
	if err := event.Validate(); err == nil || !strings.Contains(err.Error(), "harness.collection_method must be hook, otlp, plugin, or poll") {
		t.Fatalf("Validate error = %v, want collection_method error", err)
	}
}

// Both fields are additive and optional, so an event from a producer that has never heard of them
// must serialize exactly as it did before. Asserting on absence rather than on a default value is
// the point: an empty string written into either field would be a value consumers have to handle.
func TestProvenanceFieldsOmittedWhenUnset(t *testing.T) {
	event := NewEvent(NewEventOptions{Action: "tool.invoked", Harness: HarnessInfo{Name: "cursor"}})
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"fidelity", "collection_method"} {
		if strings.Contains(string(data), key) {
			t.Fatalf("unset provenance field %q serialized: %s", key, data)
		}
	}
}

func TestProvenanceFieldsSerializeUnderSchemaNames(t *testing.T) {
	event := NewEvent(NewEventOptions{
		Action:   "command.executed",
		Harness:  HarnessInfo{Name: "codex_cli", CollectionMethod: CollectionMethodOTLP},
		Fidelity: FidelityInferred,
	})
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Event struct {
			Fidelity string `json:"fidelity"`
		} `json:"event"`
		Harness struct {
			CollectionMethod string `json:"collection_method"`
		} `json:"harness"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Event.Fidelity != FidelityInferred {
		t.Fatalf("event.fidelity = %q, want %q", decoded.Event.Fidelity, FidelityInferred)
	}
	if decoded.Harness.CollectionMethod != CollectionMethodOTLP {
		t.Fatalf("harness.collection_method = %q, want %q", decoded.Harness.CollectionMethod, CollectionMethodOTLP)
	}
}

func TestCollectionMethodForPlatform(t *testing.T) {
	for platform, want := range map[string]string{
		// Hook-shaped: the vendor exposes a hook configuration and Beacon registers in it.
		"cursor":        CollectionMethodHook,
		"claude":        CollectionMethodHook,
		"devin":         CollectionMethodHook,
		"devin-cli":     CollectionMethodHook,
		"devin-desktop": CollectionMethodHook,
		"grok":          CollectionMethodHook,
		"antigravity":   CollectionMethodHook,
		"hermes":        CollectionMethodHook,
		"factory":       CollectionMethodHook,
		"copilot":       CollectionMethodHook,
		// vscode is the case that justifies keying on --platform rather than on the normalized
		// harness name: its hook and OTLP telemetry both normalize to vscode_copilot, so the
		// harness name cannot distinguish them and only the flag can.
		"vscode": CollectionMethodHook,
		// Plugin-shaped: Beacon ships and versions the file the runtime loads.
		"opencode": CollectionMethodPlugin,
		"cline":    CollectionMethodPlugin,
		"pi":       CollectionMethodPlugin,
		"omp":      CollectionMethodPlugin,
		// Unset stays unset rather than defaulting to a method, so an event with no platform does
		// not claim one.
		"": "",
	} {
		if got := CollectionMethodForPlatform(platform); got != want {
			t.Fatalf("CollectionMethodForPlatform(%q) = %q, want %q", platform, got, want)
		}
	}
}

// Case and surrounding whitespace come from a hook command line written into a settings file, so
// they are worth normalizing rather than trusting.
func TestCollectionMethodForPlatformNormalizesInput(t *testing.T) {
	for _, platform := range []string{"Cline", "  cline  ", "CLINE"} {
		if got := CollectionMethodForPlatform(platform); got != CollectionMethodPlugin {
			t.Fatalf("CollectionMethodForPlatform(%q) = %q, want %q", platform, got, CollectionMethodPlugin)
		}
	}
}

// An unrecognized platform resolves to hook rather than to empty, mirroring how
// NormalizeHarnessName keeps an unknown runtime's own name instead of dropping it. A new
// hook-shaped integration then needs no change here; a new plugin-shaped one does.
func TestCollectionMethodForPlatformDefaultsToHook(t *testing.T) {
	if got := CollectionMethodForPlatform("some-future-runtime"); got != CollectionMethodHook {
		t.Fatalf("CollectionMethodForPlatform(unknown) = %q, want %q", got, CollectionMethodHook)
	}
}

func TestValidCollectionMethodAndFidelityRejectEmpty(t *testing.T) {
	// Validate() checks for empty separately before calling these, so the predicates themselves
	// treat "" as not-a-value. Pinning that keeps a future caller from using them as an
	// "is this allowed" check and accidentally permitting the empty string.
	if ValidCollectionMethod("") {
		t.Fatal("ValidCollectionMethod(\"\") = true, want false")
	}
	if ValidFidelity("") {
		t.Fatal("ValidFidelity(\"\") = true, want false")
	}
}
