package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/onboarding"
	"github.com/spf13/cobra"
)

// onboardingHarness isolates the package-level state the onboarding gate reads, so a
// test can describe exactly one situation without inheriting another's leftovers.
type onboardingHarness struct {
	t         *testing.T
	cmd       *cobra.Command
	stdout    *bytes.Buffer
	stderr    *bytes.Buffer
	asked     bool
	sent      []onboarding.Submission
	saved     []onboarding.Profile
	loaded    onboarding.Profile
	answers   onboarding.Answers
	askErr    error
	outcome   string
	sendErr   error
	probeRuns int
	// offered records the PromptOptions the prompt was invoked with; askable and
	// standaloneAnswer drive the destination question.
	offered           []onboarding.PromptOptions
	askable           bool
	standaloneAsked   bool
	standaloneOffered bool
	standaloneAnswer  string
}

func newOnboardingHarness(t *testing.T) *onboardingHarness {
	t.Helper()

	h := &onboardingHarness{
		t:       t,
		stdout:  &bytes.Buffer{},
		stderr:  &bytes.Buffer{},
		answers: onboarding.Answers{Email: "shukan@asymptotelabs.ai", Usage: onboarding.UsageWork},
		outcome: onboarding.OutcomeSubmitted,
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
	t.Setenv("HOME", t.TempDir())

	prevLoad, prevSave, prevAsk, prevSend := onboardingLoad, onboardingSave, onboardingAskWith, onboardingSend
	prevTTY, prevRoot, prevProbe := onboardingIsTTY, onboardingIsRoot, onboardingProbeFn
	prevStandalone, prevAskable := onboardingAskDestination, destinationAskable
	t.Setenv(managedIngestEnvEnabled, "")

	onboardingLoad = func() onboarding.Profile { return h.loaded }
	onboardingSave = func(p onboarding.Profile) error {
		h.saved = append(h.saved, p)
		return nil
	}
	onboardingAskWith = func(_ io.Reader, _ io.Writer, opts onboarding.PromptOptions) (onboarding.Answers, error) {
		h.asked = true
		h.offered = append(h.offered, opts)
		return h.answers, h.askErr
	}
	onboardingAskDestination = func(_ io.Reader, _ io.Writer, offerAsymptote bool) (string, error) {
		h.standaloneAsked = true
		h.standaloneOffered = offerAsymptote
		return h.standaloneAnswer, nil
	}
	destinationAskable = func() bool { return h.askable }
	onboardingSend = func(_ context.Context, s onboarding.Submission) (string, error) {
		h.sent = append(h.sent, s)
		return h.outcome, h.sendErr
	}
	onboardingIsTTY = func() bool { return true }
	onboardingIsRoot = func() bool { return false }
	onboardingProbeFn = func() *onboarding.RuntimeProbe {
		h.probeRuns++
		return nil
	}

	t.Cleanup(func() {
		endpointOpts = prevOpts
		onboardingLoad, onboardingSave, onboardingAskWith, onboardingSend = prevLoad, prevSave, prevAsk, prevSend
		onboardingIsTTY, onboardingIsRoot, onboardingProbeFn = prevTTY, prevRoot, prevProbe
		onboardingAskDestination, destinationAskable = prevStandalone, prevAskable
	})
	return h
}

func TestMaybeRunOnboardingPromptsOnInteractiveInstall(t *testing.T) {
	h := newOnboardingHarness(t)

	if _, err := maybeRunOnboarding(h.cmd); err != nil {
		t.Fatalf("maybeRunOnboarding returned error: %v", err)
	}
	if !h.asked {
		t.Fatalf("expected the prompt to run on an interactive per-user install")
	}
	if len(h.sent) != 1 {
		t.Fatalf("submissions = %d, want 1", len(h.sent))
	}
	if h.sent[0].Email != "shukan@asymptotelabs.ai" || h.sent[0].Usage != onboarding.UsageWork {
		t.Fatalf("submission = %+v, want the prompt answers", h.sent[0])
	}
	if h.sent[0].InstallMode != "user" {
		t.Fatalf("install_mode = %q, want user", h.sent[0].InstallMode)
	}
	if len(h.saved) != 1 || !h.saved[0].Prompted() {
		t.Fatalf("saved = %+v, want one completed profile", h.saved)
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
				h.loaded = onboarding.Profile{Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-07T00:00:00Z"}}
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

			if _, err := maybeRunOnboarding(h.cmd); err != nil {
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

	_, err := maybeRunOnboarding(h.cmd)
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

// The user answered; the network did not cooperate. That is our problem, not theirs.
func TestMaybeRunOnboardingSurvivesSubmissionFailure(t *testing.T) {
	h := newOnboardingHarness(t)
	h.outcome = onboarding.OutcomePending
	h.sendErr = errors.New("connection refused")

	if _, err := maybeRunOnboarding(h.cmd); err != nil {
		t.Fatalf("maybeRunOnboarding returned error: %v", err)
	}
	if len(h.saved) != 1 {
		t.Fatalf("saved = %d profiles, want 1", len(h.saved))
	}
	saved := h.saved[0]
	if !saved.Prompted() {
		t.Fatalf("profile is not marked completed, so the user would be asked again")
	}
	if saved.Onboarding.Outcome != onboarding.OutcomePending {
		t.Fatalf("outcome = %q, want %q", saved.Onboarding.Outcome, onboarding.OutcomePending)
	}
	if saved.Pending == nil {
		t.Fatalf("pending payload was not kept, so the signup can never be resent")
	}
}

// A refusal is terminal, and the address should not linger on disk afterwards.
func TestMaybeRunOnboardingDropsPayloadWhenRejected(t *testing.T) {
	h := newOnboardingHarness(t)
	h.outcome = onboarding.OutcomeRejected
	h.sendErr = errors.New("signup endpoint returned 429 Too Many Requests")

	if _, err := maybeRunOnboarding(h.cmd); err != nil {
		t.Fatalf("maybeRunOnboarding returned error: %v", err)
	}
	if h.saved[0].Pending != nil {
		t.Fatalf("kept a payload for a terminal rejection")
	}
	if !strings.Contains(h.stderr.String(), "continuing") {
		t.Fatalf("stderr = %q, want a note that the install continues", h.stderr.String())
	}
}

func TestMaybeRunOnboardingResendsPendingSubmission(t *testing.T) {
	h := newOnboardingHarness(t)
	pending := onboarding.Submission{InstallID: "abc123", Email: "shukan@asymptotelabs.ai"}
	h.loaded = onboarding.Profile{
		InstallID:  "abc123",
		Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-07T00:00:00Z", Outcome: onboarding.OutcomePending},
		Pending:    &pending,
	}

	if _, err := maybeRunOnboarding(h.cmd); err != nil {
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
		Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-07T00:00:00Z", Outcome: onboarding.OutcomePending},
		Pending:    &pending,
	}

	if _, err := maybeRunOnboarding(h.cmd); err != nil {
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

	if _, err := maybeRunOnboarding(h.cmd); err != nil {
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

	if _, err := maybeRunOnboarding(h.cmd); err != nil {
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

	if _, err := maybeRunOnboarding(h.cmd); err != nil {
		t.Fatalf("maybeRunOnboarding returned error: %v", err)
	}
	if len(h.sent) != 0 {
		t.Fatalf("submitted despite BEACON_ONBOARDING=0")
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
		Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-07T00:00:00Z", Outcome: onboarding.OutcomePending},
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

func TestMaybeRunOnboardingAsksDestinationOnlyWhenAskable(t *testing.T) {
	h := newOnboardingHarness(t)
	h.askable = false
	if connect, err := maybeRunOnboarding(h.cmd); err != nil || connect {
		t.Fatalf("connect=%t err=%v", connect, err)
	}
	if len(h.offered) != 1 || h.offered[0].AskDestination {
		t.Fatalf("the destination question must not be asked on a connected endpoint: %+v", h.offered)
	}
	if h.saved[len(h.saved)-1].Onboarding.Destination != "" {
		t.Fatal("a question that was not asked must not be recorded")
	}

	h = newOnboardingHarness(t)
	h.askable = true
	h.answers.DestinationAsked = true
	h.answers.Destination = onboarding.DestinationAsymptote
	connect, err := maybeRunOnboarding(h.cmd)
	if err != nil || !connect {
		t.Fatalf("the Asymptote answer should request a connect: connect=%t err=%v", connect, err)
	}
	if !h.offered[0].AskDestination || !h.offered[0].OfferAsymptote {
		t.Fatalf("prompt should have been asked to offer all three destinations: %+v", h.offered[0])
	}
	if got := h.saved[len(h.saved)-1].Onboarding.Destination; got != "" {
		t.Fatalf("asymptote is recorded only once the install has connected the machine, got %q", got)
	}

	for _, answer := range []string{onboarding.DestinationLocal, onboarding.DestinationOwnInfra} {
		h = newOnboardingHarness(t)
		h.askable = true
		h.answers.DestinationAsked = true
		h.answers.Destination = answer
		if connect, _ := maybeRunOnboarding(h.cmd); connect {
			t.Fatalf("%s must not connect", answer)
		}
		if got := h.saved[len(h.saved)-1].Onboarding.Destination; got != answer {
			t.Fatalf("recorded destination = %q, want %q", got, answer)
		}
	}
}

// BEACON_MANAGED_INGEST=0 hides the Asymptote row but the question is still asked.
func TestDestinationQuestionHidesAsymptoteWhenOptedOut(t *testing.T) {
	h := newOnboardingHarness(t)
	h.askable = true
	t.Setenv(managedIngestEnvEnabled, "0")
	h.answers.DestinationAsked = true
	h.answers.Destination = onboarding.DestinationLocal
	if connect, err := maybeRunOnboarding(h.cmd); err != nil || connect {
		t.Fatalf("connect=%t err=%v", connect, err)
	}
	if len(h.offered) != 1 || !h.offered[0].AskDestination || h.offered[0].OfferAsymptote {
		t.Fatalf("BEACON_MANAGED_INGEST=0 must hide only the Asymptote row: %+v", h.offered)
	}

	h = newOnboardingHarness(t)
	h.askable = true
	t.Setenv(managedIngestEnvEnabled, "0")
	h.loaded = onboarding.Profile{InstallID: "abc", Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-01T00:00:00Z"}}
	h.standaloneAnswer = onboarding.DestinationOwnInfra
	if _, err := maybeRunOnboarding(h.cmd); err != nil || !h.standaloneAsked || h.standaloneOffered {
		t.Fatalf("standalone question should be asked without the Asymptote row: err=%v asked=%t offered=%t", err, h.standaloneAsked, h.standaloneOffered)
	}
}

func TestDestinationAskedOnceToPreviouslyOnboardedMachine(t *testing.T) {
	h := newOnboardingHarness(t)
	h.askable = true
	h.loaded = onboarding.Profile{InstallID: "abc", Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-01T00:00:00Z", Outcome: onboarding.OutcomeSubmitted, Email: "shukan@asymptotelabs.ai", Usage: onboarding.UsageWork}}
	h.standaloneAnswer = onboarding.DestinationAsymptote
	connect, err := maybeRunOnboarding(h.cmd)
	if err != nil || !connect {
		t.Fatalf("connect=%t err=%v", connect, err)
	}
	if h.asked {
		t.Fatal("the signup questions must not be asked again")
	}
	if !h.standaloneAsked || !h.standaloneOffered {
		t.Fatalf("the destination question should be asked once, with the Asymptote row: asked=%t offered=%t", h.standaloneAsked, h.standaloneOffered)
	}
	if len(h.saved) != 0 {
		t.Fatalf("asymptote must not be recorded before the connect succeeds: %+v", h.saved)
	}

	// Local and own infrastructure are recorded at once.
	for _, answer := range []string{onboarding.DestinationLocal, onboarding.DestinationOwnInfra} {
		h = newOnboardingHarness(t)
		h.askable = true
		h.loaded = onboarding.Profile{InstallID: "abc", Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-01T00:00:00Z"}}
		h.standaloneAnswer = answer
		if connect, _ := maybeRunOnboarding(h.cmd); connect || !h.standaloneAsked {
			t.Fatalf("connect=%t asked=%t", connect, h.standaloneAsked)
		}
		if got := h.saved[len(h.saved)-1].Onboarding.Destination; got != answer {
			t.Fatalf("recorded destination = %q, want %q", got, answer)
		}
	}

	// Once recorded, it is never asked again.
	h = newOnboardingHarness(t)
	h.askable = true
	h.loaded = onboarding.Profile{InstallID: "abc", Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-01T00:00:00Z", Destination: onboarding.DestinationLocal}}
	if connect, _ := maybeRunOnboarding(h.cmd); connect || h.standaloneAsked {
		t.Fatalf("a recorded destination must not be asked again: connect=%t asked=%t", connect, h.standaloneAsked)
	}

	// Non-interactive paths never see it either.
	h = newOnboardingHarness(t)
	h.askable = true
	h.loaded = onboarding.Profile{InstallID: "abc", Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-01T00:00:00Z"}}
	onboardingIsTTY = func() bool { return false }
	if connect, _ := maybeRunOnboarding(h.cmd); connect || h.standaloneAsked {
		t.Fatal("no terminal, no question")
	}
	h = newOnboardingHarness(t)
	h.askable = true
	h.loaded = onboarding.Profile{InstallID: "abc", Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-01T00:00:00Z"}}
	t.Setenv("CI", "1")
	if connect, _ := maybeRunOnboarding(h.cmd); connect || h.standaloneAsked {
		t.Fatal("CI never sees the question")
	}
	// A connected endpoint has answered by doing.
	h = newOnboardingHarness(t)
	h.askable = false
	h.loaded = onboarding.Profile{InstallID: "abc", Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-01T00:00:00Z"}}
	if connect, _ := maybeRunOnboarding(h.cmd); connect || h.standaloneAsked {
		t.Fatal("a connected endpoint is not asked")
	}
}

// An Asymptote answer whose install or connect failed must be asked again, not remembered
// as done: the record is written only by recordDestinationAsymptote, after the connect.
func TestAsymptoteDestinationIsRecordedAfterConnect(t *testing.T) {
	h := newOnboardingHarness(t)
	h.askable = true
	h.answers.DestinationAsked = true
	h.answers.Destination = onboarding.DestinationAsymptote
	if connect, err := maybeRunOnboarding(h.cmd); err != nil || !connect {
		t.Fatalf("connect=%t err=%v", connect, err)
	}
	afterPrompt := h.saved[len(h.saved)-1]
	if afterPrompt.Onboarding.Destination != "" {
		t.Fatalf("recorded before connect: %q", afterPrompt.Onboarding.Destination)
	}

	// The install failed before connecting: the next interactive install asks again.
	h2 := newOnboardingHarness(t)
	h2.askable = true
	h2.loaded = afterPrompt
	h2.standaloneAnswer = onboarding.DestinationAsymptote
	if connect, err := maybeRunOnboarding(h2.cmd); err != nil || !connect || !h2.standaloneAsked || h2.asked {
		t.Fatalf("connect=%t err=%v standaloneAsked=%t signupAsked=%t", connect, err, h2.standaloneAsked, h2.asked)
	}

	// The install connected: the answer is recorded and the question retires.
	h3 := newOnboardingHarness(t)
	h3.askable = true
	h3.loaded = afterPrompt
	recordDestinationAsymptote(h3.cmd)
	if got := h3.saved[len(h3.saved)-1].Onboarding.Destination; got != onboarding.DestinationAsymptote {
		t.Fatalf("recorded destination = %q", got)
	}
	h4 := newOnboardingHarness(t)
	h4.askable = true
	h4.loaded = h3.saved[len(h3.saved)-1]
	if connect, _ := maybeRunOnboarding(h4.cmd); connect || h4.standaloneAsked {
		t.Fatalf("a recorded destination must not be asked again: connect=%t asked=%t", connect, h4.standaloneAsked)
	}

	// Never written for a machine that was not onboarded (root, CI, --connect without a prompt).
	h5 := newOnboardingHarness(t)
	recordDestinationAsymptote(h5.cmd)
	if len(h5.saved) != 0 {
		t.Fatalf("no profile should be written without a completed onboarding: %+v", h5.saved)
	}

	// A recorded local answer stays local even when an operator connects with --connect.
	h6 := newOnboardingHarness(t)
	h6.loaded = onboarding.Profile{InstallID: "abc", Onboarding: onboarding.Onboarding{CompletedAt: "2026-08-01T00:00:00Z", Destination: onboarding.DestinationLocal}}
	recordDestinationAsymptote(h6.cmd)
	if len(h6.saved) != 0 {
		t.Fatalf("an owner's answer must not be rewritten: %+v", h6.saved)
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
	if connect, err := maybeRunOnboarding(h.cmd); err != nil || connect {
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
	if connect, err := maybeRunOnboarding(h.cmd); err != nil || connect || h.standaloneAsked {
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
		onboarding.DestinationAsymptote: "Telemetry destination: Asymptote Managed",
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
}
