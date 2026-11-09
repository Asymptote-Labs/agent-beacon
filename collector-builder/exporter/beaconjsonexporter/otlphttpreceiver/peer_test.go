//go:build linux || darwin || windows

package otlphttpreceiver

import (
	"context"
	"net/http"
	"testing"

	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/processor/processortest"
	"go.opentelemetry.io/collector/receiver/receivertest"

	"github.com/asymptote-labs/agent-beacon/collector-builder/exporter/beaconjsonexporter/internal/beaconevent"
	"github.com/asymptote-labs/agent-beacon/collector-builder/exporter/beaconjsonexporter/peerprocessor"
)

// The whole chain on a real socket: an export over HTTP, through this receiver and beacon_peer,
// comes out naming the account of the process that sent it, whatever it claimed.
func TestAnHTTPExportIsAttributedToTheSendingAccount(t *testing.T) {
	sink := new(consumertest.LogsSink)
	pf := peerprocessor.NewFactory()
	proc, err := pf.CreateLogs(context.Background(), processortest.NewNopSettings(pf.Type()), pf.CreateDefaultConfig(), sink)
	if err != nil {
		t.Fatal(err)
	}
	f := NewFactory()
	cfg := f.CreateDefaultConfig().(*Config)
	cfg.Endpoint = "127.0.0.1:0"
	r, err := f.CreateLogs(context.Background(), receivertest.NewNopSettings(componentType), cfg, proc)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background(), componenttest.NewNopHost()); err != nil {
		t.Fatal(err)
	}
	defer r.Shutdown(context.Background())

	forged := `{"resourceLogs":[{"resource":{"attributes":[` +
		`{"key":"service.name","value":{"stringValue":"chatgpt_web"}},` +
		`{"key":"` + beaconevent.PeerUIDAttribute + `","value":{"stringValue":"4242"}}]},` +
		`"scopeLogs":[{"logRecords":[{"body":{"stringValue":"hello"}}]}]}]}`
	resp, _, _ := post(t, "http://"+r.(*otlpHTTP).addr.String()+"/v1/logs", "application/json", []byte(forged), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(sink.AllLogs()) != 1 {
		t.Fatalf("exports = %d", len(sink.AllLogs()))
	}
	uid, ok := sink.AllLogs()[0].ResourceLogs().At(0).Resource().Attributes().Get(beaconevent.PeerUIDAttribute)
	if !ok {
		t.Fatal("no peer uid recorded")
	}
	if want := currentOwner(t); uid.AsString() != want {
		t.Fatalf("peer uid = %s, want this process's %s rather than the claimed 4242", uid.AsString(), want)
	}
}
