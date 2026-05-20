package openclaw

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrintConfigIncludesPluginConfigEndpointAndPrivacyNote(t *testing.T) {
	out := PrintConfig(Config{
		Endpoint:    "http://127.0.0.1:4318",
		Protocol:    "http/protobuf",
		ServiceName: "openclaw-gateway",
	})
	for _, want := range []string{
		"openclaw plugins install clawhub:@openclaw/diagnostics-otel",
		"openclaw plugins enable diagnostics-otel",
		`endpoint: "http://127.0.0.1:4318"`,
		`protocol: "http/protobuf"`,
		`serviceName: "openclaw-gateway"`,
		"does not export raw prompt",
		DocsURL,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("PrintConfig missing %q:\n%s", want, out)
		}
	}
}

func TestOpenClawEventStatusAndSince(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	data := strings.Join([]string{
		`{"timestamp":"2026-05-13T16:18:40Z","harness":{"name":"cursor"}}`,
		`{"timestamp":"2026-05-13T16:20:40Z","harness":{"name":"openclaw_gateway"}}`,
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	if !HasRecentOpenClawEvent(path) {
		t.Fatal("expected OpenClaw event to be detected")
	}
	if !HasOpenClawEventSince(path, time.Date(2026, 5, 13, 16, 20, 0, 0, time.UTC)) {
		t.Fatal("expected OpenClaw event after since timestamp")
	}
	if HasOpenClawEventSince(path, time.Date(2026, 5, 13, 16, 21, 0, 0, time.UTC)) {
		t.Fatal("did not expect OpenClaw event after later since timestamp")
	}

	status := GetStatus(path)
	if !status.LastEventObserved {
		t.Fatal("expected status to observe OpenClaw event")
	}
	if got, want := status.LastEventObservedAt, "2026-05-13T16:20:40Z"; got != want {
		t.Fatalf("LastEventObservedAt = %q, want %q", got, want)
	}
}
