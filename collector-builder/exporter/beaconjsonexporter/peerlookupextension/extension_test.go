package peerlookupextension

import (
	"context"
	"testing"

	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/extension/extensiontest"
)

// The CLI matches this exact name in `beacon-otelcol components`; renaming it turns peer attribution
// off on macOS and Windows for every collector built after the rename.
func TestTheMarkerIsListedUnderItsName(t *testing.T) {
	f := NewFactory()
	if got := f.Type().String(); got != "beacon_peer_lookup" {
		t.Fatalf("type = %q, want beacon_peer_lookup", got)
	}
	ext, err := f.Create(context.Background(), extensiontest.NewNopSettings(f.Type()), f.CreateDefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := ext.Start(context.Background(), componenttest.NewNopHost()); err != nil {
		t.Fatal(err)
	}
	if err := ext.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
