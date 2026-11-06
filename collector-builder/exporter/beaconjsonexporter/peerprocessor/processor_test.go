package peerprocessor

import (
	"context"
	"errors"
	"net"
	"testing"

	"go.opentelemetry.io/collector/client"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor/processortest"

	"github.com/asymptote-labs/agent-beacon/collector-builder/exporter/beaconjsonexporter/internal/beaconevent"
)

// fakeKernel answers for one client address only, as the socket table does.
func fakeKernel(t *testing.T, owner *net.TCPAddr, uid, name string) {
	t.Helper()
	oldUID, oldName := lookupUID, lookupName
	t.Cleanup(func() { lookupUID, lookupName = oldUID, oldName })
	lookupUID = func(addr *net.TCPAddr) (string, error) {
		if owner != nil && addr.IP.Equal(owner.IP) && addr.Port == owner.Port {
			return uid, nil
		}
		return "", errors.New("no socket")
	}
	lookupName = func(got string) string {
		if got == uid {
			return name
		}
		return ""
	}
}

func from(addr net.Addr) context.Context {
	return client.NewContext(context.Background(), client.Info{Addr: addr})
}

func forgedLogs() plog.Logs {
	logs := plog.NewLogs()
	for i := 0; i < 2; i++ {
		rl := logs.ResourceLogs().AppendEmpty()
		rl.Resource().Attributes().PutStr("service.name", "chatgpt_web")
		// What a sender would write to pass itself off as someone else.
		rl.Resource().Attributes().PutStr(beaconevent.PeerUIDAttribute, "0")
		rl.Resource().Attributes().PutStr(beaconevent.PeerUserAttribute, "root")
		rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty().Body().SetStr("hi")
	}
	return logs
}

func peerAttrs(t *testing.T, logs plog.Logs) []string {
	t.Helper()
	var out []string
	for i := 0; i < logs.ResourceLogs().Len(); i++ {
		attrs := logs.ResourceLogs().At(i).Resource().Attributes()
		get := func(key string) string {
			if v, ok := attrs.Get(key); ok {
				return v.AsString()
			}
			return ""
		}
		out = append(out, get(beaconevent.PeerUIDAttribute)+"/"+get(beaconevent.PeerUserAttribute))
	}
	return out
}

func TestTheSendersAccountReplacesWhatItClaimed(t *testing.T) {
	alice := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 50123}
	fakeKernel(t, alice, "1001", "alice")
	sink := new(consumertest.LogsSink)
	p, err := NewFactory().CreateLogs(context.Background(), processortest.NewNopSettings(componentType), &Config{}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ConsumeLogs(from(alice), forgedLogs()); err != nil {
		t.Fatal(err)
	}
	got := peerAttrs(t, sink.AllLogs()[0])
	for _, v := range got {
		if v != "1001/alice" {
			t.Fatalf("peer attributes = %v, want alice's on every resource", got)
		}
	}
}

// An export whose sender cannot be named keeps nothing a sender wrote, so the exporter falls back to
// the collector's own account rather than to the claim.
func TestAnUnidentifiedSenderLeavesNoClaim(t *testing.T) {
	fakeKernel(t, nil, "", "")
	for name, ctx := range map[string]context.Context{
		"no client info":         context.Background(),
		"socket gone":            from(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4000}),
		"address without a port": from(&net.IPAddr{IP: net.IPv4(127, 0, 0, 1)}),
	} {
		t.Run(name, func(t *testing.T) {
			sink := new(consumertest.LogsSink)
			p, err := NewFactory().CreateLogs(context.Background(), processortest.NewNopSettings(componentType), &Config{}, sink)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.ConsumeLogs(ctx, forgedLogs()); err != nil {
				t.Fatal(err)
			}
			for _, v := range peerAttrs(t, sink.AllLogs()[0]) {
				if v != "/" {
					t.Fatalf("peer attributes = %q, want none", v)
				}
			}
		})
	}
}

// An account the system cannot name is still recorded by its id.
func TestAnUnnamedAccountKeepsItsID(t *testing.T) {
	bob := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 50124}
	fakeKernel(t, bob, "1002", "")
	sink := new(consumertest.LogsSink)
	p, _ := NewFactory().CreateLogs(context.Background(), processortest.NewNopSettings(componentType), &Config{}, sink)
	if err := p.ConsumeLogs(from(bob), forgedLogs()); err != nil {
		t.Fatal(err)
	}
	if got := peerAttrs(t, sink.AllLogs()[0]); got[0] != "1002/" {
		t.Fatalf("peer attributes = %v, want uid 1002 and no name", got)
	}
}

func TestTracesAndMetricsAreStampedToo(t *testing.T) {
	alice := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 50125}
	fakeKernel(t, alice, "1001", "alice")
	set := processortest.NewNopSettings(componentType)

	traces := ptrace.NewTraces()
	traces.ResourceSpans().AppendEmpty().Resource().Attributes().PutStr(beaconevent.PeerUIDAttribute, "0")
	tsink := new(consumertest.TracesSink)
	tp, _ := NewFactory().CreateTraces(context.Background(), set, &Config{}, tsink)
	if err := tp.ConsumeTraces(from(alice), traces); err != nil {
		t.Fatal(err)
	}
	if v, _ := tsink.AllTraces()[0].ResourceSpans().At(0).Resource().Attributes().Get(beaconevent.PeerUIDAttribute); v.AsString() != "1001" {
		t.Fatalf("trace peer uid = %q", v.AsString())
	}

	metrics := pmetric.NewMetrics()
	metrics.ResourceMetrics().AppendEmpty().Resource().Attributes().PutStr(beaconevent.PeerUserAttribute, "root")
	msink := new(consumertest.MetricsSink)
	mp, _ := NewFactory().CreateMetrics(context.Background(), set, &Config{}, msink)
	if err := mp.ConsumeMetrics(from(alice), metrics); err != nil {
		t.Fatal(err)
	}
	if v, _ := msink.AllMetrics()[0].ResourceMetrics().At(0).Resource().Attributes().Get(beaconevent.PeerUserAttribute); v.AsString() != "alice" {
		t.Fatalf("metric peer user = %q", v.AsString())
	}
}

// The processor changes data, so the collector must hand it a copy when the receiver also feeds
// another pipeline (the OTLP forward): that one must not carry Beacon's attribution to a backend.
func TestDeclaresThatItMutates(t *testing.T) {
	p, _ := NewFactory().CreateLogs(context.Background(), processortest.NewNopSettings(componentType), &Config{}, new(consumertest.LogsSink))
	if !p.Capabilities().MutatesData {
		t.Fatal("MutatesData = false")
	}
}
