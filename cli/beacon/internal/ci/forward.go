package ci

import (
	"fmt"
	"os"
	"strings"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

// Supported SIEM forwarding providers for ci exec egress.
const (
	ForwardSplunk = "splunk"
	ForwardFalcon = "falcon"
)

// Environment variables that carry forwarding configuration. Tokens are read
// from the environment only and never accepted as command flags, so they do
// not land in CI process listings or shell history. Endpoints are not secret
// and may also be supplied via the --forward-endpoint flag.
const (
	EnvSplunkToken    = "BEACON_CI_SPLUNK_HEC_TOKEN"
	EnvSplunkEndpoint = "BEACON_CI_SPLUNK_HEC_ENDPOINT"
	EnvFalconToken    = "BEACON_CI_FALCON_HEC_TOKEN"
	EnvFalconEndpoint = "BEACON_CI_FALCON_HEC_ENDPOINT"
)

// forwardTokenEnv lists every environment variable that may hold a forwarding
// secret. These are stripped from the child command environment so the agent
// process never sees the SIEM token.
var forwardTokenEnv = []string{EnvSplunkToken, EnvFalconToken}

// normalizeForward validates and canonicalizes a --forward provider value. An
// empty value means no forwarding is configured.
func normalizeForward(provider string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "":
		return "", nil
	case ForwardSplunk:
		return ForwardSplunk, nil
	case ForwardFalcon:
		return ForwardFalcon, nil
	default:
		return "", fmt.Errorf("unsupported --forward %q; supported values are %s and %s", provider, ForwardSplunk, ForwardFalcon)
	}
}

// resolveForwardDestinations builds the SIEM forwarder configuration for the
// ephemeral collector. The endpoint may come from the flag or the provider
// endpoint env var; the token is read from the environment only. It returns
// nil when no forwarding is requested.
func resolveForwardDestinations(provider, endpointFlag string) (*endpointconfig.Destinations, error) {
	provider, err := normalizeForward(provider)
	if err != nil {
		return nil, err
	}
	switch provider {
	case ForwardSplunk:
		endpoint := firstNonEmpty(endpointFlag, os.Getenv(EnvSplunkEndpoint))
		token := strings.TrimSpace(os.Getenv(EnvSplunkToken))
		if err := requireForwardCreds(ForwardSplunk, endpoint, token, EnvSplunkEndpoint, EnvSplunkToken); err != nil {
			return nil, err
		}
		return &endpointconfig.Destinations{
			SplunkHEC: &endpointconfig.SplunkHEC{Enabled: true, Endpoint: endpoint, Token: token},
		}, nil
	case ForwardFalcon:
		endpoint := firstNonEmpty(endpointFlag, os.Getenv(EnvFalconEndpoint))
		token := strings.TrimSpace(os.Getenv(EnvFalconToken))
		if err := requireForwardCreds(ForwardFalcon, endpoint, token, EnvFalconEndpoint, EnvFalconToken); err != nil {
			return nil, err
		}
		return &endpointconfig.Destinations{
			FalconHEC: &endpointconfig.FalconHEC{Enabled: true, Endpoint: endpoint, Token: token},
		}, nil
	default:
		return nil, nil
	}
}

func requireForwardCreds(provider, endpoint, token, endpointEnv, tokenEnv string) error {
	if strings.TrimSpace(endpoint) == "" {
		return fmt.Errorf("--forward %s requires an endpoint; pass --forward-endpoint or set %s", provider, endpointEnv)
	}
	if token == "" {
		return fmt.Errorf("--forward %s requires a token; set %s in the environment (tokens are not accepted as flags)", provider, tokenEnv)
	}
	return nil
}

// forwardEndpointOf returns the configured forwarder endpoint for display. It
// never exposes the token.
func forwardEndpointOf(destinations *endpointconfig.Destinations) string {
	if destinations == nil {
		return ""
	}
	if destinations.SplunkHEC != nil {
		return destinations.SplunkHEC.Endpoint
	}
	if destinations.FalconHEC != nil {
		return destinations.FalconHEC.Endpoint
	}
	return ""
}

// stripEnv removes the named variables from a flattened environment slice.
func stripEnv(env []string, keys ...string) []string {
	if len(keys) == 0 {
		return env
	}
	drop := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		drop[key] = struct{}{}
	}
	out := env[:0]
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if _, skip := drop[name]; skip {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
