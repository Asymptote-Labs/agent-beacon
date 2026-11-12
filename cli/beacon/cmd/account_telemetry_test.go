package cmd

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/account"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/onboarding"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/telemetry"
)

type identified struct{ email, userID string }

// recordIdentify swaps the identify seam for one that keeps every call.
func recordIdentify(t *testing.T) *[]identified {
	t.Helper()
	calls := &[]identified{}
	old := telemetryIdentify
	telemetryIdentify = func(email, userID string) { *calls = append(*calls, identified{email, userID}) }
	t.Cleanup(func() { telemetryIdentify = old })
	return calls
}

type discardReporter struct{}

func (discardReporter) Prompt(onboarding.SignInPrompt) {}

func TestLoginSendsSignInAndIdentifies(t *testing.T) {
	withAccountFakes(t)
	events := recordTelemetry(t)
	ids := recordIdentify(t)
	session, status := accountFixture()
	accountLogin = func(context.Context, account.LoginOptions) (*account.Session, error) { return session, nil }
	accountSave = func(account.Session) error { return nil }
	accountInspect = func(time.Time) account.Status { return status }
	loginCmd.SetOut(io.Discard)
	loginCmd.SetErr(io.Discard)
	if err := runLogin(loginCmd, nil); err != nil {
		t.Fatal(err)
	}
	signIns := eventsNamed(*events, telemetry.EventSignInCompleted)
	if len(signIns) != 1 || signIns[0].props[telemetry.PropTrigger] != telemetry.TriggerLoginCommand {
		t.Fatalf("events = %+v", *events)
	}
	if len(*ids) != 1 || (*ids)[0] != (identified{"person@example.com", "usr_1"}) {
		t.Fatalf("identified %+v", *ids)
	}
}

func TestWizardSignInSendsSignInAndIdentifies(t *testing.T) {
	h := newOnboardingHarness(t)
	h.accountStatus = account.Status{}
	events := recordTelemetry(t)
	ids := recordIdentify(t)
	// The wizard signs in in place through its SignIn function.
	onboardingRunWizard = func(_ io.Reader, _ io.Writer, opts onboarding.WizardOptions) (onboarding.WizardResult, error) {
		acct, err := opts.SignIn(context.Background(), onboarding.SignInRequest{}, discardReporter{})
		if err != nil {
			return onboarding.WizardResult{}, err
		}
		return onboarding.WizardResult{Completed: true, SignedInEmail: acct.Email, Destination: onboarding.DestinationLocal}, nil
	}
	if _, err := runOnboarding(t, h.cmd); err != nil {
		t.Fatal(err)
	}
	signIns := eventsNamed(*events, telemetry.EventSignInCompleted)
	if len(signIns) != 1 || signIns[0].props[telemetry.PropTrigger] != telemetry.TriggerWizard {
		t.Fatalf("events = %+v", *events)
	}
	if len(*ids) != 1 || (*ids)[0] != (identified{"shukan@asymptotelabs.ai", "usr_1"}) {
		t.Fatalf("identified %+v", *ids)
	}
}

func TestWizardFallbackLoginSendsSignIn(t *testing.T) {
	h := newOnboardingHarness(t)
	h.accountStatus = account.Status{}
	events := recordTelemetry(t)
	ids := recordIdentify(t)
	if _, err := runOnboarding(t, h.cmd); err != nil {
		t.Fatal(err)
	}
	if h.loginRuns != 1 || len(eventsNamed(*events, telemetry.EventSignInCompleted)) != 1 || len(*ids) != 1 {
		t.Fatalf("login runs %d, events %+v, identified %+v", h.loginRuns, *events, *ids)
	}
}

func TestWizardWithASignedInAccountIdentifiesWithoutASignInEvent(t *testing.T) {
	h := newOnboardingHarness(t)
	events := recordTelemetry(t)
	ids := recordIdentify(t)
	if _, err := runOnboarding(t, h.cmd); err != nil {
		t.Fatal(err)
	}
	if len(eventsNamed(*events, telemetry.EventSignInCompleted)) != 0 {
		t.Fatalf("events = %+v", *events)
	}
	if len(*ids) != 1 || (*ids)[0] != (identified{"shukan@asymptotelabs.ai", "usr_1"}) {
		t.Fatalf("identified %+v", *ids)
	}
}

func TestInstallEmailSources(t *testing.T) {
	cases := []struct {
		name       string
		flag       string
		env        map[string]string
		wantSource string
		wantEmail  string
		wantKind   string
	}{
		{name: "flag", flag: " Ada@Acme.io ", env: map[string]string{installEmailEnv: "env@acme.io"}, wantSource: telemetry.EmailSourceFlag, wantEmail: "ada@acme.io", wantKind: telemetry.EmailDomainWork},
		{name: "env", env: map[string]string{installEmailEnv: "someone@gmail.com"}, wantSource: telemetry.EmailSourceEnv, wantEmail: "someone@gmail.com", wantKind: telemetry.EmailDomainFree},
		{name: "legacy env", env: map[string]string{onboardingEnvEmail: "ops@acme.io"}, wantSource: telemetry.EmailSourceEnv, wantEmail: "ops@acme.io", wantKind: telemetry.EmailDomainWork},
		{name: "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOnboardingHarness(t)
			h.accountStatus = account.Status{}
			events := recordTelemetry(t)
			ids := recordIdentify(t)
			t.Setenv(installEmailEnv, "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			endpointOpts.email = tc.flag
			submitInstallEmail(h.stderr)

			submitted := eventsNamed(*events, telemetry.EventEmailSubmitted)
			if tc.wantSource == "" {
				if len(submitted) != 0 || len(*ids) != 0 || len(h.saved) != 0 {
					t.Fatalf("no email sent %+v, identified %+v, saved %+v", *events, *ids, h.saved)
				}
				return
			}
			want := map[string]any{telemetry.PropEmailSource: tc.wantSource, telemetry.PropEmailDomainKind: tc.wantKind}
			if len(submitted) != 1 || len(submitted[0].props) != 2 ||
				submitted[0].props[telemetry.PropEmailSource] != want[telemetry.PropEmailSource] ||
				submitted[0].props[telemetry.PropEmailDomainKind] != want[telemetry.PropEmailDomainKind] {
				t.Fatalf("events = %+v, want %+v", *events, want)
			}
			if len(*ids) != 1 || (*ids)[0] != (identified{tc.wantEmail, ""}) {
				t.Fatalf("identified %+v", *ids)
			}
			if len(h.saved) != 1 || h.saved[0].Onboarding.Email != tc.wantEmail || h.saved[0].Prompted() {
				t.Fatalf("saved %+v", h.saved)
			}
		})
	}
}

func TestInstallEmailIsNotRecordedOverASignedInAccount(t *testing.T) {
	h := newOnboardingHarness(t)
	events := recordTelemetry(t)
	recordIdentify(t)
	endpointOpts.email = "other@acme.io"
	submitInstallEmail(h.stderr)
	if len(eventsNamed(*events, telemetry.EventEmailSubmitted)) != 1 {
		t.Fatalf("events = %+v", *events)
	}
	if len(h.saved) != 0 {
		t.Fatalf("recorded the email over the signed-in account: %+v", h.saved)
	}
}

func TestInvalidInstallEmail(t *testing.T) {
	h := newOnboardingHarness(t)
	events := recordTelemetry(t)
	endpointOpts.email = "not-an-email"
	if err := validateInstallEmailFlag(); err == nil || !strings.Contains(err.Error(), "--email") {
		t.Fatalf("validateInstallEmailFlag = %v", err)
	}
	endpointOpts.email = ""
	t.Setenv(installEmailEnv, "still not")
	var stderr bytes.Buffer
	submitInstallEmail(&stderr)
	if !strings.Contains(stderr.String(), "ignoring "+installEmailEnv) || len(*events) != 0 || len(h.saved) != 0 {
		t.Fatalf("stderr %q, events %+v, saved %+v", stderr.String(), *events, h.saved)
	}
}

// An email given to the install survives a wizard finished without an account.
func TestWizardWithoutAccountKeepsTheInstallEmail(t *testing.T) {
	h := newOnboardingHarness(t)
	h.accountStatus = account.Status{}
	recordTelemetry(t)
	recordIdentify(t)
	h.loaded = onboarding.Profile{Onboarding: onboarding.Onboarding{Email: "ada@acme.io"}}
	onboardingRunWizard = func(io.Reader, io.Writer, onboarding.WizardOptions) (onboarding.WizardResult, error) {
		return onboarding.WizardResult{Completed: true, WithoutAccount: true, Destination: onboarding.DestinationLocal}, nil
	}
	if _, err := runOnboarding(t, h.cmd); err != nil {
		t.Fatal(err)
	}
	if len(h.saved) != 1 || h.saved[0].Onboarding.Email != "ada@acme.io" || h.saved[0].Onboarding.Outcome != onboarding.OutcomeSkipped {
		t.Fatalf("saved %+v", h.saved)
	}
}
