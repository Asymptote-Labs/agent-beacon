// Package loopbackhostextension is a server authenticator that admits only requests whose Host
// header names a loopback address, so a DNS-rebinding page cannot post forged telemetry to the
// loopback OTLP HTTP receiver. The receiver needs include_metadata, which is how confighttp hands
// the Host header to an authenticator.
package loopbackhostextension

import (
	"context"
	"errors"

	"go.opentelemetry.io/collector/client"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
	"go.opentelemetry.io/collector/extension/extensionauth"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

var componentType = component.MustNewType("loopback_host")

type Config struct{}

func NewFactory() extension.Factory {
	return extension.NewFactory(
		componentType,
		func() component.Config { return &Config{} },
		func(context.Context, extension.Settings, component.Config) (extension.Extension, error) {
			return extensionauth.NewServer(extensionauth.WithServerAuthenticate(authenticate))
		},
		component.StabilityLevelBeta,
	)
}

func authenticate(ctx context.Context, _ map[string][]string) (context.Context, error) {
	hosts := client.FromContext(ctx).Metadata.Get(client.MetadataHostName)
	if len(hosts) != 1 || !asymptoteobserve.IsLoopbackHost(hosts[0]) {
		return ctx, errors.New("request host must be a loopback address")
	}
	return ctx, nil
}
