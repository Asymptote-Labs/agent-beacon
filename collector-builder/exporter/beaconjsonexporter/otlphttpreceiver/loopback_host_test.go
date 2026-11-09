package otlphttpreceiver

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"testing"

	"go.opentelemetry.io/collector/client"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/extension/extensiontest"
	"go.opentelemetry.io/collector/receiver/receivertest"

	"github.com/asymptote-labs/agent-beacon/collector-builder/exporter/beaconjsonexporter/loopbackhostextension"
)

type authHost struct {
	component.Host
	ext map[component.ID]component.Component
}

func (h authHost) GetExtensions() map[component.ID]component.Component { return h.ext }

// The CLI renders this receiver on Linux in place of the upstream OTLP/HTTP one, with the same
// include_metadata and loopback_host settings, so it must refuse a DNS-rebinding Host exactly as
// the upstream receiver does, while still handing the pipeline the client's port.
func TestLoopbackHostAuthenticatorRefusesRebindingHosts(t *testing.T) {
	lf := loopbackhostextension.NewFactory()
	ext, err := lf.Create(context.Background(), extensiontest.NewNopSettings(lf.Type()), lf.CreateDefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	f := NewFactory()
	cfg := f.CreateDefaultConfig().(*Config)
	// The keys exactly as collector.ConfigYAML writes them under beacon_otlp_http.
	if err := confmap.NewFromStringMap(map[string]any{
		"endpoint":         "127.0.0.1:0",
		"include_metadata": true,
		"auth":             map[string]any{"authenticator": "loopback_host"},
	}).Unmarshal(cfg); err != nil {
		t.Fatal(err)
	}
	logs := &recordingLogs{}
	r, err := f.CreateLogs(context.Background(), receivertest.NewNopSettings(componentType), cfg, logs)
	if err != nil {
		t.Fatal(err)
	}
	host := authHost{componenttest.NewNopHost(), map[component.ID]component.Component{component.NewID(lf.Type()): ext}}
	if err := r.Start(context.Background(), host); err != nil {
		t.Fatal(err)
	}
	defer r.Shutdown(context.Background())
	addr := r.(*otlpHTTP).addr.String()

	body := []byte(`{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"body":{"stringValue":"hello"}}]}]}]}`)
	accepted := 0
	for requestHost, want := range map[string]int{
		addr:                         http.StatusOK,
		"localhost:4318":             http.StatusOK,
		"[::1]:4318":                 http.StatusOK,
		"attacker.example:4318":      http.StatusUnauthorized,
		"localhost.attacker.example": http.StatusUnauthorized,
	} {
		req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/logs", bytes.NewReader(body))
		req.Host = requestHost
		req.Header.Set("Content-Type", jsonContentType)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("Host %q: status = %d, want %d", requestHost, resp.StatusCode, want)
		}
		if want == http.StatusOK {
			accepted++
		}
	}

	logs.mu.Lock()
	defer logs.mu.Unlock()
	if len(logs.infos) != accepted {
		t.Fatalf("exports reaching the pipeline = %d, want only the %d loopback ones", len(logs.infos), accepted)
	}
	for _, info := range logs.infos {
		tcp, ok := info.Addr.(*net.TCPAddr)
		if !ok || tcp.Port == 0 {
			t.Fatalf("client.Info.Addr = %v, want the client's address with its port", info.Addr)
		}
		if len(info.Metadata.Get(client.MetadataHostName)) == 0 {
			// include_metadata's headers must survive the port rewrite.
			t.Errorf("client.Info lost the request metadata: %+v", info.Metadata)
		}
	}
}
