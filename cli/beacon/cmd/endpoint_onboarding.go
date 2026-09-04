package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/onboarding"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
	"github.com/spf13/cobra"
)

// Environment overrides for onboarding.
const (
	// onboardingEnvEnabled set to a false-ish value skips the prompt entirely. There is
	// no equivalent CLI flag: this exists for unattended CI and MDM installs, not as a
	// convenient way for an interactive user to decline.
	onboardingEnvEnabled = "BEACON_ONBOARDING"
	// onboardingEnvEmail and onboardingEnvUsage let a headless rollout supply the
	// answers up front. An admin deploying by MDM often does want the attribution;
	// they just have no terminal to type it into.
	onboardingEnvEmail = "BEACON_ONBOARDING_EMAIL"
	onboardingEnvUsage = "BEACON_ONBOARDING_USAGE"
	// managedIngestEnvEnabled set to a false-ish value hides the Asymptote Managed row
	// from the telemetry destination question; the question itself (local or your own
	// infrastructure) is still asked. The explicit --connect flag is unaffected: the
	// variable hides an offer, it does not override an operator's request.
	managedIngestEnvEnabled = "BEACON_MANAGED_INGEST"
)

// Reasons the prompt did not run. Surfaced by `endpoint onboarding --show` so support
// can tell "declined" apart from "never asked".
const (
	onboardingSkipCompleted     = "already_completed"
	onboardingSkipNotAterminal  = "not_a_terminal"
	onboardingSkipOptedOut      = "opted_out"
	onboardingSkipCI            = "ci"
	onboardingSkipSystemInstall = "system_install"
)

// Indirection points so the gate and the flow can be tested without a terminal, a
// network, or a real home directory.
var (
	onboardingLoad                     = onboarding.Load
	onboardingSave                     = onboarding.Save
	onboardingAsk                      = onboarding.Prompt
	onboardingSend                     = onboarding.Submit
	onboardingStdin          io.Reader = os.Stdin
	onboardingIsTTY                    = defaultOnboardingIsTTY
	onboardingIsRoot                   = func() bool { return os.Geteuid() == 0 }
	onboardingProbeFn                  = onboarding.StartRuntimeProbe
	onboardingAskWith                  = onboarding.PromptWith
	onboardingAskDestination           = onboarding.AskDestination
	// destinationAskable reports whether the destination question makes sense here: an
	// endpoint already connected to Asymptote has answered it by doing.
	destinationAskable = defaultDestinationAskable
)

// runtimeProbeBudget bounds how long submission waits on background runtime
// discovery. The probe starts before the first question, so in practice it has
// already finished by the time a human has typed an email.
const runtimeProbeBudget = 2 * time.Second

// submitBudget bounds the whole submission so a wedged endpoint cannot stall an
// install behind it.
const submitBudget = 6 * time.Second

func defaultOnboardingIsTTY() bool {
	return isCharDevice(os.Stdin) && isCharDevice(os.Stdout)
}

// maybeRunOnboarding runs the one-time signup prompt if this install should ask, and
// reports whether the user chose Asymptote Managed as the telemetry destination, which
// the install carries out with a connect once it has finished.
//
// It is called from `endpoint install` after the --dry-run early return, so a dry run
// never prompts. An error returned here does stop the install: the prompt is a
// required step on an interactive terminal, and every refusal path names the opt-out.
func maybeRunOnboarding(cmd *cobra.Command) (connect bool, err error) {
	profile := onboardingLoad()

	// Someone who already answered is never asked again, but a submission that failed
	// on a flaky network still deserves a quiet retry. The destination question is newer
	// than the signup prompt, so a machine onboarded before it existed gets it once.
	if profile.Prompted() {
		resendPendingOnboarding(&profile)
		return maybeAskDestination(cmd, &profile)
	}

	// A headless rollout that supplied answers is recorded without a terminal.
	if email, usage, ok := onboardingAnswersFromEnv(cmd.ErrOrStderr()); ok {
		completeOnboarding(cmd, &profile, email, usage, nil, "")
		return false, nil
	}

	if _, skipped := onboardingSkipReason(profile); skipped {
		return false, nil
	}

	// `install --connect` is already the answer to the destination question, so the
	// prompt does not ask it; the install records asymptote once the connect succeeds.
	ask := !endpointOpts.connect && destinationAskable()

	// Discovery shells out to every installed runtime, so start it now and collect it
	// after the questions. The latency disappears behind the user reading and typing.
	probe := onboardingProbeFn()

	answers, err := onboardingAskWith(onboardingStdin, cmd.OutOrStdout(), onboarding.PromptOptions{AskDestination: ask, OfferAsymptote: managedIngestEnabledByEnv()})
	if err != nil {
		return false, err
	}
	// Local and own-infrastructure answers are final and recorded now. Asymptote is
	// recorded by the install once the machine is actually connected
	// (recordDestinationAsymptote): if the install or the connect fails first, the record
	// stays empty and the next interactive install asks again, instead of a stored
	// answer silencing the question on a machine that never forwarded anything.
	chooseAsymptote := answers.DestinationAsked && answers.Destination == onboarding.DestinationAsymptote
	record := answers.Destination
	if chooseAsymptote {
		record = ""
	}
	completeOnboarding(cmd, &profile, answers.Email, answers.Usage, probe, record)
	return chooseAsymptote, nil
}

// maybeAskDestination asks the destination question alone, once, on an interactive
// install of a machine that was onboarded before the question existed.
func maybeAskDestination(cmd *cobra.Command, profile *onboarding.Profile) (bool, error) {
	if profile.Onboarding.Destination != "" || endpointOpts.connect {
		return false, nil
	}
	if _, skipped := destinationSkipReason(*profile); skipped {
		return false, nil
	}
	destination, err := onboardingAskDestination(onboardingStdin, cmd.OutOrStdout(), managedIngestEnabledByEnv())
	if err != nil {
		return false, err
	}
	if destination == onboarding.DestinationAsymptote {
		// Recorded by recordDestinationAsymptote once the connect has succeeded.
		return true, nil
	}
	profile.Onboarding.Destination = destination
	if err := onboardingSave(*profile); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "beacon: could not record onboarding: %v\n", err)
	}
	return false, nil
}

// recordDestinationAsymptote stores the Asymptote answer after `endpoint install` has
// connected the machine, whether the answer came from the question or from --connect.
// Until then it is unrecorded on purpose, so a failed install or connect is retried by
// asking again rather than remembered as done. A recorded local or own-infrastructure
// answer is not overwritten: --connect on such a machine is an operator's action, not a
// change of the owner's answer.
func recordDestinationAsymptote(cmd *cobra.Command) {
	profile := onboardingLoad()
	if !profile.Prompted() || profile.Onboarding.Destination != "" {
		return
	}
	profile.Onboarding.Destination = onboarding.DestinationAsymptote
	if err := onboardingSave(profile); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "beacon: could not record onboarding: %v\n", err)
	}
}

// destinationSkipReason mirrors onboardingSkipReason for the destination question, plus
// one condition of its own: an endpoint already connected to Asymptote is not asked.
func destinationSkipReason(profile onboarding.Profile) (string, bool) {
	switch {
	case profile.Onboarding.Destination != "":
		return onboardingSkipCompleted, true
	case !endpointUserMode(), onboardingIsRoot():
		return onboardingSkipSystemInstall, true
	case isCIEnvironment():
		return onboardingSkipCI, true
	case !onboardingIsTTY():
		return onboardingSkipNotAterminal, true
	case !destinationAskable():
		return "already_connected", true
	default:
		return "", false
	}
}

// managedIngestEnabledByEnv reports whether BEACON_MANAGED_INGEST permits the Asymptote
// Managed row.
func managedIngestEnabledByEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(managedIngestEnvEnabled))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// defaultDestinationAskable is false once this endpoint is connected to Asymptote: the
// machine has a destination, and the record is written when the connect succeeds.
func defaultDestinationAskable() bool {
	return !asymptote.Connected(endpointUserMode())
}

// destinationLabel is the human wording for a recorded destination value.
func destinationLabel(value string) string {
	switch value {
	case onboarding.DestinationLocal:
		return "local only (nothing forwarded)"
	case onboarding.DestinationOwnInfra:
		return "own infrastructure (forwarding pack)"
	case onboarding.DestinationAsymptote:
		return "Asymptote Managed"
	default:
		return value
	}
}

// onboardingSkipReason reports whether the prompt should stay silent, and why.
func onboardingSkipReason(profile onboarding.Profile) (string, bool) {
	switch {
	case profile.Prompted():
		return onboardingSkipCompleted, true
	case !onboardingEnabledByEnv():
		return onboardingSkipOptedOut, true
	// A system install is the MDM and package-postinstall path. It runs as root with
	// no console user to ask, and blocking it would break fleet deployment.
	case !endpointUserMode(), onboardingIsRoot():
		return onboardingSkipSystemInstall, true
	case isCIEnvironment():
		return onboardingSkipCI, true
	case !onboardingIsTTY():
		return onboardingSkipNotAterminal, true
	default:
		return "", false
	}
}

// onboardingEnabledByEnv reports whether BEACON_ONBOARDING permits the prompt.
func onboardingEnabledByEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(onboardingEnvEnabled))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// isCIEnvironment covers the CI systems that set a conventional marker. The terminal
// check already catches most of them; this is belt and braces for runners that
// allocate a pty.
func isCIEnvironment() bool {
	for _, key := range []string{"CI", "CONTINUOUS_INTEGRATION", "GITHUB_ACTIONS", "GITLAB_CI", "BUILDKITE", "JENKINS_URL", "TEAMCITY_VERSION"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return false
}

// onboardingAnswersFromEnv reads pre-supplied answers for a headless rollout.
//
// Bad values are reported and ignored rather than treated as an error: this path runs
// inside fleet deployments, where failing an install over a typo in an MDM variable
// would be far worse than losing one attribution row.
func onboardingAnswersFromEnv(stderr io.Writer) (string, string, bool) {
	rawEmail := strings.TrimSpace(os.Getenv(onboardingEnvEmail))
	rawUsage := strings.TrimSpace(os.Getenv(onboardingEnvUsage))
	if rawEmail == "" || rawUsage == "" {
		return "", "", false
	}
	if !onboardingEnabledByEnv() {
		return "", "", false
	}
	email, err := onboarding.NormalizeEmail(rawEmail)
	if err != nil {
		fmt.Fprintf(stderr, "beacon: ignoring %s: %v\n", onboardingEnvEmail, err)
		return "", "", false
	}
	usage, ok := onboarding.NormalizeUsage(rawUsage)
	if !ok {
		fmt.Fprintf(stderr, "beacon: ignoring %s=%q: expected one of work, personal, evaluating\n", onboardingEnvUsage, rawUsage)
		return "", "", false
	}
	return email, usage, true
}

// completeOnboarding submits the answers and records the outcome.
//
// It never returns an error. Once the user has answered, the install belongs to them;
// a signup endpoint being unreachable is our problem, not theirs.
//
// destination is the telemetry destination to record alongside the answers: local or
// own_infra, or "" when the question was not asked or the answer was Asymptote (stored
// by the install after the connect succeeds). It is never sent anywhere.
func completeOnboarding(cmd *cobra.Command, profile *onboarding.Profile, email, usage string, probe *onboarding.RuntimeProbe, destination string) {
	installID, err := onboarding.EnsureInstallID(profile)
	if err != nil {
		// Without an install ID there is no dedupe key, so there is nothing sensible
		// to send. Record the answer locally so we still never ask twice.
		installID = ""
	}

	mode := "user"
	if !endpointUserMode() {
		mode = "system"
	}
	submission := onboarding.NewSubmission(
		installID,
		email,
		usage,
		version.GetVersion(),
		mode,
		probe.Wait(runtimeProbeBudget),
	)

	ctx, cancel := context.WithTimeout(context.Background(), submitBudget)
	defer cancel()
	outcome, sendErr := onboardingSend(ctx, submission)

	profile.Onboarding = onboarding.Onboarding{
		CompletedAt:   time.Now().UTC().Format(time.RFC3339),
		Outcome:       outcome,
		Email:         email,
		Usage:         usage,
		BeaconVersion: version.GetVersion(),
	}
	profile.Onboarding.Destination = destination
	// Keep the payload only when resending could still work. A rejected submission is
	// terminal, and holding the address on disk past that point serves nobody.
	if outcome == onboarding.OutcomePending {
		profile.Pending = &submission
	} else {
		profile.Pending = nil
	}

	if err := onboardingSave(*profile); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "beacon: could not record onboarding: %v\n", err)
	}
	if sendErr != nil && outcome == onboarding.OutcomeRejected {
		fmt.Fprintf(cmd.ErrOrStderr(), "beacon: signup was not accepted (%v); continuing.\n", sendErr)
	}
}

// retryPendingOnboarding resends a queued signup without ever prompting.
//
// Called from `endpoint repair`, which is a maintenance command and must never ask a
// question -- including of someone who has not been through onboarding at all. It only
// gives a submission that failed on a flaky network a second chance, which is what the
// docs promise.
func retryPendingOnboarding() {
	profile := onboardingLoad()
	if !profile.Prompted() || profile.Pending == nil {
		return
	}
	resendPendingOnboarding(&profile)
}

// resendPendingOnboarding retries a submission that previously failed. Best effort
// and silent: the user already answered and must not be bothered about it again.
func resendPendingOnboarding(profile *onboarding.Profile) {
	if profile.Pending == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), submitBudget)
	defer cancel()

	outcome, _ := onboardingSend(ctx, *profile.Pending)
	if outcome == onboarding.OutcomePending {
		return
	}
	profile.Onboarding.Outcome = outcome
	profile.Pending = nil
	_ = onboardingSave(*profile)
}

var endpointOnboardingCmd = &cobra.Command{
	Use:          "onboarding",
	Short:        "Show or reset the one-time Beacon onboarding record",
	SilenceUsage: true,
	RunE:         runEndpointOnboarding,
}

type endpointOnboardingStatus struct {
	Prompted      bool   `json:"prompted"`
	CompletedAt   string `json:"completed_at,omitempty"`
	Outcome       string `json:"outcome,omitempty"`
	Email         string `json:"email,omitempty"`
	Usage         string `json:"usage,omitempty"`
	InstallID     string `json:"install_id,omitempty"`
	BeaconVersion string `json:"beacon_version,omitempty"`
	Destination   string `json:"destination,omitempty"`
	Pending       bool   `json:"pending_submission"`
	SkipReason    string `json:"skip_reason,omitempty"`
	ProfilePath   string `json:"profile_path"`
	Endpoint      string `json:"endpoint"`
}

func runEndpointOnboarding(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()

	if endpointOpts.onboardingReset {
		if err := os.Remove(onboarding.Path()); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Fprintf(out, "Onboarding record cleared: %s\n", onboarding.Path())
		return nil
	}

	profile := onboardingLoad()

	if endpointOpts.onboardingResend {
		if profile.Pending == nil {
			fmt.Fprintln(out, "No pending signup to resend.")
			return nil
		}
		resendPendingOnboarding(&profile)
		fmt.Fprintf(out, "Resend attempted. Outcome: %s\n", profile.Onboarding.Outcome)
		return nil
	}

	reason, _ := onboardingSkipReason(profile)
	status := endpointOnboardingStatus{
		Prompted:      profile.Prompted(),
		CompletedAt:   profile.Onboarding.CompletedAt,
		Outcome:       profile.Onboarding.Outcome,
		Email:         profile.Onboarding.Email,
		Usage:         profile.Onboarding.Usage,
		InstallID:     profile.InstallID,
		BeaconVersion: profile.Onboarding.BeaconVersion,
		Destination:   profile.Onboarding.Destination,
		Pending:       profile.Pending != nil,
		SkipReason:    reason,
		ProfilePath:   onboarding.Path(),
		Endpoint:      onboarding.Endpoint(),
	}

	if endpointOpts.jsonOutput {
		return json.NewEncoder(out).Encode(status)
	}

	if !status.Prompted {
		fmt.Fprintln(out, "Onboarding: not yet completed")
		if status.SkipReason != "" {
			fmt.Fprintf(out, "Would skip because: %s\n", status.SkipReason)
		}
	} else {
		fmt.Fprintf(out, "Onboarding: completed %s (%s)\n", status.CompletedAt, status.Outcome)
		fmt.Fprintf(out, "Email: %s\n", status.Email)
		fmt.Fprintf(out, "Usage: %s\n", status.Usage)
	}
	if status.Destination != "" {
		fmt.Fprintf(out, "Telemetry destination: %s\n", destinationLabel(status.Destination))
	}
	fmt.Fprintf(out, "Install ID: %s\n", status.InstallID)
	fmt.Fprintf(out, "Profile: %s\n", status.ProfilePath)
	fmt.Fprintf(out, "Endpoint: %s\n", status.Endpoint)
	if status.Pending {
		fmt.Fprintln(out, "A signup is queued for resend. Retry with: beacon endpoint onboarding --resend")
	}
	return nil
}
