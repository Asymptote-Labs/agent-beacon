package loopbackhostextension

import (
	"context"
	"net"
	"net/http"
	"testing"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/config/configauth"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/extension/extensiontest"
)

type host struct {
	component.Host
	ext map[component.ID]component.Component
}

func (h host) GetExtensions() map[component.ID]component.Component { return h.ext }

// The receiver's server is built the way the generated config builds it, so this also proves
// include_metadata carries the Host header to the authenticator.
func TestRejectsRebindingHost(t *testing.T) {
	id := component.NewID(componentType)
	ext, err := NewFactory().Create(context.Background(), extensiontest.NewNopSettings(componentType), &Config{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := confighttp.ServerConfig{
		Endpoint:        "127.0.0.1:0",
		IncludeMetadata: true,
		Auth:            &confighttp.AuthConfig{Authentication: configauth.Authentication{AuthenticatorID: id}},
	}
	listener, err := net.Listen("tcp", cfg.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	server, err := cfg.ToServer(context.Background(), host{componenttest.NewNopHost(), map[component.ID]component.Component{id: ext}},
		componenttest.NewNopTelemetrySettings(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(listener)
	defer server.Close()

	for requestHost, want := range map[string]int{
		listener.Addr().String():     http.StatusOK,
		"localhost:4318":             http.StatusOK,
		"[::1]:4318":                 http.StatusOK,
		"attacker.example:4318":      http.StatusUnauthorized,
		"localhost.attacker.example": http.StatusUnauthorized,
	} {
		req, _ := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/v1/logs", nil)
		req.Host = requestHost
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("Host %q: status = %d, want %d", requestHost, resp.StatusCode, want)
		}
	}
}
