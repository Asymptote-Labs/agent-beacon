package asymptoteobserve

import (
	"strings"
	"testing"
)

func TestEnvelopeValidate(t *testing.T) {
	valid := NewEnvelope(OriginCI, HarnessInfo{Name: "claude"}, map[string]interface{}{"event": "raw"})
	valid.Session = &SessionInfo{ID: "session-1"}
	valid.Run = &RunInfo{Provider: "github_actions", RunID: "123"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate returned error for a NewEnvelope: %v", err)
	}

	tests := []struct {
		name     string
		envelope Envelope
		want     string
	}{
		{"origin", NewEnvelope("lambda", HarnessInfo{Name: "claude"}, nil), "origin"},
		{"vendor", Envelope{Vendor: "other", SchemaVersion: SchemaVersion, Origin: OriginLocal, Harness: HarnessInfo{Name: "claude"}}, "vendor must be beacon"},
		{"schema", Envelope{Vendor: Vendor, Origin: OriginLocal, Harness: HarnessInfo{Name: "claude"}}, "schema_version is required"},
		{"harness", Envelope{Vendor: Vendor, SchemaVersion: SchemaVersion, Origin: OriginLocal}, "harness.name is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.envelope.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate error = %v, want %q", err, tt.want)
			}
		})
	}
}
