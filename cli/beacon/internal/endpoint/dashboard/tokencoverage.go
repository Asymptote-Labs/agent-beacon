package dashboard

import (
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/inventory"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/tokens"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// buildTokenCoverage joins the runtimes Beacon configured against the ones that reported usage,
// for the dashboard's read of `beacon token-usage --coverage`.
//
// Every judgment about whether a runtime's silence is a fault stays in tokens.Coverage and its
// expectation table. This assembles the two inputs and nothing else, so the dashboard and the CLI
// cannot come to different conclusions about the same log -- which they would within a release if
// either grew its own copy of the rule.
func buildTokenCoverage(events []schema.Event, harness string) tokens.CoverageReport {
	// The harness scope is applied to both sides of the join by the same code, for the reason the
	// CLI does it here rather than in the event reader: this join is only correct while both sides
	// agree on what a harness name is, and an asymmetry between them reported a runtime that had
	// spent tokens as inactive.
	want := asymptoteobserve.NormalizeHarnessName(strings.TrimSpace(harness))
	if want != "" {
		scoped := make([]schema.Event, 0, len(events))
		for _, event := range events {
			if strings.EqualFold(asymptoteobserve.NormalizeHarnessName(event.Harness.Name), want) {
				scoped = append(scoped, event)
			}
		}
		events = scoped
	}

	// Installed runtimes come from the config scanner rather than from the log, because the whole
	// question is which configured runtime is missing from the log.
	configs := make([]tokens.InstalledConfig, 0)
	for _, config := range scanInventory(inventory.Options{}).Configs {
		configs = append(configs, tokens.InstalledConfig{
			Runtime:       config.Runtime,
			Path:          config.Path,
			Kind:          config.ConfigKind,
			BeaconManaged: config.BeaconManaged,
			Exists:        config.Exists,
		})
	}
	installed := tokens.InstalledRuntimes(configs)
	if want != "" {
		// A harness scope narrows the events, so it narrows the installed list with them.
		// Otherwise every other installed runtime has no events in the filtered set and reports
		// inactive, which reads as "installed but unused" when the truth is that the caller asked
		// about a different runtime.
		scoped := make([]string, 0, 1)
		for _, name := range installed {
			if strings.EqualFold(asymptoteobserve.NormalizeHarnessName(name), want) {
				scoped = append(scoped, name)
			}
		}
		installed = scoped
	}
	return tokens.Coverage(events, installed)
}
