package ci

import (
	"strings"
	"testing"
)

func TestResolveForwardSplunkFromFlagEndpoint(t *testing.T) {
	t.Setenv(EnvSplunkToken, "splunk-secret")
	t.Setenv(EnvSplunkEndpoint, "")

	dest, err := resolveForwardDestinations("Splunk", "https://splunk.example/services/collector")
	if err != nil {
		t.Fatalf("resolveForwardDestinations returned error: %v", err)
	}
	if dest == nil || dest.SplunkHEC == nil {
		t.Fatalf("expected Splunk destination, got %+v", dest)
	}
	if !dest.SplunkHEC.Enabled {
		t.Fatal("Splunk destination should be enabled")
	}
	if dest.SplunkHEC.Endpoint != "https://splunk.example/services/collector" {
		t.Fatalf("Endpoint = %q", dest.SplunkHEC.Endpoint)
	}
	if dest.SplunkHEC.Token != "splunk-secret" {
		t.Fatalf("Token = %q", dest.SplunkHEC.Token)
	}
}

func TestResolveForwardFalconUsesEnvEndpoint(t *testing.T) {
	t.Setenv(EnvFalconToken, "falcon-secret")
	t.Setenv(EnvFalconEndpoint, "https://falcon.example/services/collector")

	dest, err := resolveForwardDestinations("falcon", "")
	if err != nil {
		t.Fatalf("resolveForwardDestinations returned error: %v", err)
	}
	if dest == nil || dest.FalconHEC == nil {
		t.Fatalf("expected Falcon destination, got %+v", dest)
	}
	if dest.FalconHEC.Endpoint != "https://falcon.example/services/collector" {
		t.Fatalf("Endpoint = %q", dest.FalconHEC.Endpoint)
	}
	if dest.FalconHEC.Token != "falcon-secret" {
		t.Fatalf("Token = %q", dest.FalconHEC.Token)
	}
}

func TestResolveForwardMissingTokenErrors(t *testing.T) {
	t.Setenv(EnvSplunkToken, "")

	_, err := resolveForwardDestinations("splunk", "https://splunk.example")
	if err == nil {
		t.Fatal("expected error when token env var is unset")
	}
	if !strings.Contains(err.Error(), EnvSplunkToken) {
		t.Fatalf("error should name the token env var, got %v", err)
	}
}

func TestResolveForwardMissingEndpointErrors(t *testing.T) {
	t.Setenv(EnvSplunkToken, "splunk-secret")
	t.Setenv(EnvSplunkEndpoint, "")

	if _, err := resolveForwardDestinations("splunk", ""); err == nil {
		t.Fatal("expected error when endpoint is missing")
	}
}

func TestResolveForwardUnsupportedProvider(t *testing.T) {
	if _, err := resolveForwardDestinations("datadog", "https://example"); err == nil {
		t.Fatal("expected error for unsupported provider")
	}
}

func TestNormalizeForwardEmptyIsNoForwarding(t *testing.T) {
	got, err := normalizeForward("  ")
	if err != nil {
		t.Fatalf("normalizeForward returned error: %v", err)
	}
	if got != "" {
		t.Fatalf("normalizeForward = %q, want empty", got)
	}
}

func TestStripEnvRemovesNamedKeys(t *testing.T) {
	in := []string{"KEEP=1", EnvSplunkToken + "=secret", "ALSO=2", EnvFalconToken + "=secret2"}
	out := stripEnv(in, forwardTokenEnv...)
	joined := strings.Join(out, "\n")
	if strings.Contains(joined, "secret") {
		t.Fatalf("stripEnv left a token behind:\n%s", joined)
	}
	if !strings.Contains(joined, "KEEP=1") || !strings.Contains(joined, "ALSO=2") {
		t.Fatalf("stripEnv dropped non-token vars:\n%s", joined)
	}
}
