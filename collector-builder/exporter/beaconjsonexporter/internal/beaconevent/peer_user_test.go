package beaconevent

import (
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
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

// With beacon_peer in the pipeline a Claude Cowork event has no user: the kernel names the relay
// that delivered it (the same local account for everyone in the org), and the cloud identity is the
// sender's own claim, which would otherwise let any account record an event as uid 0 / root. Both
// stay in the raw attributes.
func TestCoworkEventHasNoUserUnderThePeer(t *testing.T) {
	t.Setenv("USER", "root")
	for _, forged := range []map[string]string{
		{"user.email": "person@example.com"},
		{"user.email": "root", "user.account_id": "0"},
	} {
		resource, record := peerLog("my-cowork")
		resource["cowork.surface"] = "remote"
		for k, v := range forged {
			record.Attributes().PutStr(k, v)
		}
		event := NewConverter(Options{PeerUser: true}).EventFromLog(resource, record)
		if event.Harness.Name != "claude_cowork" {
			t.Fatalf("fixture did not resolve to claude_cowork (%q)", event.Harness.Name)
		}
		if event.User != (UserInfo{}) {
			t.Fatalf("user = %+v with %v, want none: neither the relay's account nor the claim is the person", event.User, forged)
		}
		if event.Origin != "cloud" {
			t.Fatalf("origin = %q, want Cowork's surface still applied", event.Origin)
		}
		attrs, _ := event.Raw["attributes"].(map[string]interface{})
		if attrs["user.email"] != forged["user.email"] {
			t.Fatalf("raw attributes lost the Cowork identity: %v", attrs)
		}
		if attrs[PeerUIDAttribute] != "1001" || attrs[PeerUserAttribute] != "alice" {
			t.Fatalf("raw attributes lost the relaying account: %v", attrs)
		}
	}
}

// Without beacon_peer (a collector config that does not render it), Claude Cowork's cloud identity replaces the collector's own
// account, as it always has.
func TestCoworkIdentityWinsWithoutThePeer(t *testing.T) {
	resource, record := peerLog("claude-cowork")
	resource["cowork.surface"] = "desktop"
	record.Attributes().PutStr("user.email", "person@example.com")
	event := NewConverter(Options{}).EventFromLog(resource, record)
	if event.Harness.Name != "claude_cowork" {
		t.Fatalf("fixture did not resolve to claude_cowork (%q)", event.Harness.Name)
	}
	if event.User.Name != "person@example.com" {
		t.Fatalf("user = %+v, want the Cowork identity", event.User)
	}
}

// A sender cannot name an account from below the resource: beacon_peer writes the resource, and a
// record, span or datapoint attribute of the same name used to win the merge.
func TestPeerAttributesBelowTheResourceAreIgnored(t *testing.T) {
	t.Setenv("USER", "root")
	conv := NewConverter(Options{PeerUser: true})

	// The kernel named alice; the record claims root.
	resource, record := peerLog("chatgpt_web")
	record.Attributes().PutStr(PeerUIDAttribute, "0")
	record.Attributes().PutStr(PeerUserAttribute, "root")
	if got := conv.EventFromLog(resource, record).User; got.Name != "alice" || got.UID != "1001" {
		t.Fatalf("log user = %+v, want the kernel's alice over the record's claim", got)
	}

	// The kernel named nobody; the record's claim must not stand in.
	unknown := map[string]interface{}{"service.name": "chatgpt_web"}
	if got := conv.EventFromLog(unknown, record).User; got != (UserInfo{}) {
		t.Fatalf("log user = %+v, want none: the only identity was the sender's own claim", got)
	}

	span := ptrace.NewSpan()
	span.SetName("tool")
	span.Attributes().PutStr(PeerUIDAttribute, "0")
	span.Attributes().PutStr(PeerUserAttribute, "root")
	if got := conv.EventFromSpan(resource, span).User; got.Name != "alice" || got.UID != "1001" {
		t.Fatalf("span user = %+v, want alice", got)
	}
	if got := conv.EventFromSpan(unknown, span).User; got != (UserInfo{}) {
		t.Fatalf("span user = %+v, want none", got)
	}

	metrics := pmetric.NewMetrics()
	rm := metrics.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "claude-code")
	sum := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	sum.SetName("claude_code.token.usage")
	dp := sum.SetEmptySum().DataPoints().AppendEmpty()
	dp.SetIntValue(10)
	dp.Attributes().PutStr("type", "input")
	dp.Attributes().PutStr(PeerUIDAttribute, "0")
	dp.Attributes().PutStr(PeerUserAttribute, "root")
	events := conv.EventsFromMetrics(metrics)
	if len(events) == 0 {
		t.Fatal("no events from the token usage metric")
	}
	for _, e := range events {
		if e.User != (UserInfo{}) {
			t.Fatalf("metric user = %+v, want none: the datapoint's claim must not stand in", e.User)
		}
	}
}

// With the processor in the pipeline, an export it could not attribute carries no user rather than
// the collector's own account, which on a system install would read exactly like root's own.
func TestAnUnidentifiedSenderCarriesNoUser(t *testing.T) {
	t.Setenv("USER", "root")
	t.Setenv("UID", "0")
	_, record := peerLog("chatgpt_web")
	event := NewConverter(Options{PeerUser: true}).EventFromLog(map[string]interface{}{"service.name": "chatgpt_web"}, record)
	if event.User != (UserInfo{}) {
		t.Fatalf("user = %+v, want none", event.User)
	}
}

// The same holds for Claude Cowork's spans and metric datapoints, which never pass through the log
// normalizer: the relay's account must not become the person on any signal.
func TestCoworkSpansAndMetricsHaveNoUserUnderThePeer(t *testing.T) {
	t.Setenv("USER", "root")
	conv := NewConverter(Options{PeerUser: true})
	resource := map[string]interface{}{
		"service.name":    "claude-cowork",
		PeerUIDAttribute:  "1001",
		PeerUserAttribute: "alice",
	}

	span := ptrace.NewSpan()
	span.SetName("tool")
	span.Attributes().PutStr("user.email", "root")
	event := conv.EventFromSpan(resource, span)
	if event.Harness.Name != "claude_cowork" {
		t.Fatalf("span fixture did not resolve to claude_cowork (%q)", event.Harness.Name)
	}
	if event.User != (UserInfo{}) {
		t.Fatalf("span user = %+v, want none: the kernel named the relay, not the person", event.User)
	}

	metrics := pmetric.NewMetrics()
	rm := metrics.ResourceMetrics().AppendEmpty()
	for k, v := range resource {
		rm.Resource().Attributes().PutStr(k, v.(string))
	}
	sum := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	sum.SetName("claude_code.token.usage")
	dp := sum.SetEmptySum().DataPoints().AppendEmpty()
	dp.SetIntValue(10)
	dp.Attributes().PutStr("type", "input")
	events := conv.EventsFromMetrics(metrics)
	if len(events) == 0 {
		t.Fatal("no events from the Cowork token usage metric")
	}
	for _, e := range events {
		if e.Harness.Name != "claude_cowork" {
			t.Fatalf("metric fixture did not resolve to claude_cowork (%q)", e.Harness.Name)
		}
		if e.User != (UserInfo{}) {
			t.Fatalf("metric user = %+v, want none: the kernel named the relay, not the person", e.User)
		}
	}

	// Without the processor nothing changes for these signals.
	if got := NewConverter(Options{}).EventFromSpan(resource, span).User; got == (UserInfo{}) {
		t.Fatal("span user empty without peer_user; want the collector's account as before")
	}
}
