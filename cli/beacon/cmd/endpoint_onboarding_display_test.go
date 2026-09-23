package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/account"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/auth"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/onboarding"
)

type recordingReporter struct{ prompts []onboarding.SignInPrompt }

func (r *recordingReporter) Prompt(p onboarding.SignInPrompt) { r.prompts = append(r.prompts, p) }

// The wizard can only skip a sign-in that cannot finish if it is told there is no display, and the
// SSH path only works if it is handed the port to forward.
func TestOnboardingTellsTheWizardAboutAMissingDisplay(t *testing.T) {
	h := newOnboardingHarness(t)
	h.accountStatus = account.Status{}

	prevCheck, prevLogin := onboardingDisplayCheck, onboardingAccountLogin
	t.Cleanup(func() { onboardingDisplayCheck, onboardingAccountLogin = prevCheck, prevLogin })
	onboardingDisplayCheck = func() error { return fmt.Errorf("%w: this is an SSH session", auth.ErrNoDisplay) }
	onboardingAccountLogin = func(_ context.Context, opts account.LoginOptions) (*account.Session, error) {
		opts.OnPrompt(account.LoginPrompt{URL: "https://beacon.sh/cli/auth?port=54123", Port: 54123})
		return nil, errors.New("stop here")
	}

	var noDisplay error
	reporter := &recordingReporter{}
	onboardingRunWizard = func(_ io.Reader, _ io.Writer, opts onboarding.WizardOptions) (onboarding.WizardResult, error) {
		noDisplay = opts.NoDisplay
		_, _ = opts.SignIn(context.Background(), onboarding.SignInRequest{NoBrowser: true}, reporter)
		return onboarding.WizardResult{}, onboarding.ErrWizardCancelled
	}

	_, _ = h.run(t)
	if !errors.Is(noDisplay, auth.ErrNoDisplay) {
		t.Fatalf("WizardOptions.NoDisplay = %v, want ErrNoDisplay", noDisplay)
	}
	if len(reporter.prompts) != 1 || reporter.prompts[0].SSHForward != "ssh -L 54123:127.0.0.1:54123 <this machine>" {
		t.Fatalf("prompts = %#v, want the port forward for 54123", reporter.prompts)
	}
}
