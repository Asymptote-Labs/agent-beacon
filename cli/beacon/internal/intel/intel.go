// Package intel is how this CLI reaches Beacon Intel (https://intel.beacon.sh): the client in
// pkg/asymptoteobserve/intelapi, configured from the environment. Only an explicit `beacon intel`
// command or an agent's call to a local MCP intel_* tool reaches it; hooks, plugins and
// background jobs never do. BEACON_INTEL=0 turns it off, and BEACON_INTEL_URL points it at
// another host (https, or http on loopback only).
package intel

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/intelapi"
)

const (
	EnvURL      = "BEACON_INTEL_URL"
	EnvDisabled = "BEACON_INTEL"
)

// ErrDisabled is returned by New when BEACON_INTEL=0.
var ErrDisabled = errors.New("Beacon Intel is turned off (BEACON_INTEL=0)")

// New returns a client for the configured host, or ErrDisabled.
func New(userAgent string) (*intelapi.Client, error) {
	if Disabled() {
		return nil, ErrDisabled
	}
	base := intelapi.DefaultBaseURL
	if v := strings.TrimSpace(os.Getenv(EnvURL)); v != "" {
		base = v
	}
	client, err := intelapi.New(base, userAgent, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvURL, err)
	}
	return client, nil
}

// Disabled reports whether BEACON_INTEL turns the client off.
func Disabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvDisabled))) {
	case "0", "false", "off", "no":
		return true
	}
	return false
}
