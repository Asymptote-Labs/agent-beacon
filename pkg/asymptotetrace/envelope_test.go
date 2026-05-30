package asymptotetrace

import "testing"

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
