package beaconingestextension

import (
	"context"
	"fmt"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
)

var componentType = component.MustNewType("beacon_ingest")

// NewFactory creates the factory for the beacon_ingest extension.
func NewFactory() extension.Factory {
	return extension.NewFactory(
		componentType,
		func() component.Config { return createDefaultConfig() },
		create,
		component.StabilityLevelBeta,
	)
}

func create(_ context.Context, set extension.Settings, raw component.Config) (extension.Extension, error) {
	cfg, ok := raw.(*Config)
	if !ok {
		return nil, fmt.Errorf("unexpected config type %T", raw)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return newIngest(cfg, set.Logger), nil
}
