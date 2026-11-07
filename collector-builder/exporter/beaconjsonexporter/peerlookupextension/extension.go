// Package peerlookupextension is a marker: a collector that lists the beacon_peer_lookup extension
// in `beacon-otelcol components` has a beacon_peer processor that can ask the operating system who
// owns a loopback client socket on Linux, macOS and Windows.
//
// Collectors from before macOS and Windows support already carry beacon_peer and beacon_otlp_http,
// built with a lookup that always answers "unsupported" there, so their component lists alone do
// not tell the CLI whether peer attribution would name anyone. A config rendered with peer_user for
// such a collector would record every OTLP event with no user at all, where it used to carry the
// collector's account. The CLI therefore renders peer attribution on macOS and Windows only for a
// collector that lists this extension, and the previous config otherwise.
//
// The extension does nothing, and no config needs to name it; it exists to be listed. Being an
// extension in the collector's own build, it cannot be present without the peercred code it
// stands for, unlike a version number, which development builds and forks do not keep.
package peerlookupextension

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
)

// Type is the component name the CLI looks for.
const Type = "beacon_peer_lookup"

var componentType = component.MustNewType(Type)

// Config has no settings.
type Config struct{}

func NewFactory() extension.Factory {
	return extension.NewFactory(
		componentType,
		func() component.Config { return &Config{} },
		func(context.Context, extension.Settings, component.Config) (extension.Extension, error) {
			return noop{}, nil
		},
		component.StabilityLevelBeta,
	)
}

type noop struct{}

func (noop) Start(context.Context, component.Host) error { return nil }
func (noop) Shutdown(context.Context) error              { return nil }
