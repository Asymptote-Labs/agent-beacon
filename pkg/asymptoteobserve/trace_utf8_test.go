package asymptoteobserve

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTraceBundleLabelsPreserveUTF8Prefixes(t *testing.T) {
	for _, tc := range []struct{ name, text, title, preview string }{
		{"cjk", strings.Repeat("界", 60), strings.Repeat("界", 26), strings.Repeat("界", 53)},
		{"emoji", strings.Repeat("a", 79) + "😀" + strings.Repeat("a", 76) + "😀", strings.Repeat("a", 79), strings.Repeat("a", 79) + "😀" + strings.Repeat("a", 76)},
		{"ascii", strings.Repeat("a", 180), strings.Repeat("a", 80), strings.Repeat("a", 160)},
		{"exact boundary", strings.Repeat("界", 26) + "ab", strings.Repeat("界", 26) + "ab", strings.Repeat("界", 26) + "ab"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := traceTestEvents()[1]
			event.Prompt.Text = tc.text
			// Exercise the public JSONL event and trace-bundle serialization boundary.
			line, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			var decoded Event
			if err = json.Unmarshal(line, &decoded); err != nil {
				t.Fatal(err)
			}
			builder := NewTraceBuilder(TraceProjectionID(decoded), decoded)
			builder.Add(decoded, TraceEventFromEvent(decoded, "line-1", 1))
			bundle := builder.Bundle(TraceProvenanceV1{Source: "local"})
			if !utf8.ValidString(bundle.Summary.Title) || !utf8.ValidString(bundle.Summary.Preview) {
				t.Fatalf("invalid UTF-8 labels: title=%q preview=%q", bundle.Summary.Title, bundle.Summary.Preview)
			}
			if bundle.Summary.Title != tc.title || bundle.Summary.Preview != tc.preview {
				t.Fatalf("labels: title=%q preview=%q", bundle.Summary.Title, bundle.Summary.Preview)
			}
			payload, err := json.Marshal(bundle)
			if err != nil {
				t.Fatal(err)
			}
			var roundtrip TraceBundleV1
			if err = json.Unmarshal(payload, &roundtrip); err != nil {
				t.Fatal(err)
			}
			if roundtrip.Summary.Title != tc.title || roundtrip.Summary.Preview != tc.preview || roundtrip.Events[0].Content.Text != tc.text {
				t.Fatalf("bundle changed labels or retained prompt: %+v", roundtrip)
			}
		})
	}
}
