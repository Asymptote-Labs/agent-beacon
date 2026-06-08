package asymptoteobserve

import (
	"strings"
	"testing"
)

func TestNewEnvelopeSetsWireIdentity(t *testing.T) {
	envelope := NewEnvelope(OriginCI, HarnessInfo{Name: "claude"}, map[string]interface{}{"event": "raw"})
	envelope.Session = &SessionInfo{ID: "session-1"}
	envelope.Run = &RunInfo{Provider: "github_actions", RunID: "123"}

	if envelope.Vendor != Vendor || envelope.SchemaVersion != SchemaVersion {
		t.Fatalf("unexpected wire identity: %#v", envelope)
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
}

func TestEnvelopeValidateRejectsInvalidOrigin(t *testing.T) {
	envelope := NewEnvelope("lambda", HarnessInfo{Name: "claude"}, nil)
	if err := envelope.Validate(); err == nil {
		t.Fatal("expected invalid origin error")
	}
}

func TestEnvelopeValidateRejectsMissingFieldsDirectly(t *testing.T) {
	tests := []struct {
		name     string
		envelope Envelope
		want     string
	}{
		{
			name:     "vendor",
			envelope: Envelope{Vendor: "other", SchemaVersion: SchemaVersion, Origin: OriginLocal, Harness: HarnessInfo{Name: "claude"}},
			want:     "vendor must be beacon",
		},
		{
			name:     "schema",
			envelope: Envelope{Vendor: Vendor, Origin: OriginLocal, Harness: HarnessInfo{Name: "claude"}},
			want:     "schema_version is required",
		},
		{
			name:     "harness",
			envelope: Envelope{Vendor: Vendor, SchemaVersion: SchemaVersion, Origin: OriginLocal},
			want:     "harness.name is required",
		},
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
