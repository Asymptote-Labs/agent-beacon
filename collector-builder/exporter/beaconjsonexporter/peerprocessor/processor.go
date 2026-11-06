// Package peerprocessor records which local account sent each OTLP export.
//
// Every account on a machine exports OTLP to the same loopback collector, and OTLP carries no
// identity a collector can trust, so the exporter used to record every OTLP event under the
// collector's own account: root on a system install, whoever actually typed the prompt. The kernel
// knows better. For an export that arrived over a loopback TCP connection, the account that owns
// the connection's client socket is the account of the process that sent it, and that is what
// this processor writes on every resource, as beaconevent.PeerUIDAttribute and PeerUserAttribute.
//
// It must run before anything that loses the request's connection details (batch does), so it goes
// first in a pipeline. It removes those attributes from every resource before writing them, so a
// sender cannot name someone else by setting them itself, and it writes nothing when the sender
// cannot be identified (another platform, a client that already closed, an address that is not
// loopback). The client address comes from client.Info, which the gRPC receiver fills with the
// peer's address and port; the beacon_otlp_http receiver does the same for HTTP, which the
// upstream HTTP receiver cannot (it keeps only the IP).
package peerprocessor

import (
	"context"
	"net"

	"go.opentelemetry.io/collector/client"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/processor/processorhelper"
	"go.uber.org/zap"

	"github.com/asymptote-labs/agent-beacon/collector-builder/exporter/beaconjsonexporter/internal/accountname"
	"github.com/asymptote-labs/agent-beacon/collector-builder/exporter/beaconjsonexporter/internal/beaconevent"
	"github.com/asymptote-labs/agent-beacon/collector-builder/exporter/beaconjsonexporter/internal/peercred"
)

var componentType = component.MustNewType("beacon_peer")

// Config has no settings: the processor's whole behavior is fixed, so a config cannot weaken it.
type Config struct{}

func NewFactory() processor.Factory {
	return processor.NewFactory(componentType, func() component.Config { return &Config{} },
		processor.WithLogs(createLogs, component.StabilityLevelBeta),
		processor.WithTraces(createTraces, component.StabilityLevelBeta),
		processor.WithMetrics(createMetrics, component.StabilityLevelBeta))
}

// peer resolves a client address to an account.
type peer struct {
	uid    func(*net.TCPAddr) (string, error)
	name   func(uid string) string
	logger *zap.Logger
}

var (
	// One name cache for the whole collector: the three signals' processors share it, as the
	// ingest extension shares its own.
	names = accountname.NewCache()
	// lookupUID and lookupName are what newPeer uses; tests replace them.
	lookupUID  = peercred.UID
	lookupName = names.Lookup
)

func newPeer(set processor.Settings) *peer {
	return &peer{uid: lookupUID, name: lookupName, logger: set.Logger}
}

// identify returns the account that owns the client end of the connection the export in ctx came
// in on, or empty strings when it cannot be named.
func (p *peer) identify(ctx context.Context) (uid, name string) {
	addr, ok := client.FromContext(ctx).Addr.(*net.TCPAddr)
	if !ok || addr == nil {
		return "", ""
	}
	uid, err := p.uid(addr)
	if err != nil {
		p.logger.Debug("could not identify the account that sent an export", zap.Stringer("client", addr), zap.Error(err))
		return "", ""
	}
	return uid, p.name(uid)
}

// stamp makes a resource's peer attributes exactly what the kernel said: none of the sender's.
func stamp(resource pcommon.Resource, uid, name string) {
	attrs := resource.Attributes()
	attrs.Remove(beaconevent.PeerUIDAttribute)
	attrs.Remove(beaconevent.PeerUserAttribute)
	if uid == "" {
		return
	}
	attrs.PutStr(beaconevent.PeerUIDAttribute, uid)
	if name != "" {
		attrs.PutStr(beaconevent.PeerUserAttribute, name)
	}
}

var mutates = processorhelper.WithCapabilities(consumer.Capabilities{MutatesData: true})

func createLogs(ctx context.Context, set processor.Settings, cfg component.Config, next consumer.Logs) (processor.Logs, error) {
	p := newPeer(set)
	return processorhelper.NewLogs(ctx, set, cfg, next, func(ctx context.Context, logs plog.Logs) (plog.Logs, error) {
		uid, name := p.identify(ctx)
		for i := 0; i < logs.ResourceLogs().Len(); i++ {
			stamp(logs.ResourceLogs().At(i).Resource(), uid, name)
		}
		return logs, nil
	}, mutates)
}

func createTraces(ctx context.Context, set processor.Settings, cfg component.Config, next consumer.Traces) (processor.Traces, error) {
	p := newPeer(set)
	return processorhelper.NewTraces(ctx, set, cfg, next, func(ctx context.Context, traces ptrace.Traces) (ptrace.Traces, error) {
		uid, name := p.identify(ctx)
		for i := 0; i < traces.ResourceSpans().Len(); i++ {
			stamp(traces.ResourceSpans().At(i).Resource(), uid, name)
		}
		return traces, nil
	}, mutates)
}

func createMetrics(ctx context.Context, set processor.Settings, cfg component.Config, next consumer.Metrics) (processor.Metrics, error) {
	p := newPeer(set)
	return processorhelper.NewMetrics(ctx, set, cfg, next, func(ctx context.Context, metrics pmetric.Metrics) (pmetric.Metrics, error) {
		uid, name := p.identify(ctx)
		for i := 0; i < metrics.ResourceMetrics().Len(); i++ {
			stamp(metrics.ResourceMetrics().At(i).Resource(), uid, name)
		}
		return metrics, nil
	}, mutates)
}
