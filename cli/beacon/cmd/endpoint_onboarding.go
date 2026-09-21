package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/account"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/managedprivacy"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/onboarding"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
	"github.com/spf13/cobra"
)

// Environment overrides for onboarding.
const (
	// onboardingEnvEnabled set to a false-ish value skips the wizard entirely. There is
	// no equivalent CLI flag: this exists for unattended CI and MDM installs, not as a
	// convenient way for an interactive user to decline.
	onboardingEnvEnabled = "BEACON_ONBOARDING"
	// onboardingEnvEmail and onboardingEnvUsage let a headless rollout supply the
	// answers up front. An admin deploying by MDM often does want the attribution;
	// they just have no terminal to type it into.
	onboardingEnvEmail = "BEACON_ONBOARDING_EMAIL"
	onboardingEnvUsage = "BEACON_ONBOARDING_USAGE"
	// managedIngestEnvEnabled set to a false-ish value hides the Beacon Managed row
	// from the telemetry destination question; Local remains available. Explicit --connect
	// is unaffected: the
	// variable hides an offer, it does not override an operator's request.
	managedIngestEnvEnabled = "BEACON_MANAGED_INGEST"
)

// Reasons the wizard did not run. Surfaced by `endpoint onboarding --show` so support
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
	onboardingSend                     = onboarding.Submit
	onboardingStdin          io.Reader = os.Stdin
	onboardingIsTTY                    = defaultOnboardingIsTTY
	onboardingIsRoot                   = func() bool { return os.Geteuid() == 0 }
	onboardingRunWizard                = onboarding.RunWizard
	onboardingAccountInspect           = account.Inspect
	onboardingAccountLogin             = account.Login
	onboardingAccountSave              = account.Save
	onboardingClock                    = time.Now
	// destinationAskable reports whether the destination question makes sense here: an
	// endpoint already connected to Asymptote has answered it by doing.
	destinationAskable = defaultDestinationAskable
)

// runtimeProbeBudget survives for legacy unattended attribution submissions.
const runtimeProbeBudget = 2 * time.Second

// submitBudget bounds the whole submission so a wedged endpoint cannot stall an
// install behind it.
const submitBudget = 6 * time.Second

// defaultOnboardingIsTTY reports whether this install can show a full-screen wizard.
//
// TERM=dumb is excluded alongside the isatty checks because a dumb terminal cannot
// render the alternate screen the wizard draws into; the trace browser already made
// that distinction and onboarding needs the same one. Both land in the existing
// not_a_terminal skip, which is silent by contract.
func defaultOnboardingIsTTY() bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb") {
		return false
	}
	return isTerminal(os.Stdin) && isTerminal(os.Stdout)
}

// onboardingOutcome is what interactive onboarding decided, for the installer to
// act on once the local install has actually succeeded.
//
// Every field is the zero value on a gated path, so a package postinstall, an MDM
// run, CI, a dry run and a redirected stdin all return an outcome that asks the
// installer for nothing.
type onboardingOutcome struct {
	// Connect is true when the wizard confirmed Beacon Managed on an endpoint that
	// is not already enrolled, so the installer should connect it after installing.
	// An already-connected endpoint is deliberately excluded: enrollment rotates the
	// device key, and rotating a working forwarder's key to re-learn a destination
	// Beacon already knows would be a regression, not an improvement.
	Connect bool
	// Persist records the wizard's answers, and runs only after lifecycle.Install
	// returns. Writing the profile first meant a failed install still left the
	// machine marked onboarded, so the retry took the already-completed branch, said
	// nothing, and never offered the destination again.
	Persist func() error
}

// maybeRunOnboarding runs the one-time account and destination wizard when this
// install is interactive.
//
// It is called from `endpoint install` after the --dry-run early return, so a dry run
// never prompts. An error returned here does stop the install: the prompt is a
// required step on an interactive terminal, and every refusal path names the opt-out.
func maybeRunOnboarding(cmd *cobra.Command) (onboardingOutcome, error) {
	profile := onboardingLoad()

	// Legacy pending attribution remains retryable, but new interactive installs use
	// the signed-in account and never create another email/usage submission.
	if profile.Prompted() {
		resendPendingOnboarding(&profile)
		if profile.Onboarding.Destination != "" {
			return onboardingOutcome{}, nil
		}
		if !onboardingEnabledByEnv() {
			return onboardingOutcome{}, nil
		}
		if _, skipped := destinationSkipReason(profile); skipped {
			return onboardingOutcome{}, nil
		}
		return runAccountOnboarding(cmd, &profile, true)
	}

	// Preserve the explicit unattended attribution contract for MDM rollouts. It is
	// the only path that still uses the legacy submission endpoint.
	if email, usage, ok := onboardingAnswersFromEnv(cmd.ErrOrStderr()); ok {
		completeOnboarding(cmd, &profile, email, usage, nil, "")
		return onboardingOutcome{}, nil
	}

	if _, skipped := onboardingSkipReason(profile); skipped {
		return onboardingOutcome{}, nil
	}
	return runAccountOnboarding(cmd, &profile, false)
}

func runAccountOnboarding(cmd *cobra.Command, profile *onboarding.Profile, destinationOnly bool) (onboardingOutcome, error) {
	now := onboardingClock()
	status := onboardingAccountInspect(now)
	preset := ""
	switch {
	case endpointOpts.connect:
		preset = onboarding.DestinationAsymptote
	case !destinationAskable():
		// A connected endpoint has already selected managed forwarding, but an
		// upgraded installation may still need the new account sign-in.
		preset = onboarding.DestinationAsymptote
	}
	options := onboarding.WizardOptions{
		SignedIn:          status.SignedIn && !status.Expired,
		Email:             status.User.Email,
		OfferManaged:      managedIngestEnabledByEnv(),
		DestinationOnly:   destinationOnly,
		PresetDestination: preset,
		PresetPrivacyMode: profile.Onboarding.PrivacyMode,
		NoBrowser:         endpointOpts.noBrowser,
		Now:               onboardingClock,
		SignInTimeout:     account.LoginWait,
		// Signing in happens inside the wizard so the full-screen UI is never torn
		// down around it. The wizard itself stays free of network code: this is the
		// only place onboarding reaches beacon.sh, and it hands back an email, never
		// a token.
		SignIn: func(ctx context.Context, req onboarding.SignInRequest, reporter onboarding.Reporter) (onboarding.Account, error) {
			session, err := onboardingAccountLogin(ctx, account.LoginOptions{
				Version:   version.GetVersion(),
				NoBrowser: req.NoBrowser,
				Timeout:   account.LoginWait,
				Now:       onboardingClock,
				OnPrompt: func(prompt account.LoginPrompt) {
					reporter.Prompt(onboarding.SignInPrompt{
						URL:        prompt.URL,
						WillOpen:   prompt.WillOpen,
						BrowserErr: prompt.BrowserErr,
					})
				},
			})
			if err != nil {
				return onboarding.Account{}, err
			}
			if err := onboardingAccountSave(*session); err != nil {
				return onboarding.Account{}, fmt.Errorf("store Beacon session: %w", err)
			}
			return onboarding.Account{Email: session.User.Email}, nil
		},
	}
	result, err := onboardingRunWizard(onboardingStdin, cmd.OutOrStdout(), options)
	if err != nil {
		return onboardingOutcome{}, err
	}
	if result.NeedLogin {
		session, err := onboardingAccountLogin(commandContext(cmd), account.LoginOptions{
			Version: version.GetVersion(),
			Out:     cmd.OutOrStdout(),
			Now:     onboardingClock,
		})
		if err != nil {
			return onboardingOutcome{}, fmt.Errorf("Beacon sign-in is required for interactive setup: %w", err)
		}
		if err := onboardingAccountSave(*session); err != nil {
			return onboardingOutcome{}, fmt.Errorf("store Beacon session: %w", err)
		}
		status = account.Status{SignedIn: true, User: session.User}
		options.SignedIn = true
		options.Email = status.User.Email
		options.DestinationOnly = true
		result, err = onboardingRunWizard(onboardingStdin, cmd.OutOrStdout(), options)
		if err != nil {
			return onboardingOutcome{}, err
		}
	}
	if !result.Completed {
		return onboardingOutcome{}, onboarding.ErrWizardCancelled
	}
	if result.SignedInEmail != "" {
		status = account.Status{SignedIn: true, User: account.User{Email: result.SignedInEmail}}
	}

	privacyMode := ""
	if result.Destination == onboarding.DestinationAsymptote {
		privacyMode, err = managedprivacy.Normalize(result.PrivacyMode)
		if err != nil {
			return onboardingOutcome{}, err
		}
	}

	// Confirming Managed connects this endpoint, unless it already is one. An
	// endpoint that is already enrolled keeps its device key and simply records the
	// destination it has been using.
	alreadyConnected := result.Destination == onboarding.DestinationAsymptote && !destinationAskable()
	connectAfterInstall := result.Destination == onboarding.DestinationAsymptote && !alreadyConnected

	recordedDestination := result.Destination
	if connectAfterInstall {
		// Managed is recorded only once device enrollment succeeds, so a failed
		// connect leaves the question open and the next install re-offers it. That
		// is the mechanism `--connect` has always used; auto-connect inherits it.
		recordedDestination = ""
	}

	outcome := onboardingOutcome{Connect: connectAfterInstall}
	email := status.User.Email
	outcome.Persist = func() error {
		if profile.Onboarding.CompletedAt == "" {
			profile.Onboarding = onboarding.Onboarding{
				CompletedAt:   onboardingClock().UTC().Format(time.RFC3339),
				Outcome:       onboarding.OutcomeAuthenticated,
				Email:         email,
				BeaconVersion: version.GetVersion(),
				Destination:   recordedDestination,
				PrivacyMode:   privacyMode,
			}
			profile.Pending = nil
		} else {
			profile.Onboarding.Destination = recordedDestination
			profile.Onboarding.PrivacyMode = privacyMode
		}
		if _, err := onboarding.EnsureInstallID(profile); err != nil {
			return fmt.Errorf("create onboarding install id: %w", err)
		}
		if err := onboardingSave(*profile); err != nil {
			return fmt.Errorf("record onboarding: %w", err)
		}
		return nil
	}

	switch {
	case connectAfterInstall:
		// The connect output that follows says where events now go; anticipating it
		// here would only be wrong if enrollment then failed.
	case result.Destination == onboarding.DestinationAsymptote:
		fmt.Fprintf(cmd.OutOrStdout(), "Beacon Managed with %s privacy. This endpoint is already connected.\n", managedprivacy.Label(privacyMode))
	case result.Destination == onboarding.DestinationLocal:
		fmt.Fprintln(cmd.OutOrStdout(), "Local-only telemetry selected. Open it with `beacon traces`.")
	}
	return outcome, nil
}

// recordDestinationAsymptote stores the managed answer after explicit
// `endpoint install --connect` succeeds. A normal wizard selection records intent
// immediately and leaves connect as a separate command.
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
		return "Beacon Managed"
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
	PrivacyMode   string `json:"privacy_mode,omitempty"`
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
		PrivacyMode:   profile.Onboarding.PrivacyMode,
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
		if status.Email != "" {
			fmt.Fprintf(out, "Email: %s\n", status.Email)
		}
		if status.Usage != "" {
			fmt.Fprintf(out, "Usage: %s\n", status.Usage)
		}
	}
	if status.Destination != "" {
		fmt.Fprintf(out, "Telemetry destination: %s\n", destinationLabel(status.Destination))
	}
	if status.PrivacyMode != "" {
		fmt.Fprintf(out, "Managed privacy: %s\n", managedprivacy.Label(status.PrivacyMode))
	}
	fmt.Fprintf(out, "Install ID: %s\n", status.InstallID)
	fmt.Fprintf(out, "Profile: %s\n", status.ProfilePath)
	if status.Outcome != onboarding.OutcomeAuthenticated {
		fmt.Fprintf(out, "Legacy signup endpoint: %s\n", status.Endpoint)
	}
	if status.Pending {
		fmt.Fprintln(out, "A signup is queued for resend. Retry with: beacon endpoint onboarding --resend")
	}
	return nil
}
