package onboarding

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func wizardKey(value string) tea.KeyMsg {
	switch value {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)}
	}
}

func advanceWizard(t *testing.T, model wizardModel, key string) (wizardModel, tea.Cmd) {
	t.Helper()
	next, command := model.Update(wizardKey(key))
	return next.(wizardModel), command
}

func TestWizardRequestsLoginBeforeDestination(t *testing.T) {
	model := newWizardModel(WizardOptions{OfferManaged: true})
	model.width, model.height = 100, 30
	model, _ = advanceWizard(t, model, "enter")
	if model.screen != signInScreen {
		t.Fatalf("screen = %v, want sign in", model.screen)
	}
	if view := model.View(); !strings.Contains(view, "beacon.sh") {
		t.Fatalf("sign-in screen should name where it sends you:\n%s", view)
	}
	model, command := advanceWizard(t, model, "enter")
	if command == nil || !model.result.NeedLogin || model.result.Completed {
		t.Fatalf("result = %#v command=%v", model.result, command != nil)
	}
}

func TestSignedInWizardCanOptOutToLocal(t *testing.T) {
	model := newWizardModel(WizardOptions{
		SignedIn:     true,
		Email:        "person@example.com",
		OfferManaged: true,
	})
	model.width, model.height = 100, 30
	model, _ = advanceWizard(t, model, "enter")
	if model.screen != destinationScreen {
		t.Fatalf("screen = %v, want destination", model.screen)
	}
	if got := model.destinations(); len(got) != 2 || got[0] != DestinationAsymptote || got[1] != DestinationLocal {
		t.Fatalf("destination order = %#v", got)
	}
	model, _ = advanceWizard(t, model, "j")
	model, _ = advanceWizard(t, model, "enter")
	if model.screen != confirmScreen || model.result.Destination != DestinationLocal {
		t.Fatalf("local selection = screen %v result %#v", model.screen, model.result)
	}
	if view := model.View(); !strings.Contains(view, "telemetry stays on this machine") {
		t.Fatalf("local confirmation missing:\n%s", view)
	}
	model, command := advanceWizard(t, model, "enter")
	if command == nil || !model.result.Completed {
		t.Fatalf("completion = %#v command=%v", model.result, command != nil)
	}
}

func TestManagedWizardRequiresDisclosureConfirmation(t *testing.T) {
	model := newWizardModel(WizardOptions{
		SignedIn:        true,
		Email:           "person@example.com",
		OfferManaged:    true,
		DestinationOnly: true,
	})
	model.width, model.height = 100, 30
	model, _ = advanceWizard(t, model, "enter")
	if model.screen != managedDisclosureScreen || model.result.Destination != DestinationAsymptote {
		t.Fatalf("managed selection = screen %v result %#v", model.screen, model.result)
	}
	view := strings.Join(strings.Fields(model.View()), " ")
	for _, want := range []string{
		// Plain language: what is sent, what is not, and how to stop. No mention of
		// the forwarder's implementation, which nobody choosing a destination needs.
		"What gets sent to beacon.sh",
		"sent to your Beacon account as it happens",
		"What is already on this machine stays here",
		"which agents and tools you have installed",
		"beacon endpoint disconnect",
		// The disclosure has to describe what actually ships. The runtime source is
		// read_from = "end", so pre-connect runtime events stay local -- but the
		// inventory source is read_from = "beginning", so the existing snapshot does
		// upload. The screen used to flatly claim nothing existing is uploaded.
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("managed disclosure missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Existing local history is not uploaded") {
		t.Fatalf("the disclosure must not claim existing history is never uploaded; inventory is:\n%s", view)
	}
	for _, jargon := range []string{"Vector", "HTTPS", "forwarder", "snapshot"} {
		if strings.Contains(view, jargon) {
			t.Fatalf("the disclosure should not need %q to be understood:\n%s", jargon, view)
		}
	}
	model, _ = advanceWizard(t, model, "enter")
	if model.screen != privacyScreen {
		t.Fatalf("screen = %v, want privacy", model.screen)
	}
	if view := model.View(); !strings.Contains(view, "Standard (recommended)") || !strings.Contains(view, "Metadata only") {
		t.Fatalf("privacy choices missing:\n%s", view)
	}
	model, _ = advanceWizard(t, model, "enter")
	if model.screen != confirmScreen {
		t.Fatalf("screen = %v, want confirmation", model.screen)
	}
	if model.result.PrivacyMode != "standard" {
		t.Fatalf("privacy mode = %q", model.result.PrivacyMode)
	}
	model, _ = advanceWizard(t, model, "enter")
	if !model.result.Completed {
		t.Fatalf("result = %#v", model.result)
	}
}

func TestWizardSelectsMetadataOnlyPrivacy(t *testing.T) {
	model := newWizardModel(WizardOptions{
		SignedIn:        true,
		Email:           "person@example.com",
		OfferManaged:    true,
		DestinationOnly: true,
	})
	model, _ = advanceWizard(t, model, "enter")
	model, _ = advanceWizard(t, model, "enter")
	model, _ = advanceWizard(t, model, "j")
	model, _ = advanceWizard(t, model, "enter")
	if model.screen != confirmScreen || model.result.PrivacyMode != "metadata_only" {
		t.Fatalf("metadata selection = screen %v result %#v", model.screen, model.result)
	}
}

func TestWizardHidesManagedDestination(t *testing.T) {
	model := newWizardModel(WizardOptions{
		SignedIn:        true,
		Email:           "person@example.com",
		OfferManaged:    false,
		DestinationOnly: true,
	})
	if got := model.destinations(); len(got) != 1 || got[0] != DestinationLocal {
		t.Fatalf("destinations = %#v", got)
	}
}

func TestWizardPresetManagedSkipsDestinationChoice(t *testing.T) {
	model := newWizardModel(WizardOptions{
		SignedIn:          true,
		Email:             "person@example.com",
		OfferManaged:      false,
		PresetDestination: DestinationAsymptote,
	})
	model, _ = advanceWizard(t, model, "enter")
	if model.screen != managedDisclosureScreen || model.result.Destination != DestinationAsymptote {
		t.Fatalf("preset managed = screen %v result %#v", model.screen, model.result)
	}
}

// The destination captions are the only place a first-time user learns why Managed
// exists. They used to describe the mechanism ("forward new events after you
// connect") and named no benefit at all.
func TestDestinationCaptionsNameTheValueNotTheMechanism(t *testing.T) {
	managedLabel, managedDetail := destinationCopy(DestinationAsymptote)
	if !strings.Contains(managedLabel, "Beacon Managed") {
		t.Fatalf("managed label = %q", managedLabel)
	}
	for _, want := range []string{"Free, unlimited cloud-based retention", "AI search and analytics", "findings", "detections", "full feature set"} {
		if !strings.Contains(managedDetail, want) {
			t.Fatalf("managed detail missing %q: %q", want, managedDetail)
		}
	}

	localLabel, localDetail := destinationCopy(DestinationLocal)
	if !strings.Contains(localLabel, "Local only") {
		t.Fatalf("local label = %q", localLabel)
	}
	// Local is a supported end state, not a penalty, and it is not feature-poor:
	// the local dashboard ships the same Findings, Detections, Analytics and Token
	// Usage views. Claiming Managed adds those would be false, so the copy has to
	// differentiate on scope, retention and durability instead.
	for _, want := range []string{"this machine", "Nothing leaves your machine", "testing", "agent activity in one place"} {
		if !strings.Contains(localDetail, want) {
			t.Fatalf("local detail missing %q: %q", want, localDetail)
		}
	}
	if strings.Contains(localDetail, "Opt out") {
		t.Fatalf("local should read as a peer choice, not a refusal: %q", localDetail)
	}
	// Both say when to choose them, which is the part a first-time user needs.
	for name, detail := range map[string]string{"managed": managedDetail, "local": localDetail} {
		if !strings.Contains(detail, "Pick this") {
			t.Fatalf("%s detail gives no guidance on when to choose it: %q", name, detail)
		}
	}
}

// A caption that wraps must stay in its column. The detail was previously emitted
// as one pre-indented string and wrapped with the rest of the body, which indented
// the first line and left continuations flush against the margin.
func TestChoiceDetailIndentsEveryWrappedLine(t *testing.T) {
	model := newWizardModel(WizardOptions{SignedIn: true, OfferManaged: true, DestinationOnly: true})
	model.width, model.height = 72, 30

	_, detail := destinationCopy(DestinationAsymptote)
	rendered := choiceDetail(detail, 64)
	lines := strings.Split(rendered, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected the managed caption to wrap at this width, got one line: %q", rendered)
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, "      ") {
			t.Fatalf("wrapped caption line is not indented: %q", line)
		}
	}

	// And the whole screen still renders every word of both captions.
	view := strings.Join(strings.Fields(model.View()), " ")
	for _, want := range []string{"full feature set.", "agent activity in one place."} {
		if !strings.Contains(view, want) {
			t.Fatalf("destination screen truncated %q:\n%s", want, view)
		}
	}
}

// "q" used to quit from every screen, so a stray keystroke on a choice list killed
// the install, and no screen could ever accept free text.
func TestWizardHasNoGlobalQuitKey(t *testing.T) {
	for _, screen := range []wizardScreen{welcomeScreen, signInScreen, destinationScreen, managedDisclosureScreen, privacyScreen, confirmScreen} {
		model := newWizardModel(WizardOptions{SignedIn: true, OfferManaged: true})
		model.screen = screen
		next, command := advanceWizard(t, model, "q")
		if command != nil {
			t.Fatalf("q returned a command on screen %v; it must not quit", screen)
		}
		if next.screen != screen || next.result.Completed || next.result.NeedLogin {
			t.Fatalf("q changed state on screen %v: %#v", screen, next.result)
		}
	}
	// esc still cancels.
	model := newWizardModel(WizardOptions{SignedIn: true, OfferManaged: true})
	if _, command := advanceWizard(t, model, "esc"); command == nil {
		t.Fatal("esc must still cancel the wizard")
	}
}

// The confirm screen is where forwarding is authorized, so it has to name the
// action. It used to say "Next step after install: beacon endpoint connect", which
// stopped being true once confirming did the connecting.
func TestConfirmScreenNamesWhatConfirmingStarts(t *testing.T) {
	for mode, wantSends := range map[string]string{
		"standard":      "including prompts and tool activity",
		"metadata_only": "stay here",
	} {
		model := newWizardModel(WizardOptions{
			SignedIn:          true,
			Email:             "person@example.com",
			OfferManaged:      true,
			DestinationOnly:   true,
			PresetDestination: DestinationAsymptote,
			PresetPrivacyMode: mode,
		})
		model.width, model.height = 100, 30
		model.screen = confirmScreen
		model.result.Destination = DestinationAsymptote
		model.result.PrivacyMode = mode

		view := strings.Join(strings.Fields(model.View()), " ")
		for _, want := range []string{
			"This installs Beacon and starts forwarding to beacon.sh.",
			wantSends,
			"enter install and connect",
		} {
			if !strings.Contains(view, want) {
				t.Fatalf("confirm screen for %s missing %q:\n%s", mode, want, view)
			}
		}
		if strings.Contains(view, "Next step after install") {
			t.Fatalf("confirm screen still defers connect to a later command:\n%s", view)
		}
	}

	// Local says the opposite, and its hint stays plain.
	local := newWizardModel(WizardOptions{SignedIn: true, Email: "person@example.com", OfferManaged: true})
	local.width, local.height = 100, 30
	local.screen = confirmScreen
	local.result.Destination = DestinationLocal
	view := strings.Join(strings.Fields(local.View()), " ")
	if !strings.Contains(view, "Your telemetry stays on this machine.") {
		t.Fatalf("local confirm screen = %s", view)
	}
	if strings.Contains(view, "install and connect") {
		t.Fatalf("local confirm screen must not offer to connect: %s", view)
	}
}

// signInWizard builds a wizard whose sign-in runs in place, the way RunWizard does.
func signInWizard(t *testing.T, signIn SignInFunc) wizardModel {
	t.Helper()
	model := newWizardModel(WizardOptions{
		OfferManaged:  true,
		SignIn:        signIn,
		SignInTimeout: 5 * time.Minute,
		Now:           func() time.Time { return time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC) },
	})
	model.events = make(chan tea.Msg, 8)
	model.width, model.height = 100, 30
	return model
}

func neverReturns(ctx context.Context, _ SignInRequest, _ Reporter) (Account, error) {
	<-ctx.Done()
	return Account{}, ctx.Err()
}

// Sign-in used to quit the wizard so the caller could run it, print to plain
// stdout, block for up to five minutes, and then start a second wizard -- which
// wiped what had just been printed. It now runs without the screen going away.
func TestSignInRunsInsideTheWizard(t *testing.T) {
	model := signInWizard(t, neverReturns)
	model.screen = signInScreen

	model, cmd := advanceWizard(t, model, "enter")
	if model.screen != signInWaitScreen {
		t.Fatalf("screen = %v, want the waiting screen", model.screen)
	}
	if model.result.NeedLogin {
		t.Fatal("an injected sign-in must not fall back to the caller")
	}
	if cmd == nil {
		t.Fatal("expected the wait to be armed")
	}
	t.Cleanup(func() { model.stopSignIn() })
}

// The URL has to be on screen before any browser is claimed to have opened, and it
// has to survive rendering intact: a soft-wrapped URL is one nobody can copy.
func TestSignInWaitShowsAnUnwrappedURLAndAClock(t *testing.T) {
	model := signInWizard(t, neverReturns)
	model.screen = signInScreen
	model, _ = advanceWizard(t, model, "enter")
	t.Cleanup(func() { model.stopSignIn() })

	url := "https://beacon.sh/cli/auth?port=54123&state=" + strings.Repeat("a", 43)
	next, _ := model.Update(signInPromptMsg{attempt: 1, prompt: SignInPrompt{URL: url, WillOpen: true}})
	model = next.(wizardModel)

	// Seven seconds later.
	later := model.startedAt.Add(7 * time.Second)
	next, _ = model.Update(tickMsg(later))
	model = next.(wizardModel)

	view := model.View()
	if !strings.Contains(view, url) {
		t.Fatalf("the sign-in URL must render on one unbroken line:\n%s", view)
	}
	flat := strings.Join(strings.Fields(view), " ")
	for _, want := range []string{"0:07 elapsed", "times out in 4:53"} {
		if !strings.Contains(flat, want) {
			t.Fatalf("waiting screen missing %q:\n%s", want, view)
		}
	}
}

// Abandoning the browser must not end the install. It used to: the wait was
// uncancellable, so ctrl+c during it killed the whole command.
func TestEscDuringSignInGoesBackWithoutEndingTheInstall(t *testing.T) {
	model := signInWizard(t, neverReturns)
	model.screen = signInScreen
	model, _ = advanceWizard(t, model, "enter")

	model, cmd := advanceWizard(t, model, "esc")
	if cmd != nil {
		t.Fatal("esc while waiting must not quit the wizard")
	}
	if model.screen != signInScreen {
		t.Fatalf("screen = %v, want the sign-in screen", model.screen)
	}
	if model.result.Completed || model.result.NeedLogin {
		t.Fatalf("result = %#v", model.result)
	}
}

// Signing in is the expected path, so the sign-in screen offers nothing else. A
// visible "skip" there would read as a suggestion.
func TestSignInScreenOffersNoAlternative(t *testing.T) {
	model := signInWizard(t, neverReturns)
	model.screen = signInScreen
	flat := strings.Join(strings.Fields(model.View()), " ")
	for _, unwanted := range []string{"without an account", "Local only", "skip"} {
		if strings.Contains(strings.ToLower(flat), strings.ToLower(unwanted)) {
			t.Fatalf("the sign-in screen must not advertise %q:\n%s", unwanted, flat)
		}
	}
}

// But a sign-in that genuinely failed has to leave a way forward, or an offline or
// browserless machine cannot install a local-first tool at all.
func TestSignInFailureOffersRecoveryAndALocalFinish(t *testing.T) {
	model := signInWizard(t, neverReturns)
	model.screen = signInScreen
	model, _ = advanceWizard(t, model, "enter")
	next, _ := model.Update(signInPromptMsg{attempt: 1, prompt: SignInPrompt{URL: "https://beacon.sh/cli/auth?port=1&state=x", WillOpen: true}})
	model = next.(wizardModel)
	next, _ = model.Update(signInDoneMsg{attempt: 1, err: errors.New("connection refused")})
	model = next.(wizardModel)

	if model.screen != signInFailedScreen {
		t.Fatalf("screen = %v, want the recovery screen", model.screen)
	}
	flat := strings.Join(strings.Fields(model.View()), " ")
	for _, want := range []string{"connection refused", "Try signing in again", "Show the URL", "Finish setup without an account"} {
		if !strings.Contains(flat, want) {
			t.Fatalf("recovery screen missing %q:\n%s", want, flat)
		}
	}

	// Retry is first, so the default action is to try again rather than give up.
	if model.recoveryChoices()[0].id != "retry" {
		t.Fatalf("recovery choices = %#v, want retry first", model.recoveryChoices())
	}

	// Choosing to finish without an account lands on local, never managed.
	ids := map[string]int{}
	for i, choice := range model.recoveryChoices() {
		ids[choice.id] = i
	}
	model.selected = ids["local"]
	model, _ = advanceWizard(t, model, "enter")
	if !model.result.WithoutAccount || model.result.Destination != DestinationLocal {
		t.Fatalf("result = %#v, want a local finish without an account", model.result)
	}
	if model.screen != confirmScreen {
		t.Fatalf("screen = %v, want confirmation", model.screen)
	}
	if flat := strings.Join(strings.Fields(model.View()), " "); !strings.Contains(flat, "not signed in") {
		t.Fatalf("the confirm screen should say no account is attached:\n%s", flat)
	}
}

// Picking the manual option retries with the browser suppressed.
func TestRecoveryCanRetryWithoutABrowser(t *testing.T) {
	requests := make(chan SignInRequest, 4)
	model := signInWizard(t, func(ctx context.Context, req SignInRequest, _ Reporter) (Account, error) {
		requests <- req
		<-ctx.Done()
		return Account{}, ctx.Err()
	})
	model.screen = signInFailedScreen
	model.prompt = SignInPrompt{URL: "https://beacon.sh/cli/auth", WillOpen: true}
	for i, choice := range model.recoveryChoices() {
		if choice.id == "manual" {
			model.selected = i
		}
	}
	model, _ = advanceWizard(t, model, "enter")
	t.Cleanup(func() { model.stopSignIn() })

	if model.screen != signInWaitScreen {
		t.Fatalf("screen = %v, want the waiting screen", model.screen)
	}
	select {
	case req := <-requests:
		if !req.NoBrowser {
			t.Fatalf("retry request = %#v, want the browser suppressed", req)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the retry never reached the sign-in function")
	}
}

// A successful sign-in continues to the next question in the same screen session.
func TestSignInSuccessContinuesToTheDestinationQuestion(t *testing.T) {
	model := signInWizard(t, neverReturns)
	model.screen = signInScreen
	model, _ = advanceWizard(t, model, "enter")
	next, _ := model.Update(signInDoneMsg{attempt: 1, account: Account{Email: "person@example.com"}})
	model = next.(wizardModel)

	if model.screen != destinationScreen {
		t.Fatalf("screen = %v, want the destination question", model.screen)
	}
	if model.result.SignedInEmail != "person@example.com" || !model.options.SignedIn {
		t.Fatalf("result = %#v options.SignedIn = %t", model.result, model.options.SignedIn)
	}
}

// With no sign-in injected the wizard keeps its original contract, which is what
// the command-level tests drive.
func TestWizardWithoutAnInjectedSignInStillReportsNeedLogin(t *testing.T) {
	model := newWizardModel(WizardOptions{OfferManaged: true})
	model.screen = signInScreen
	model, cmd := advanceWizard(t, model, "enter")
	if !model.result.NeedLogin || cmd == nil {
		t.Fatalf("result = %#v cmd = %v", model.result, cmd)
	}
}

// The whole first run, in one screen session: welcome, sign in, wait for the
// browser, answer the destination question, confirm. This is the sequence that
// used to require quitting the program and starting a second one around a blocking
// five-minute login.
func TestWholeFirstRunHappensInOneScreenSession(t *testing.T) {
	model := signInWizard(t, neverReturns)
	model.options.OfferManaged = false
	model.screen = welcomeScreen

	model, _ = advanceWizard(t, model, "enter")
	if model.screen != signInScreen {
		t.Fatalf("after welcome: screen = %v", model.screen)
	}
	model, quitCmd := advanceWizard(t, model, "enter")
	t.Cleanup(func() { model.stopSignIn() })
	if model.screen != signInWaitScreen {
		t.Fatalf("after sign-in: screen = %v", model.screen)
	}
	if model.result.NeedLogin {
		t.Fatal("the program must not exit to sign in")
	}
	_ = quitCmd

	next, _ := model.Update(signInPromptMsg{attempt: 1, prompt: SignInPrompt{URL: "https://beacon.sh/cli/auth?port=1&state=x", WillOpen: true}})
	model = next.(wizardModel)
	next, _ = model.Update(signInDoneMsg{attempt: 1, account: Account{Email: "newuser@example.com"}})
	model = next.(wizardModel)
	if model.screen != destinationScreen {
		t.Fatalf("after sign-in completed: screen = %v", model.screen)
	}

	model, _ = advanceWizard(t, model, "enter")
	if model.screen != confirmScreen || model.result.Destination != DestinationLocal {
		t.Fatalf("after destination: screen = %v destination = %q", model.screen, model.result.Destination)
	}
	model, cmd := advanceWizard(t, model, "enter")
	if !model.result.Completed || cmd == nil {
		t.Fatalf("result = %#v", model.result)
	}
	if model.result.SignedInEmail != "newuser@example.com" {
		t.Fatalf("the signed-in account should reach the caller: %#v", model.result)
	}
}

// esc then enter: the cancelled attempt's completion must not be shown as a
// failure of the new one.
func TestStaleSignInDoesNotPoisonRetry(t *testing.T) {
	model := signInWizard(t, neverReturns)
	model.screen = signInScreen
	model, _ = advanceWizard(t, model, "enter") // attempt A
	model, _ = advanceWizard(t, model, "esc")   // cancel A, back to sign-in
	model, _ = advanceWizard(t, model, "enter") // attempt B
	t.Cleanup(func() { model.stopSignIn() })

	// A's goroutine now notices the cancellation and reports it, late.
	next, _ := model.Update(signInDoneMsg{attempt: 1, err: context.Canceled})
	model = next.(wizardModel)

	if model.screen == signInFailedScreen {
		t.Fatalf("a cancelled attempt was shown as a failure of the retry: err=%v", model.signInErr)
	}
	if model.screen != signInWaitScreen {
		t.Fatalf("screen = %v, want still waiting on the retry", model.screen)
	}
	if model.signInErr != nil {
		t.Fatalf("signInErr = %v, want none", model.signInErr)
	}

	// And B's own result, attempt 2, still lands.
	next, _ = model.Update(signInDoneMsg{attempt: 2, err: errors.New("connection refused")})
	model = next.(wizardModel)
	if model.screen != signInFailedScreen {
		t.Fatalf("the current attempt's failure was dropped: screen = %v", model.screen)
	}
}

// An expired session still carries a user, so Email is populated while SignedIn is
// false. Finishing without an account must not then show that stale identity.
func TestFinishingWithoutAnAccountDropsALeftoverIdentity(t *testing.T) {
	model := signInWizard(t, neverReturns)
	model.options.Email = "stale@example.com" // from an expired session
	model.options.OfferManaged = true
	model.screen = signInScreen

	model, _ = advanceWizard(t, model, "enter")
	t.Cleanup(func() { model.stopSignIn() })
	next, _ := model.Update(signInDoneMsg{attempt: 1, err: errors.New("connection refused")})
	model = next.(wizardModel)

	for i, choice := range model.recoveryChoices() {
		if choice.id == "local" {
			model.selected = i
		}
	}
	model, _ = advanceWizard(t, model, "enter")

	if model.options.Email != "" {
		t.Fatalf("a leftover identity survived the skip: %q", model.options.Email)
	}
	view := strings.Join(strings.Fields(model.View()), " ")
	if strings.Contains(view, "stale@example.com") {
		t.Fatalf("confirm screen shows an account the user did not sign in as:\n%s", view)
	}
	if strings.Contains(view, "Account:") {
		t.Fatalf("confirm screen shows an account line with no account:\n%s", view)
	}
	if !strings.Contains(view, "not signed in") {
		t.Fatalf("confirm screen should say there is no account:\n%s", view)
	}
}

// --no-browser means no browser is opened, so the first screen must not promise
// one. An SSH or headless user would otherwise go looking for a window that never
// appears, and only learn the truth on the next screen.
func TestSignInScreenHonorsNoBrowser(t *testing.T) {
	withBrowser := newWizardModel(WizardOptions{OfferManaged: true})
	withBrowser.screen = signInScreen
	withBrowser.width, withBrowser.height = 100, 30
	view := strings.Join(strings.Fields(withBrowser.View()), " ")
	if !strings.Contains(view, "will open beacon.sh in your browser") || !strings.Contains(view, "enter open beacon.sh") {
		t.Fatalf("default sign-in screen = %s", view)
	}

	headless := newWizardModel(WizardOptions{OfferManaged: true, NoBrowser: true})
	headless.screen = signInScreen
	headless.width, headless.height = 100, 30
	view = strings.Join(strings.Fields(headless.View()), " ")
	if strings.Contains(view, "will open beacon.sh in your browser") {
		t.Fatalf("--no-browser screen still promises to open a browser:\n%s", view)
	}
	for _, want := range []string{"URL for you to open", "enter show the sign-in URL"} {
		if !strings.Contains(view, want) {
			t.Fatalf("--no-browser screen missing %q:\n%s", want, view)
		}
	}
	// Either way the screen offers no way around signing in.
	for _, unwanted := range []string{"without an account", "Local only"} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("sign-in screen must not advertise %q:\n%s", unwanted, view)
		}
	}
}
