package cmd

import (
	"errors"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/onboarding"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/telemetry"
)

// installSourceEnv is set by the system packages' install scripts, so an install can tell that it
// came from a package rather than from an administrator typing `--system`.
const installSourceEnv = "BEACON_INSTALL_SOURCE"

// Swapped by tests.
var (
	telemetryCapture         = telemetry.Capture
	installTelemetryNow      = time.Now
	telemetryInstalledBefore = telemetry.InstalledBefore
	telemetryRecordInstalled = telemetry.RecordInstalled
	installManifestExists    = func(userMode bool) bool {
		_, err := lifecycle.ReadManifest(userMode)
		return !errors.Is(err, fs.ErrNotExist)
	}
)

// installTelemetry carries what cli_install_started measured to cli_install_completed.
type installTelemetry struct {
	start          time.Time
	firstForDevice bool
}

// startInstallTelemetry sends cli_install_started. It runs after the dry-run return and before the
// wizard, so the setup path is what the wizard gate will decide, not what the wizard did.
func startInstallTelemetry(discovered []harness.Harness) installTelemetry {
	profile := onboardingLoad()
	t := installTelemetry{start: installTelemetryNow(), firstForDevice: firstInstallForDevice(profile)}
	telemetryCapture(telemetry.EventInstallStarted, map[string]any{
		telemetry.PropSetupPath:         installSetupPath(profile),
		telemetry.PropDetectedHarnesses: detectedHarnessNames(discovered),
		telemetry.PropIsFirstForDevice:  t.firstForDevice,
	})
	return t
}

// firstInstallForDevice reports whether nothing says Beacon was installed here before: no wizard
// answer (only the wizard records one), no install manifest (every install path writes it, and
// uninstall removes it), and no install recorded by telemetry (which outlives uninstall). Reinstalls,
// upgrades and repairs by an agent, a script or CI are therefore not first installs.
func firstInstallForDevice(profile onboarding.Profile) bool {
	return !profile.Prompted() && !installManifestExists(endpointUserMode()) && !telemetryInstalledBefore()
}

// completed sends cli_install_completed for an install that succeeded. backfill is nil when the
// install skipped its backfill; connected is true when this install connected to Beacon Cloud.
func (t installTelemetry) completed(selection endpointTargetSelection, backfill *endpointBackfillResult, connected bool) {
	destination, cloudConnect := telemetry.DestinationLocal, telemetry.CloudConnectNone
	if !destinationAskable() {
		// Connected to Beacon Cloud, by this install or an earlier one.
		destination, cloudConnect = telemetry.DestinationCloud, telemetry.CloudConnectExisting
		if connected {
			cloudConnect = telemetry.CloudConnectNew
		}
	}
	sessions := 0
	if backfill != nil {
		sessions = len(backfill.Sessions)
	}
	telemetryCapture(telemetry.EventInstallCompleted, map[string]any{
		telemetry.PropDestination:          destination,
		telemetry.PropCloudConnect:         cloudConnect,
		telemetry.PropHarnessesConfigured:  configuredTargetNames(selection),
		telemetry.PropBackfillSessionCount: sessions,
		telemetry.PropDurationMS:           installTelemetryNow().Sub(t.start).Milliseconds(),
		telemetry.PropIsFirstForDevice:     t.firstForDevice,
	})
	telemetryRecordInstalled(installTelemetryNow())
}

// installSetupPath names how this install is being driven. The checks run in order: a package
// script can run in CI, and a CI runner with a pty could otherwise look interactive.
func installSetupPath(profile onboarding.Profile) string {
	switch {
	case strings.EqualFold(strings.TrimSpace(os.Getenv(installSourceEnv)), "package"):
		return telemetry.SetupPathPackage
	case isCIEnvironment():
		return telemetry.SetupPathCI
	}
	if run, _ := onboardingWizardPlan(profile); run {
		return telemetry.SetupPathWizard
	}
	if telemetry.InvokingAgent(os.Environ()) != telemetry.AgentNone && !onboardingIsTTY() {
		return telemetry.SetupPathAgent
	}
	return telemetry.SetupPathNoninteractive
}

// detectedHarnessNames lists the canonical names of the runtimes discovery found, sorted.
func detectedHarnessNames(discovered []harness.Harness) []string {
	names := []string{}
	for _, found := range discovered {
		if found.Detected {
			names = append(names, found.Name)
		}
	}
	sort.Strings(names)
	return names
}

// configuredTargetNames lists the install targets the selection configures, OTLP and hooks
// together, each once, sorted.
func configuredTargetNames(selection endpointTargetSelection) []string {
	seen := map[string]bool{}
	names := []string{}
	for _, name := range append(append([]string{}, selection.OTLP...), selection.Hooks...) {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
