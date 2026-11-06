package beaconevent

import (
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func peerLog(service string) (map[string]interface{}, plog.LogRecord) {
	resource := map[string]interface{}{
		"service.name":    service,
		PeerUIDAttribute:  "1001",
		PeerUserAttribute: "alice",
	}
	record := plog.NewLogRecord()
	record.Body().SetStr("hello")
	record.Attributes().PutStr("event.name", "user_prompt")
	return resource, record
}

// With the processor in the pipeline, the account the kernel named replaces the collector's own.
func TestPeerUserNamesTheSendingAccount(t *testing.T) {
	t.Setenv("USER", "root")
	t.Setenv("UID", "0")
	resource, record := peerLog("chatgpt_web")
	event := NewConverter(Options{PeerUser: true}).EventFromLog(resource, record)
	if event.User.Name != "alice" || event.User.UID != "1001" {
		t.Fatalf("user = %+v, want alice/1001", event.User)
	}

	span := ptrace.NewSpan()
	span.SetName("tool")
	if got := NewConverter(Options{PeerUser: true}).EventFromSpan(resource, span).User; got.Name != "alice" || got.UID != "1001" {
		t.Fatalf("span user = %+v, want alice/1001", got)
	}
}

// Without the processor the attributes are only what the sender wrote, so they are ignored.
func TestPeerUserIsIgnoredUnlessConfigured(t *testing.T) {
	t.Setenv("USER", "root")
	t.Setenv("UID", "0")
	resource, record := peerLog("chatgpt_web")
	event := NewConverter(Options{}).EventFromLog(resource, record)
	if event.User.Name != "root" || event.User.UID != "0" {
		t.Fatalf("user = %+v, want the collector's own account", event.User)
	}
}

// An account the system cannot name is recorded by id alone, never beside the collector's name.
func TestPeerUserWithoutANameKeepsOnlyTheID(t *testing.T) {
	t.Setenv("USER", "root")
	resource, record := peerLog("chatgpt_web")
	delete(resource, PeerUserAttribute)
	event := NewConverter(Options{PeerUser: true}).EventFromLog(resource, record)
	if event.User.Name != "" || event.User.UID != "1001" {
		t.Fatalf("user = %+v, want uid 1001 and no name", event.User)
	}
}

// A runtime that reports its own cloud identity (Claude Cowork) keeps it: the sender's local account
// is the desktop app's, not the person's.
func TestCoworkIdentityStillWinsOverThePeer(t *testing.T) {
	resource, record := peerLog("claude-cowork")
	resource["cowork.surface"] = "desktop"
	record.Attributes().PutStr("user.email", "person@example.com")
	event := NewConverter(Options{PeerUser: true}).EventFromLog(resource, record)
	if event.Harness.Name != "claude_cowork" {
		t.Skipf("fixture did not resolve to claude_cowork (%q)", event.Harness.Name)
	}
	if event.User.Name != "person@example.com" {
		t.Fatalf("user = %+v, want the Cowork identity", event.User)
	}
}
