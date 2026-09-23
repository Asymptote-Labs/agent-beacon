package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/account"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/onboarding"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/spf13/cobra"
)

// onboardingHarness isolates the package-level state the onboarding gate reads, so a
// test can describe exactly one situation without inheriting another's leftovers.
type onboardingHarness struct {
	t       *testing.T
	cmd     *cobra.Command
	stdout  *bytes.Buffer
	stderr  *bytes.Buffer
	asked   bool
	sent    []onboarding.Submission
	saved   []onboarding.Profile
	loaded  onboarding.Profile
	answers onboarding.Answers
	askErr  error
	outcome string
	sendErr error
	// offered records the PromptOptions the prompt was invoked with; askable and
	// standaloneAnswer drive the destination question.
	offered           []onboarding.PromptOptions
	askable           bool
	standaloneAsked   bool
	standaloneOffered bool
	standaloneAnswer  string
	accountStatus     account.Status
	loginRuns         int
	loginErr          error
	wizardPrivacyMode string
}

func newOnboardingHarness(t *testing.T) *onboardingHarness {
	t.Helper()

	h := &onboardingHarness{
		t:       t,
		stdout:  &bytes.Buffer{},
		stderr:  &bytes.Buffer{},
		answers: onboarding.Answers{Email: "shukan@asymptotelabs.ai", Usage: onboarding.UsageWork},
		outcome: onboarding.OutcomeSubmitted,
		askable: true,
		accountStatus: account.Status{
			SignedIn: true,
			User:     account.User{ID: "usr_1", Email: "shukan@asymptotelabs.ai"},
		},
	}
	h.cmd = &cobra.Command{}
	h.cmd.SetOut(h.stdout)
	h.cmd.SetErr(h.stderr)

	// An interactive per-user install with nothing opted out: the one situation in
	// which the prompt is supposed to appear. Each test moves one thing off this base.
	prevOpts := endpointOpts
	endpointOpts.userMode = true
	endpointOpts.systemMode = false
	endpointOpts.jsonOutput = false
	endpointOpts.onboardingReset = false
	endpointOpts.onboardingResend = false

	for _, key := range []string{"CI", "CONTINUOUS_INTEGRATION", "GITHUB_ACTIONS", "GITLAB_CI", "BUILDKITE", "JENKINS_URL", "TEAMCITY_VERSION"} {
		t.Setenv(key, "")
	}
	t.Setenv(onboardingEnvEnabled, "")
	t.Setenv(onboardingEnvEmail, "")
	t.Setenv(onboardingEnvUsage, "")
	testenv.SetHome(t, t.TempDir())

	prevLoad, prevSave, prevSend := onboardingLoad, onboardingSave, onboardingSend
	prevTTY, prevRoot := onboardingIsTTY, onboardingIsRoot
	prevAskable := destinationAskable
	prevWizard := onboardingRunWizard
	prevAccountInspect, prevAccountLogin, prevAccountSave := onboardingAccountInspect, onboardingAccountLogin, onboardingAccountSave
	prevClock := onboardingClock
	t.Setenv(managedIngestEnvEnabled, "")

	onboardingLoad = func() onboarding.Profile { return h.loaded }
	onboardingSave = func(p onboarding.Profile) error {
		h.saved = append(h.saved, p)
		return nil
	}
	destinationAskable = func() bool { return h.askable }
	onboardingRunWizard = func(_ io.Reader, _ io.Writer, opts onboarding.WizardOptions) (onboarding.WizardResult, error) {
		if h.askErr != nil {
			return onboarding.WizardResult{}, h.askErr
		}
		destination := opts.PresetDestination
		if opts.DestinationOnly {
			h.standaloneAsked = true
			h.standaloneOffered = opts.OfferManaged
			if destination == "" {
				destination = h.standaloneAnswer
			}
		} else {
			h.asked = true
			h.offered = append(h.offered, onboarding.PromptOptions{
				AskDestination: destinationAskable() && !endpointOpts.connect,
				OfferAsymptote: opts.OfferManaged,
			})
			if destination == "" && h.answers.DestinationAsked {
				destination = h.answers.Destination
			}
		}
		if !opts.SignedIn {
			return onboarding.WizardResult{NeedLogin: true}, nil
		}
		if destination == "" {
			destination = onboarding.DestinationLocal
			if opts.OfferManaged {
				destination = onboarding.DestinationAsymptote
			}
		}
		return onboarding.WizardResult{Completed: true, Destination: destination, PrivacyMode: h.wizardPrivacyMode}, nil
	}
	onboardingAccountInspect = func(time.Time) account.Status { return h.accountStatus }
	onboardingAccountLogin = func(context.Context, account.LoginOptions) (*account.Session, error) {
		h.loginRuns++
		if h.loginErr != nil {
			return nil, h.loginErr
		}
		return &account.Session{
			BaseURL:     account.DefaultBaseURL,
			AccessToken: "token",
			User:        account.User{ID: "usr_1", Email: "shukan@asymptotelabs.ai"},
		}, nil
	}
	onboardingAccountSave = func(account.Session) error { return nil }
	onboardingClock = func() time.Time { return time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC) }
	onboardingSend = func(_ context.Context, s onboarding.Submission) (string, error) {
		h.sent = append(h.sent, s)
		return h.outcome, h.sendErr
	}
	onboardingIsTTY = func() bool { return true }
	onboardingIsRoot = func() bool { return false }
	t.Cleanup(func() {
		endpointOpts = prevOpts
		onboardingLoad, onboardingSave, onboardingSend = prevLoad, prevSave, prevSend
		onboardingIsTTY, onboardingIsRoot = prevTTY, prevRoot
		destinationAskable = prevAskable
		onboardingRunWizard = prevWizard
		onboardingAccountInspect, onboardingAccountLogin, onboardingAccountSave = prevAccountInspect, prevAccountLogin, prevAccountSave
		onboardingClock = prevClock
	})
	return h
}

// run is runOnboarding bound to this harness's command.
func (h *onboardingHarness) run(t *testing.T) (bool, error) {
	t.Helper()
	return runOnboarding(t, h.cmd)
}

// runOnboarding runs onboarding and then performs the persistence step the
// installer performs once lifecycle.Install has succeeded. Onboarding itself no
// longer writes the profile: a record written before the install meant a failed
// install still looked onboarded.
func runOnboarding(t *testing.T, cmd *cobra.Command) (bool, error) {
	t.Helper()
	outcome, err := maybeRunOnboarding(cmd)
	if err != nil {
		return false, err
	}
	if outcome.Persist != nil {
		if err := outcome.Persist(); err != nil {
			return outcome.Connect, err
		}
	}
	return outcome.Connect, nil
}

func TestMaybeRunOnboardingPromptsOnInteractiveInstall(t *testing.T) {
	h := newOnboardingHarness(t)

	if _, err := runOnboarding(t, h.cmd); err != nil {
		t.Fatalf("maybeRunOnboarding returned error: %v", err)
	}
	if !h.asked {
		t.Fatalf("expected the wizard to run on an interactive per-user install")
	}
	if len(h.sent) != 0 {
		t.Fatalf("interactive account onboarding sent %d legacy submissions", len(h.sent))
	}
	if len(h.saved) != 1 || !h.saved[0].Prompted() {
		t.Fatalf("saved = %+v, want one completed profile", h.saved)
	}
	if h.saved[0].Onboarding.Outcome != onboarding.OutcomeAuthenticated ||
		h.saved[0].Onboarding.Email != "shukan@asymptotelabs.ai" {
		t.Fatalf("saved account onboarding = %+v", h.saved[0].Onboarding)
	}
	// Managed is the preselected answer, and confirming it now connects the endpoint
	// rather than printing a command for the user to run next. The destination stays
	// unrecorded until that enrollment succeeds.
	if h.saved[0].Onboarding.Destination != "" {
		t.Fatalf("destination must not be recorded before enrollment succeeds: %q", h.saved[0].Onboarding.Destination)
	}
	if strings.Contains(h.stdout.String(), "beacon endpoint connect") {
		t.Fatalf("the wizard should not tell the user to run connect when it is about to: %s", h.stdout.String())
	}
	if h.saved[0].InstallID == "" {
		t.Fatalf("saved profile has no install ID")
	}
}

// Every one of these is a real deployment path. A prompt in any of them hangs an
// install that no human is watching.
func TestMaybeRunOnboardingStaysSilentWhenGated(t *testing.T) {
	cases := []struct {
		name       string
		arrange    func(t *testing.T, h *onboardingHarness)
		wantReason string
	}{
		{
			name: "already completed",
			arrange: func(_ *testing.T, h *onboardingHarness) {
				h.loaded = onboarding.Profile{Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-07T00:00:00Z", Destination: onboarding.DestinationLocal}}
			},
			wantReason: onboardingSkipCompleted,
		},
		{
			name:       "BEACON_ONBOARDING=0",
			arrange:    func(t *testing.T, _ *onboardingHarness) { t.Setenv(onboardingEnvEnabled, "0") },
			wantReason: onboardingSkipOptedOut,
		},
		{
			name:       "BEACON_ONBOARDING=false",
			arrange:    func(t *testing.T, _ *onboardingHarness) { t.Setenv(onboardingEnvEnabled, "false") },
			wantReason: onboardingSkipOptedOut,
		},
		{
			name:       "system install",
			arrange:    func(_ *testing.T, _ *onboardingHarness) { endpointOpts.systemMode = true },
			wantReason: onboardingSkipSystemInstall,
		},
		{
			name:       "running as root",
			arrange:    func(_ *testing.T, _ *onboardingHarness) { onboardingIsRoot = func() bool { return true } },
			wantReason: onboardingSkipSystemInstall,
		},
		{
			name:       "CI",
			arrange:    func(t *testing.T, _ *onboardingHarness) { t.Setenv("CI", "true") },
			wantReason: onboardingSkipCI,
		},
		{
			name:       "GitHub Actions",
			arrange:    func(t *testing.T, _ *onboardingHarness) { t.Setenv("GITHUB_ACTIONS", "true") },
			wantReason: onboardingSkipCI,
		},
		{
			name:       "piped stdin",
			arrange:    func(_ *testing.T, _ *onboardingHarness) { onboardingIsTTY = func() bool { return false } },
			wantReason: onboardingSkipNotAterminal,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOnboardingHarness(t)
			tc.arrange(t, h)

			reason, skipped := onboardingSkipReason(h.loaded)
			if !skipped {
				t.Fatalf("onboardingSkipReason() reported the prompt should run")
			}
			if reason != tc.wantReason {
				t.Fatalf("skip reason = %q, want %q", reason, tc.wantReason)
			}

			if _, err := runOnboarding(t, h.cmd); err != nil {
				t.Fatalf("maybeRunOnboarding returned error: %v", err)
			}
			if h.asked {
				t.Fatalf("the prompt ran despite %s", tc.name)
			}
			if len(h.sent) != 0 {
				t.Fatalf("submitted %d payloads while gated, want 0", len(h.sent))
			}
			if h.stdout.Len() != 0 {
				t.Fatalf("gated onboarding wrote to stdout: %q", h.stdout.String())
			}
		})
	}
}

// A user who cannot get past the prompt stops the install.
func TestMaybeRunOnboardingPropagatesPromptFailure(t *testing.T) {
	h := newOnboardingHarness(t)
	h.askErr = onboarding.ErrTooManyAttempts

	_, err := runOnboarding(t, h.cmd)
	if err == nil {
		t.Fatalf("maybeRunOnboarding returned nil, want the prompt failure to stop the install")
	}
	if strings.Contains(err.Error(), "BEACON_ONBOARDING") {
		t.Fatalf("error %q leaks the unattended-install escape hatch", err)
	}
	if len(h.saved) != 0 {
		t.Fatalf("saved a profile for an unanswered prompt: %+v", h.saved)
	}
}

func TestInteractiveOnboardingSignsInWithoutLegacySubmission(t *testing.T) {
	h := newOnboardingHarness(t)
	h.accountStatus = account.Status{}

	if _, err := runOnboarding(t, h.cmd); err != nil {
		t.Fatalf("maybeRunOnboarding returned error: %v", err)
	}
	if h.loginRuns != 1 {
		t.Fatalf("login runs = %d, want 1", h.loginRuns)
	}
	if len(h.sent) != 0 {
		t.Fatalf("interactive onboarding sent legacy payloads: %+v", h.sent)
	}
	if len(h.saved) != 1 || h.saved[0].Onboarding.Outcome != onboarding.OutcomeAuthenticated {
		t.Fatalf("saved = %+v", h.saved)
	}
}

func TestInteractiveOnboardingStopsWhenRequiredLoginFails(t *testing.T) {
	h := newOnboardingHarness(t)
	h.accountStatus = account.Status{}
	h.loginErr = errors.New("authentication service unavailable")

	_, err := runOnboarding(t, h.cmd)
	if err == nil || !strings.Contains(err.Error(), "sign-in is required") {
		t.Fatalf("error = %v", err)
	}
	if len(h.saved) != 0 || len(h.sent) != 0 {
		t.Fatalf("failed login persisted onboarding: saved=%+v sent=%+v", h.saved, h.sent)
	}
}

func TestMaybeRunOnboardingResendsPendingSubmission(t *testing.T) {
	h := newOnboardingHarness(t)
	pending := onboarding.Submission{InstallID: "abc123", Email: "shukan@asymptotelabs.ai"}
	h.loaded = onboarding.Profile{
		InstallID:  "abc123",
		Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-07T00:00:00Z", Outcome: onboarding.OutcomePending, Destination: onboarding.DestinationLocal},
		Pending:    &pending,
	}

	if _, err := runOnboarding(t, h.cmd); err != nil {
		t.Fatalf("maybeRunOnboarding returned error: %v", err)
	}
	if h.asked {
		t.Fatalf("re-prompted a user who already answered")
	}
	if len(h.sent) != 1 || h.sent[0].InstallID != "abc123" {
		t.Fatalf("sent = %+v, want the pending payload resent once", h.sent)
	}
	if len(h.saved) != 1 || h.saved[0].Pending != nil {
		t.Fatalf("saved = %+v, want the pending payload cleared after success", h.saved)
	}
}

func TestMaybeRunOnboardingKeepsPendingWhenResendFails(t *testing.T) {
	h := newOnboardingHarness(t)
	h.outcome = onboarding.OutcomePending
	pending := onboarding.Submission{InstallID: "abc123"}
	h.loaded = onboarding.Profile{
		Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-07T00:00:00Z", Outcome: onboarding.OutcomePending, Destination: onboarding.DestinationLocal},
		Pending:    &pending,
	}

	if _, err := runOnboarding(t, h.cmd); err != nil {
		t.Fatalf("maybeRunOnboarding returned error: %v", err)
	}
	if len(h.saved) != 0 {
		t.Fatalf("rewrote the profile on a failed resend: %+v", h.saved)
	}
}

// An MDM rollout has no terminal but often does want the attribution.
func TestMaybeRunOnboardingAcceptsEnvironmentAnswersHeadless(t *testing.T) {
	h := newOnboardingHarness(t)
	onboardingIsTTY = func() bool { return false }
	t.Setenv(onboardingEnvEmail, "  Ops@AsymptoteLabs.AI ")
	t.Setenv(onboardingEnvUsage, "work")

	if _, err := runOnboarding(t, h.cmd); err != nil {
		t.Fatalf("maybeRunOnboarding returned error: %v", err)
	}
	if h.asked {
		t.Fatalf("prompted despite pre-supplied answers")
	}
	if len(h.sent) != 1 {
		t.Fatalf("submissions = %d, want 1", len(h.sent))
	}
	if h.sent[0].Email != "ops@asymptotelabs.ai" {
		t.Fatalf("email = %q, want it normalized", h.sent[0].Email)
	}
}

// A typo in an MDM variable must not fail a fleet install.
func TestMaybeRunOnboardingIgnoresInvalidEnvironmentAnswers(t *testing.T) {
	h := newOnboardingHarness(t)
	onboardingIsTTY = func() bool { return false }
	t.Setenv(onboardingEnvEmail, "not-an-email")
	t.Setenv(onboardingEnvUsage, "work")

	if _, err := runOnboarding(t, h.cmd); err != nil {
		t.Fatalf("maybeRunOnboarding returned error: %v", err)
	}
	if len(h.sent) != 0 || h.asked {
		t.Fatalf("acted on an invalid environment answer")
	}
	if !strings.Contains(h.stderr.String(), onboardingEnvEmail) {
		t.Fatalf("stderr = %q, want it to name the ignored variable", h.stderr.String())
	}
}

func TestMaybeRunOnboardingEnvironmentAnswersRespectOptOut(t *testing.T) {
	h := newOnboardingHarness(t)
	t.Setenv(onboardingEnvEnabled, "0")
	t.Setenv(onboardingEnvEmail, "ops@asymptotelabs.ai")
	t.Setenv(onboardingEnvUsage, "work")

	if _, err := runOnboarding(t, h.cmd); err != nil {
		t.Fatalf("maybeRunOnboarding returned error: %v", err)
	}
	if len(h.sent) != 0 {
		t.Fatalf("submitted despite BEACON_ONBOARDING=0")
	}
}

func TestPreviouslyOnboardedDestinationUpgradeRespectsOptOut(t *testing.T) {
	h := newOnboardingHarness(t)
	h.loaded = onboarding.Profile{
		InstallID:  "legacy",
		Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-01T00:00:00Z", Outcome: onboarding.OutcomeSubmitted},
	}
	t.Setenv(onboardingEnvEnabled, "0")

	if connect, err := runOnboarding(t, h.cmd); err != nil || connect {
		t.Fatalf("connect=%t err=%v", connect, err)
	}
	if h.asked || h.standaloneAsked || h.loginRuns != 0 || len(h.saved) != 0 {
		t.Fatalf("opted-out upgrade prompted or persisted: asked=%t standalone=%t login=%d saved=%+v", h.asked, h.standaloneAsked, h.loginRuns, h.saved)
	}
}

// The opt-out is an environment variable only. Shipping a CLI flag would make
// declining a single keystroke, which is not what this prompt is for.
func TestInstallHasNoOnboardingOptOutFlag(t *testing.T) {
	if flag := endpointInstallCmd.Flags().Lookup("no-onboarding"); flag != nil {
		t.Fatalf("endpoint install still registers --no-onboarding")
	}
}

func TestEndpointOnboardingShowsRecord(t *testing.T) {
	h := newOnboardingHarness(t)
	h.loaded = onboarding.Profile{
		InstallID: "abc123",
		Onboarding: onboarding.Onboarding{
			CompletedAt: "2026-08-07T00:00:00Z",
			Outcome:     onboarding.OutcomeSubmitted,
			Email:       "shukan@asymptotelabs.ai",
			Usage:       onboarding.UsageWork,
		},
	}

	if err := runEndpointOnboarding(h.cmd, nil); err != nil {
		t.Fatalf("runEndpointOnboarding returned error: %v", err)
	}
	out := h.stdout.String()
	for _, want := range []string{"completed", "shukan@asymptotelabs.ai", "abc123"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output is missing %q:\n%s", want, out)
		}
	}
}

func TestEndpointOnboardingShowsAuthenticatedRecordWithoutLegacyFields(t *testing.T) {
	h := newOnboardingHarness(t)
	h.loaded = onboarding.Profile{
		InstallID: "abc123",
		Onboarding: onboarding.Onboarding{
			CompletedAt: "2026-09-21T08:00:00Z",
			Outcome:     onboarding.OutcomeAuthenticated,
			Email:       "person@example.com",
			Destination: onboarding.DestinationLocal,
		},
	}
	if err := runEndpointOnboarding(h.cmd, nil); err != nil {
		t.Fatal(err)
	}
	out := h.stdout.String()
	for _, want := range []string{"authenticated", "person@example.com", "local only"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"Usage:", "Legacy signup endpoint:"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("authenticated output contains %q:\n%s", unwanted, out)
		}
	}
}

func TestEndpointOnboardingResetRemovesProfile(t *testing.T) {
	h := newOnboardingHarness(t)
	endpointOpts.onboardingReset = true

	if err := onboarding.Save(onboarding.Profile{Onboarding: onboarding.Onboarding{CompletedAt: "x"}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := runEndpointOnboarding(h.cmd, nil); err != nil {
		t.Fatalf("runEndpointOnboarding returned error: %v", err)
	}
	if _, err := os.Stat(onboarding.Path()); !os.IsNotExist(err) {
		t.Fatalf("profile still exists after --reset (err = %v)", err)
	}
}

func TestEndpointOnboardingResetOnMissingProfileIsNotAnError(t *testing.T) {
	h := newOnboardingHarness(t)
	endpointOpts.onboardingReset = true

	if err := runEndpointOnboarding(h.cmd, nil); err != nil {
		t.Fatalf("runEndpointOnboarding returned error for a missing profile: %v", err)
	}
}

func TestEndpointOnboardingResendWithoutPendingIsQuiet(t *testing.T) {
	h := newOnboardingHarness(t)
	endpointOpts.onboardingResend = true

	if err := runEndpointOnboarding(h.cmd, nil); err != nil {
		t.Fatalf("runEndpointOnboarding returned error: %v", err)
	}
	if len(h.sent) != 0 {
		t.Fatalf("sent %d payloads with nothing pending", len(h.sent))
	}
	if !strings.Contains(h.stdout.String(), "No pending signup") {
		t.Fatalf("output = %q, want it to say there is nothing to resend", h.stdout.String())
	}
}

func TestEndpointOnboardingJSONOutput(t *testing.T) {
	h := newOnboardingHarness(t)
	endpointOpts.jsonOutput = true
	h.loaded = onboarding.Profile{
		InstallID:  "abc123",
		Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-07T00:00:00Z", Outcome: onboarding.OutcomeSubmitted},
	}

	if err := runEndpointOnboarding(h.cmd, nil); err != nil {
		t.Fatalf("runEndpointOnboarding returned error: %v", err)
	}
	out := h.stdout.String()
	if !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("output is not JSON:\n%s", out)
	}
	if !strings.Contains(out, `"prompted":true`) {
		t.Fatalf("JSON = %s, want prompted true", out)
	}
}

func TestOnboardingEnabledByEnv(t *testing.T) {
	cases := map[string]bool{
		"":      true,
		"1":     true,
		"true":  true,
		"yes":   true,
		"0":     false,
		"false": false,
		"FALSE": false,
		"no":    false,
		"off":   false,
		" 0 ":   false,
	}
	for value, want := range cases {
		t.Setenv(onboardingEnvEnabled, value)
		if got := onboardingEnabledByEnv(); got != want {
			t.Fatalf("onboardingEnabledByEnv() = %t for %q, want %t", got, value, want)
		}
	}
}

// Repair must resend a queued signup -- the docs promise it -- but must never prompt,
// including for someone who has not been through onboarding at all.
func TestRetryPendingOnboardingResendsWithoutPrompting(t *testing.T) {
	h := newOnboardingHarness(t)
	pending := onboarding.Submission{InstallID: "abc123", Email: "shukan@asymptotelabs.ai"}
	h.loaded = onboarding.Profile{
		InstallID:  "abc123",
		Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-07T00:00:00Z", Outcome: onboarding.OutcomePending, Destination: onboarding.DestinationLocal},
		Pending:    &pending,
	}

	retryPendingOnboarding()

	if h.asked {
		t.Fatalf("repair prompted for onboarding")
	}
	if len(h.sent) != 1 || h.sent[0].InstallID != "abc123" {
		t.Fatalf("sent = %+v, want the pending payload resent once", h.sent)
	}
	if len(h.saved) != 1 || h.saved[0].Pending != nil {
		t.Fatalf("saved = %+v, want the payload cleared after delivery", h.saved)
	}
}

func TestRetryPendingOnboardingIsSilentWithNothingQueued(t *testing.T) {
	for name, profile := range map[string]onboarding.Profile{
		"never onboarded":     {},
		"onboarded, no queue": {Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-07T00:00:00Z", Outcome: onboarding.OutcomeSubmitted}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newOnboardingHarness(t)
			h.loaded = profile

			retryPendingOnboarding()

			if h.asked {
				t.Fatalf("repair prompted for onboarding")
			}
			if len(h.sent) != 0 || len(h.saved) != 0 {
				t.Fatalf("repair touched the network or disk: sent=%d saved=%d", len(h.sent), len(h.saved))
			}
		})
	}
}

func TestMaybeRunOnboardingConnectsManagedAndRecordsLocal(t *testing.T) {
	// An endpoint that is already enrolled is not asked and is not re-enrolled:
	// enrollment rotates the device key, so reconnecting a working forwarder to
	// re-learn a destination Beacon already has would break it, not improve it.
	h := newOnboardingHarness(t)
	h.askable = false
	if connect, err := runOnboarding(t, h.cmd); err != nil || connect {
		t.Fatalf("an already-connected endpoint must not be re-enrolled: connect=%t err=%v", connect, err)
	}
	if len(h.offered) != 1 || h.offered[0].AskDestination {
		t.Fatalf("the destination question must not be asked on a connected endpoint: %+v", h.offered)
	}
	if got := h.saved[len(h.saved)-1].Onboarding.Destination; got != onboarding.DestinationAsymptote {
		t.Fatalf("connected endpoint destination = %q, want managed", got)
	}

	// Confirming Managed on a fresh endpoint connects it in the same command.
	h = newOnboardingHarness(t)
	h.askable = true
	h.answers.DestinationAsked = true
	h.answers.Destination = onboarding.DestinationAsymptote
	h.wizardPrivacyMode = "metadata-only"
	connect, err := runOnboarding(t, h.cmd)
	if err != nil || !connect {
		t.Fatalf("confirming managed should connect this endpoint: connect=%t err=%v", connect, err)
	}
	if !h.offered[0].AskDestination || !h.offered[0].OfferAsymptote {
		t.Fatalf("wizard should offer local and managed destinations: %+v", h.offered[0])
	}
	// The privacy answer is recorded now, because connect reads it back. The
	// destination is not, because enrollment has not happened yet.
	saved := h.saved[len(h.saved)-1].Onboarding
	if saved.Destination != "" {
		t.Fatalf("managed must not be recorded before enrollment succeeds, got %q", saved.Destination)
	}
	if saved.PrivacyMode != "metadata_only" {
		t.Fatalf("privacy mode must be recorded for connect to read: %q", saved.PrivacyMode)
	}

	h = newOnboardingHarness(t)
	h.askable = true
	h.answers.DestinationAsked = true
	h.answers.Destination = onboarding.DestinationLocal
	if connect, _ := runOnboarding(t, h.cmd); connect {
		t.Fatal("local selection must not connect")
	}
	if got := h.saved[len(h.saved)-1].Onboarding.Destination; got != onboarding.DestinationLocal {
		t.Fatalf("recorded destination = %q, want local", got)
	}
}

// BEACON_MANAGED_INGEST=0 hides the Asymptote row but the question is still asked.
func TestDestinationQuestionHidesAsymptoteWhenOptedOut(t *testing.T) {
	h := newOnboardingHarness(t)
	h.askable = true
	t.Setenv(managedIngestEnvEnabled, "0")
	h.answers.DestinationAsked = true
	h.answers.Destination = onboarding.DestinationLocal
	if connect, err := runOnboarding(t, h.cmd); err != nil || connect {
		t.Fatalf("connect=%t err=%v", connect, err)
	}
	if len(h.offered) != 1 || !h.offered[0].AskDestination || h.offered[0].OfferAsymptote {
		t.Fatalf("BEACON_MANAGED_INGEST=0 must hide only the Asymptote row: %+v", h.offered)
	}

	h = newOnboardingHarness(t)
	h.askable = true
	t.Setenv(managedIngestEnvEnabled, "0")
	h.loaded = onboarding.Profile{InstallID: "abc", Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-01T00:00:00Z"}}
	h.standaloneAnswer = onboarding.DestinationLocal
	if _, err := runOnboarding(t, h.cmd); err != nil || !h.standaloneAsked || h.standaloneOffered {
		t.Fatalf("standalone question should be asked without the Asymptote row: err=%v asked=%t offered=%t", err, h.standaloneAsked, h.standaloneOffered)
	}
	if got := h.saved[len(h.saved)-1].Onboarding.Destination; got != onboarding.DestinationLocal {
		t.Fatalf("destination = %q, want local", got)
	}
}

func TestDestinationAskedOnceToPreviouslyOnboardedMachine(t *testing.T) {
	h := newOnboardingHarness(t)
	h.askable = true
	h.loaded = onboarding.Profile{InstallID: "abc", Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-01T00:00:00Z", Outcome: onboarding.OutcomeSubmitted, Email: "shukan@asymptotelabs.ai", Usage: onboarding.UsageWork}}
	h.standaloneAnswer = onboarding.DestinationAsymptote
	connect, err := runOnboarding(t, h.cmd)
	if err != nil || !connect {
		t.Fatalf("answering managed should connect this endpoint: connect=%t err=%v", connect, err)
	}
	if h.asked {
		t.Fatal("the signup questions must not be asked again")
	}
	if !h.standaloneAsked || !h.standaloneOffered {
		t.Fatalf("the destination question should be asked once, with the Asymptote row: asked=%t offered=%t", h.standaloneAsked, h.standaloneOffered)
	}
	if len(h.saved) != 1 || h.saved[0].Onboarding.Destination != "" {
		t.Fatalf("managed must be recorded only after enrollment succeeds: %+v", h.saved)
	}

	// Local is recorded at once.
	h = newOnboardingHarness(t)
	h.askable = true
	h.loaded = onboarding.Profile{InstallID: "abc", Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-01T00:00:00Z"}}
	h.standaloneAnswer = onboarding.DestinationLocal
	if connect, _ := runOnboarding(t, h.cmd); connect || !h.standaloneAsked {
		t.Fatalf("connect=%t asked=%t", connect, h.standaloneAsked)
	}
	if got := h.saved[len(h.saved)-1].Onboarding.Destination; got != onboarding.DestinationLocal {
		t.Fatalf("recorded destination = %q, want local", got)
	}

	// Once recorded, it is never asked again.
	h = newOnboardingHarness(t)
	h.askable = true
	h.loaded = onboarding.Profile{InstallID: "abc", Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-01T00:00:00Z", Destination: onboarding.DestinationLocal}}
	if connect, _ := runOnboarding(t, h.cmd); connect || h.standaloneAsked {
		t.Fatalf("a recorded destination must not be asked again: connect=%t asked=%t", connect, h.standaloneAsked)
	}

	// Non-interactive paths never see it either.
	h = newOnboardingHarness(t)
	h.askable = true
	h.loaded = onboarding.Profile{InstallID: "abc", Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-01T00:00:00Z"}}
	onboardingIsTTY = func() bool { return false }
	if connect, _ := runOnboarding(t, h.cmd); connect || h.standaloneAsked {
		t.Fatal("no terminal, no question")
	}
	h = newOnboardingHarness(t)
	h.askable = true
	h.loaded = onboarding.Profile{InstallID: "abc", Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-01T00:00:00Z"}}
	t.Setenv("CI", "1")
	if connect, _ := runOnboarding(t, h.cmd); connect || h.standaloneAsked {
		t.Fatal("CI never sees the question")
	}
	// An upgraded endpoint that is already connected keeps the legacy skip
	// guarantee; reinstall must not introduce a new account dependency.
	h = newOnboardingHarness(t)
	h.askable = false
	h.loaded = onboarding.Profile{InstallID: "abc", Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-01T00:00:00Z"}}
	if connect, _ := runOnboarding(t, h.cmd); connect || h.standaloneAsked || h.loginRuns != 0 || len(h.saved) != 0 {
		t.Fatalf("connected reinstall prompted or persisted: connect=%t asked=%t login=%d saved=%+v", connect, h.standaloneAsked, h.loginRuns, h.saved)
	}
}

func TestManagedDestinationIsRecordedOnlyAfterConnectSucceeds(t *testing.T) {
	h := newOnboardingHarness(t)
	h.askable = true
	h.answers.DestinationAsked = true
	h.answers.Destination = onboarding.DestinationAsymptote
	h.wizardPrivacyMode = "metadata-only"
	if connect, err := runOnboarding(t, h.cmd); err != nil || !connect {
		t.Fatalf("connect=%t err=%v", connect, err)
	}
	afterPrompt := h.saved[len(h.saved)-1]
	if afterPrompt.Onboarding.Destination != "" {
		t.Fatalf("destination recorded before enrollment: %q", afterPrompt.Onboarding.Destination)
	}
	if afterPrompt.Onboarding.PrivacyMode != "metadata_only" {
		t.Fatalf("managed privacy = %q", afterPrompt.Onboarding.PrivacyMode)
	}

	// An enrollment that never succeeded leaves the destination open, so the next
	// install asks again and retries rather than silently doing nothing. This is the
	// retry path, and it is the reason the destination is written last.
	retry := newOnboardingHarness(t)
	retry.loaded = afterPrompt
	retry.askable = true
	retry.standaloneAnswer = onboarding.DestinationAsymptote
	if connect, err := retry.run(t); err != nil || !connect || !retry.standaloneAsked {
		t.Fatalf("an unconnected managed endpoint should re-offer and retry: connect=%t asked=%t err=%v", connect, retry.standaloneAsked, err)
	}

	// Once enrollment has succeeded, recordDestinationAsymptote fills the destination
	// in and the question is never asked again.
	connected := afterPrompt
	connected.Onboarding.Destination = onboarding.DestinationAsymptote
	h2 := newOnboardingHarness(t)
	h2.loaded = connected
	if connect, err := h2.run(t); err != nil || connect || h2.asked || h2.standaloneAsked {
		t.Fatalf("a recorded managed destination prompted again: connect=%t err=%v", connect, err)
	}
}

// --connect is already the answer: the question is not asked on either the first-run
// prompt or an already-onboarded machine, so Enter can never record "local" on a machine
// the same command then connects.
func TestInstallConnectFlagSkipsDestinationQuestion(t *testing.T) {
	h := newOnboardingHarness(t)
	h.askable = true
	endpointOpts.connect = true
	t.Cleanup(func() { endpointOpts.connect = false })
	if connect, err := runOnboarding(t, h.cmd); err != nil || !connect {
		t.Fatalf("connect=%t err=%v", connect, err)
	}
	if len(h.offered) != 1 || h.offered[0].AskDestination {
		t.Fatalf("--connect must not add the question to the first-run prompt: %+v", h.offered)
	}
	if got := h.saved[len(h.saved)-1].Onboarding.Destination; got != "" {
		t.Fatalf("recorded %q before the connect ran", got)
	}

	h = newOnboardingHarness(t)
	h.askable = true
	h.loaded = onboarding.Profile{InstallID: "abc", Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-01T00:00:00Z"}}
	if connect, err := runOnboarding(t, h.cmd); err != nil || !connect || !h.standaloneAsked {
		t.Fatalf("connect=%t err=%v asked=%t", connect, err, h.standaloneAsked)
	}

	// After the connect the install records the answer, exactly as for an asked question.
	recordDestinationAsymptote(h.cmd)
	if got := h.saved[len(h.saved)-1].Onboarding.Destination; got != onboarding.DestinationAsymptote {
		t.Fatalf("recorded destination = %q", got)
	}
}

func TestInstallHasConnectFlag(t *testing.T) {
	if endpointInstallCmd.Flags().Lookup("connect") == nil {
		t.Fatal("endpoint install should expose --connect")
	}
}

func TestEndpointOnboardingShowsDestination(t *testing.T) {
	for value, want := range map[string]string{
		onboarding.DestinationLocal:     "Telemetry destination: local only",
		onboarding.DestinationOwnInfra:  "Telemetry destination: own infrastructure",
		onboarding.DestinationAsymptote: "Telemetry destination: Beacon Managed",
	} {
		h := newOnboardingHarness(t)
		h.loaded = onboarding.Profile{InstallID: "abc", Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-01T00:00:00Z", Outcome: onboarding.OutcomeSubmitted, Email: "shukan@asymptotelabs.ai", Usage: onboarding.UsageWork, Destination: value}}
		if err := runEndpointOnboarding(h.cmd, nil); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(h.stdout.String(), want) {
			t.Fatalf("output = %s", h.stdout.String())
		}
	}
	h := newOnboardingHarness(t)
	h.loaded = onboarding.Profile{InstallID: "abc", Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-01T00:00:00Z", Outcome: onboarding.OutcomeSubmitted}}
	if err := runEndpointOnboarding(h.cmd, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.stdout.String(), "Telemetry destination") {
		t.Fatalf("no destination line before the question is answered:\n%s", h.stdout.String())
	}

	h = newOnboardingHarness(t)
	h.loaded = onboarding.Profile{InstallID: "abc", Onboarding: onboarding.Onboarding{
		CompletedAt: "2026-09-21T08:00:00Z",
		Outcome:     onboarding.OutcomeAuthenticated,
		Destination: onboarding.DestinationAsymptote,
		PrivacyMode: "metadata_only",
	}}
	if err := runEndpointOnboarding(h.cmd, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.stdout.String(), "Managed privacy: Metadata only") {
		t.Fatalf("privacy missing:\n%s", h.stdout.String())
	}
}

// Onboarding must persist nothing until the install it is onboarding has actually
// succeeded.
//
// The profile used to be written inside the wizard, before lifecycle.Install ran.
// An install that then failed left a record saying the machine was onboarded, so
// the retry took the already-completed branch, printed nothing, and never offered
// the destination again -- leaving the user with a recorded intent and no endpoint.
func TestOnboardingPersistsNothingUntilTheInstallSucceeds(t *testing.T) {
	h := newOnboardingHarness(t)
	h.askable = true

	outcome, err := maybeRunOnboarding(h.cmd)
	if err != nil {
		t.Fatalf("maybeRunOnboarding returned error: %v", err)
	}
	if !h.asked {
		t.Fatal("expected the wizard to run")
	}
	if outcome.Persist == nil {
		t.Fatal("a completed wizard must hand the installer a record to persist")
	}
	// runEndpointInstall calls Persist only after lifecycle.Install returns nil.
	// Stopping here is what a failed install looks like.
	if len(h.saved) != 0 {
		t.Fatalf("onboarding wrote a profile before the install succeeded: %+v", h.saved)
	}

	// Once the install succeeds, the record lands. (Done before the retry harness
	// below, which rebinds the package-level save hook to itself.)
	if err := outcome.Persist(); err != nil {
		t.Fatalf("persist after a successful install: %v", err)
	}
	if len(h.saved) != 1 || !h.saved[0].Prompted() {
		t.Fatalf("saved = %+v, want one completed profile once the install succeeded", h.saved)
	}

	// After a failed install nothing was persisted, so the next install reloads an
	// unprompted machine and asks again instead of silently skipping.
	retry := newOnboardingHarness(t)
	retry.askable = true
	retry.loaded = onboarding.Profile{}
	if _, err := maybeRunOnboarding(retry.cmd); err != nil {
		t.Fatalf("retry returned error: %v", err)
	}
	if !retry.asked {
		t.Fatal("a machine whose install failed must be asked again on the next install")
	}
}

// Finishing setup without an account must not record a leftover identity as the
// person who onboarded, nor claim the run was authenticated.
//
// account.Inspect returns the stored user even when the session is expired, so
// Email is populated while SignedIn is false.
func TestSkippingTheAccountRecordsNoIdentity(t *testing.T) {
	h := newOnboardingHarness(t)
	h.askable = true
	h.accountStatus = account.Status{
		SignedIn: true,
		Expired:  true,
		User:     account.User{ID: "usr_old", Email: "stale@example.com"},
	}
	onboardingRunWizard = func(_ io.Reader, _ io.Writer, _ onboarding.WizardOptions) (onboarding.WizardResult, error) {
		return onboarding.WizardResult{
			Completed:      true,
			WithoutAccount: true,
			Destination:    onboarding.DestinationLocal,
		}, nil
	}

	if _, err := runOnboarding(t, h.cmd); err != nil {
		t.Fatalf("runOnboarding returned error: %v", err)
	}
	if len(h.saved) != 1 {
		t.Fatalf("saved = %+v, want one profile", h.saved)
	}
	got := h.saved[0].Onboarding
	if got.Email != "" {
		t.Fatalf("recorded an identity the user did not sign in as: %q", got.Email)
	}
	if got.Outcome == onboarding.OutcomeAuthenticated {
		t.Fatalf("a run with no account must not be recorded as authenticated: %q", got.Outcome)
	}
	if got.Outcome != onboarding.OutcomeSkipped {
		t.Fatalf("outcome = %q, want skipped", got.Outcome)
	}
	if got.Destination != onboarding.DestinationLocal {
		t.Fatalf("destination = %q, want local", got.Destination)
	}
}
