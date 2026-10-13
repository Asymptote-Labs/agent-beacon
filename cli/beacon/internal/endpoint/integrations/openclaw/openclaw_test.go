package openclaw

import (
	"fmt"
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
		`{"timestamp":"2026-05-13T16:20:40Z","harness":{"name":"openclaw_gateway","collection_method":"otlp"}}`,
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

func TestOTLPValidationSeparatesOtherCollectionMethods(t *testing.T) {
	old := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	recent := old.Add(2 * time.Hour)
	row := func(method string, timestamp time.Time) string {
		return fmt.Sprintf(`{"timestamp":%q,"harness":{"name":"openclaw-gateway","collection_method":%q}}`, timestamp.Format(time.RFC3339Nano), method)
	}
	for _, test := range []struct {
		name     string
		rows     []string
		observed bool
		last     time.Time
	}{
		{"otlp", []string{row("otlp", old)}, true, old},
		{"plugin", []string{row("plugin", recent)}, false, time.Time{}},
		{"poll", []string{row("poll", recent)}, false, time.Time{}},
		{"hook", []string{row("hook", recent)}, false, time.Time{}},
		{"newer plugin does not refresh old otlp", []string{row("otlp", old), row("plugin", recent)}, true, old},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime.jsonl")
			if err := os.WriteFile(path, []byte(strings.Join(test.rows, "\n")+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			status := GetStatus(path)
			if got := HasRecentOpenClawEvent(path); got != test.observed {
				t.Fatalf("observed = %v, want %v", got, test.observed)
			}
			if status.LastEventObserved != test.observed {
				t.Fatalf("status observed = %v, want %v", status.LastEventObserved, test.observed)
			}
			if !test.last.IsZero() && status.LastEventObservedAt != test.last.Format(time.RFC3339) {
				t.Fatalf("last timestamp = %s, want %s", status.LastEventObservedAt, test.last)
			}
			if HasOpenClawEventSince(path, old.Add(time.Hour)) {
				t.Fatal("non-OTLP events must not refresh OTLP validation")
			}
		})
	}
}
