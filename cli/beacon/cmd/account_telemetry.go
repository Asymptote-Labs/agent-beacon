package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/account"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/onboarding"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/telemetry"
)

// installEmailEnv is the environment form of `endpoint install --email`, for an agent or a script
// that installs Beacon for someone. BEACON_ONBOARDING_EMAIL, the legacy unattended answer, counts
// too.
const installEmailEnv = "BEACON_EMAIL"

// Swapped by tests.
var telemetryIdentify = telemetry.Identify

// recordSignIn sends cli_sign_in_completed and then identifies the signed-in person.
func recordSignIn(trigger string, user account.User) {
	telemetryCapture(telemetry.EventSignInCompleted, map[string]any{telemetry.PropTrigger: trigger})
	telemetryIdentify(user.Email, user.ID)
}

// identifySignedIn identifies the person of a session that was already signed in, without an event:
// nothing happened, but telemetry may not have known them yet.
func identifySignedIn(status account.Status) {
	if status.SignedIn && !status.Expired && status.User.Email != "" {
		telemetryIdentify(status.User.Email, status.User.ID)
	}
}

// installEmailInput returns the email given to this install and where it came from: --email, then
// BEACON_EMAIL, then BEACON_ONBOARDING_EMAIL. raw is "" when none was given.
func installEmailInput() (raw, source, name string) {
	if value := strings.TrimSpace(endpointOpts.email); value != "" {
		return value, telemetry.EmailSourceFlag, "--email"
	}
	for _, env := range []string{installEmailEnv, onboardingEnvEmail} {
		if value := strings.TrimSpace(os.Getenv(env)); value != "" {
			return value, telemetry.EmailSourceEnv, env
		}
	}
	return "", "", ""
}

// validateInstallEmailFlag rejects a malformed --email before the install changes anything. The
// environment forms are only warned about later: a typo in a fleet variable must not fail an install.
func validateInstallEmailFlag() error {
	if strings.TrimSpace(endpointOpts.email) == "" {
		return nil
	}
	if _, err := onboarding.NormalizeEmail(endpointOpts.email); err != nil {
		return fmt.Errorf("--email: %w", err)
	}
	return nil
}

// submitInstallEmail handles an email given to this install without signing in: it sends
// cli_email_submitted, identifies the person, and records the email in the profile unless a signed-in
// account already names who this is.
func submitInstallEmail(stderr io.Writer) {
	raw, source, name := installEmailInput()
	if raw == "" {
		return
	}
	email, err := onboarding.NormalizeEmail(raw)
	if err != nil {
		// BEACON_ONBOARDING_EMAIL is reported by the legacy path that also reads it.
		if name != onboardingEnvEmail {
			fmt.Fprintf(stderr, "beacon: ignoring %s: %v\n", name, err)
		}
		return
	}
	kind := telemetry.EmailDomainWork
	if telemetry.IsFreeEmailDomain(email) {
		kind = telemetry.EmailDomainFree
	}
	telemetryCapture(telemetry.EventEmailSubmitted, map[string]any{
		telemetry.PropEmailSource:     source,
		telemetry.PropEmailDomainKind: kind,
	})
	telemetryIdentify(email, "")

	if status := onboardingAccountInspect(onboardingClock()); status.SignedIn && !status.Expired {
		return
	}
	profile := onboardingLoad()
	if profile.Onboarding.Email == email {
		return
	}
	profile.Onboarding.Email = email
	if err := onboardingSave(profile); err != nil {
		fmt.Fprintf(stderr, "beacon: could not record %s: %v\n", name, err)
	}
}
