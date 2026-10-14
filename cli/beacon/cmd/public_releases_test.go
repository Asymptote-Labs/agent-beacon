package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/updatecheck"
)

// A build that is not updated from Beacon's public releases must say so, and do nothing else,
// from every command that would otherwise look at those releases. None of these touch the
// network or need root on that path.
func TestUpdateCommandsDeclineWhenBuildIsNotUpdatedFromPublicReleases(t *testing.T) {
	updatecheck.PublicReleases = false
	t.Cleanup(func() { updatecheck.PublicReleases = true })

	for name, run := range map[string]func(*cobra.Command, []string) error{
		"version check":   runVersionCheck,
		"endpoint update": runEndpointUpdate,
		"update enable":   runUpdateEnable,
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&out)
			if err := run(cmd, nil); err != nil {
				t.Fatalf("%s returned %v", name, err)
			}
			if got := strings.TrimSpace(out.String()); got != notUpdatedFromPublicReleases {
				t.Fatalf("%s printed %q, want %q", name, got, notUpdatedFromPublicReleases)
			}
		})
	}
}
