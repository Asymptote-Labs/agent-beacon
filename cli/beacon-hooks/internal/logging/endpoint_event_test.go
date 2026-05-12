package logging

import "testing"

func TestEndpointActionDoesNotClassifyRoutineStopAsBlocked(t *testing.T) {
	action, severity := endpointAction("stop-async-handler", map[string]interface{}{"message": "stop completed"})
	if action != "session.ended" || severity != "info" {
		t.Fatalf("endpointAction() = %s/%s, want session.ended/info", action, severity)
	}
}

func TestEndpointActionClassifiesViolationBlock(t *testing.T) {
	action, severity := endpointAction("stop-async-handler", map[string]interface{}{"message": "Blocking with violations"})
	if action != "policy.blocked" || severity != "high" {
		t.Fatalf("endpointAction() = %s/%s, want policy.blocked/high", action, severity)
	}
}

func TestEndpointRedaction(t *testing.T) {
	got := redactEndpointString("token=super-secret")
	if got == "token=super-secret" {
		t.Fatal("expected token to be redacted")
	}
}
