package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/account"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/onboarding"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/telemetry"
)

// installEmailEnv is the environment form of `endpoint install --email`, for an agent or a script
// that installs Beacon for someone. The legacy BEACON_ONBOARDING_EMAIL does not count: fleets set it
// to one administrator's address on every machine, so it says nothing about who installed this one,
// and it keeps feeding only its own attribution submission.
const installEmailEnv = "BEACON_EMAIL"

// Swapped by tests.
var (
	telemetryIdentify = telemetry.Identify
	// onboardingHomeOwnerUID is the owner of the home directory, for onboardingProfileWritable.
	onboardingHomeOwnerUID = func() (int, error) {
		home, err := os.UserHomeDir()
		if err != nil {
			return -1, err
		}
		return sessionfs.OwnerUID(home)
	}
)

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
// BEACON_EMAIL. raw is "" when none was given.
func installEmailInput() (raw, source, name string) {
	if value := strings.TrimSpace(endpointOpts.email); value != "" {
		return value, telemetry.EmailSourceFlag, "--email"
	}
	if value := strings.TrimSpace(os.Getenv(installEmailEnv)); value != "" {
		return value, telemetry.EmailSourceEnv, installEmailEnv
	}
	return "", "", ""
}

// installEmailGiven reports whether this install was given a usable email (--email,
// BEACON_EMAIL), which the wizard then does not ask for again.
func installEmailGiven() bool {
	raw, _, _ := installEmailInput()
	if raw == "" {
		return false
	}
	_, err := onboarding.NormalizeEmail(raw)
	return err == nil
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
// cli_email_submitted, identifies the person, and records the email in a profile that has not been
// onboarded yet.
//
// A valid sign-in already names who this is, so then the email is ignored altogether: identifying it
// would re-attribute the machine to an address that never signed in.
func submitInstallEmail(stderr io.Writer) {
	raw, source, name := installEmailInput()
	if raw == "" {
		return
	}
	email, err := onboarding.NormalizeEmail(raw)
	if err != nil {
		fmt.Fprintf(stderr, "beacon: ignoring %s: %v\n", name, err)
		return
	}
	if status := onboardingAccountInspect(onboardingClock()); status.SignedIn && !status.Expired {
		return
	}
	recordEmailSubmitted(source, email)

	// A completed onboarding record stays as written: its email may be the account that signed in,
	// and replacing it would name an unverified address as the authenticated one.
	if !onboardingProfileWritable() {
		return
	}
	profile := onboardingLoad()
	if profile.Prompted() || profile.Onboarding.Email == email {
		return
	}
	profile.Onboarding.Email = email
	if err := onboardingSave(profile); err != nil {
		fmt.Fprintf(stderr, "beacon: could not record %s: %v\n", name, err)
	}
}

// recordEmailSubmitted sends cli_email_submitted for an email given without signing in, then
// identifies the person. email is already normalized.
func recordEmailSubmitted(source, email string) {
	kind := telemetry.EmailDomainWork
	if telemetry.IsFreeEmailDomain(email) {
		kind = telemetry.EmailDomainFree
	}
	telemetryCapture(telemetry.EventEmailSubmitted, map[string]any{
		telemetry.PropEmailSource:     source,
		telemetry.PropEmailDomainKind: kind,
	})
	telemetryIdentify(email, "")
}

// recordWizardEmail sends what the wizard's optional email question got: cli_email_submitted and
// Identify for an address, cli_email_prompt_skipped for a blank answer, nothing when it was not asked.
func recordWizardEmail(result onboarding.WizardResult) {
	switch {
	case !result.EmailAsked:
	case result.Email != "":
		recordEmailSubmitted(telemetry.EmailSourcePrompt, result.Email)
	default:
		telemetryCapture(telemetry.EventEmailPromptSkipped, nil)
	}
}

// saveWizardEmail records the address typed at the wizard's email question as soon as it is sent.
// A failed save is only reported: the install goes on, and Persist writes the profile again.
func saveWizardEmail(stderr io.Writer, profile onboarding.Profile) {
	if !onboardingProfileWritable() {
		return
	}
	if err := onboardingSave(profile); err != nil {
		fmt.Fprintf(stderr, "beacon: could not record the email: %v\n", err)
	}
}

// onboardingProfileWritable reports whether this process may write ~/.beacon/profile.json. root
// running with another account's HOME (sudo on macOS keeps it) must not, or it would leave a
// root-owned profile, or ~/.beacon, that the person's own runs could no longer read.
func onboardingProfileWritable() bool {
	if !onboardingIsRoot() {
		return true
	}
	uid, err := onboardingHomeOwnerUID()
	return err == nil && uid == 0
}
