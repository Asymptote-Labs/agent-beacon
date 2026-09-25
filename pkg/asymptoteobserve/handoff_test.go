package asymptoteobserve

import (
	"strings"
	"testing"
)

func TestHandoffMarkerRoundTrips(t *testing.T) {
	for _, tc := range []struct{ harness, id, wantHarness string }{
		{"claude_code", "ea7f210f-402b-53b1-aec2-864a488dfcd3", "claude_code"},
		{"claude", "s-1", "claude_code"},
		{"codex_cli", "019a2b3c-thread", "codex_cli"},
		{"opencode", "ses_4f2a1b", "opencode"},
		{"cline", "subagent:lead-1:child.2", "cline"},
		{"devin-cli", "devin-sess-1", "devin-cli"},
	} {
		marker := HandoffMarker(tc.harness, tc.id)
		if marker == "" {
			t.Fatalf("HandoffMarker(%q, %q) is empty", tc.harness, tc.id)
		}
		got, ok := ParseHandoffMarker("Continue the work.\n\n" + marker + "\n")
		if !ok || got.SourceHarness != tc.wantHarness || got.SourceSessionID != tc.id {
			t.Fatalf("ParseHandoffMarker(%q) = %+v, %v", marker, got, ok)
		}
	}
}

func TestHandoffMarkerRefusesValuesItCannotCarry(t *testing.T) {
	for _, tc := range []struct{ harness, id string }{
		{"claude_code", ""},
		{"claude_code", "has space"},
		{"claude_code", "close]bracket"},
		{"claude_code", strings.Repeat("a", 201)},
		{"", "s-1"},
	} {
		if got := HandoffMarker(tc.harness, tc.id); got != "" {
			t.Fatalf("HandoffMarker(%q, %q) = %q, want no marker", tc.harness, tc.id, got)
		}
	}
}

func TestParseHandoffMarkerIgnoresLookalikes(t *testing.T) {
	for _, text := range []string{
		"",
		"no marker here",
		"[beacon-handoff from=claude_code]",
		"[beacon-handoff session=s-1 from=claude_code]",
		"[beacon-handoff from=Claude session=s-1]",
		"[beacon-handoff from=claude_code session=s 1]",
	} {
		if got, ok := ParseHandoffMarker(text); ok {
			t.Fatalf("ParseHandoffMarker(%q) = %+v, want no match", text, got)
		}
	}
	got, ok := ParseHandoffMarker("[beacon-handoff from=codex_cli session=a] and [beacon-handoff from=cline session=b]")
	if !ok || got.SourceSessionID != "a" {
		t.Fatalf("the first marker wins, got %+v", got)
	}
}
