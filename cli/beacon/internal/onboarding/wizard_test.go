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
	// The disclosure has to describe what actually ships. The runtime source is
	// read_from = "end", so pre-connect runtime events stay local -- but the
	// inventory source is read_from = "beginning", so the existing snapshot does
	// upload. The screen used to flatly claim nothing existing is uploaded.
	view := strings.Join(strings.Fields(model.View()), " ")
	for _, false_ := range []string{
		"Existing local history is not uploaded",
		"What is already on this machine stays here",
		"Nothing already recorded",
	} {
		if strings.Contains(view, false_) {
			t.Fatalf("the disclosure must not claim existing data stays local; inventory does not:\n%s", view)
		}
	}
	model, _ = advanceWizard(t, model, "enter")
	if model.screen != privacyScreen {
		t.Fatalf("screen = %v, want privacy", model.screen)
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
	for _, want := range []string{"full feature set.", "beacon.sh/privacy", "Your agent activity stays on this machine."} {
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

// bubbletea's alt-screen renderer keeps the last height lines of a frame. On a
// default 80×24 terminal that used to drop the account and the operator line
// while leaving "enter install and connect" on screen.
func TestConfirmScreenFitsDefaultTerminal(t *testing.T) {
	model := newWizardModel(WizardOptions{
		SignedIn:          true,
		Email:             "person@example.com",
		OfferManaged:      true,
		OfferAgentTools:   true,
		PresetDestination: DestinationAsymptote,
		PresetPrivacyMode: "standard",
		BackfillDays:      30,
	})
	model.width, model.height = 80, 24
	model.screen = confirmScreen
	model.result.Destination = DestinationAsymptote
	model.result.PrivacyMode = "standard"

	view := model.View()
	lines := strings.Split(view, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > 24 {
		t.Fatalf("confirm screen is %d lines at 80 columns; an 80×24 terminal drops the top:\n%s", len(lines), view)
	}
	for _, want := range []string{
		"Account: person@example.com",
		"operated by Asymptote Labs",
		"This installs Beacon and starts forwarding to beacon.sh.",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("80×24 confirm screen missing %q:\n%s", want, view)
		}
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

// The browser that opens is the default one, which is not always the one signed in to
// beacon.sh. Once a browser has been opened, the wait says where the URL belongs; when none
// was, there is no other browser to warn about.
func TestSignInWaitSaysWhichBrowserTheURLBelongsIn(t *testing.T) {
	url := "https://beacon.sh/cli/auth?port=54123&state=abc"
	for _, tc := range []struct {
		name   string
		prompt SignInPrompt
		want   bool
	}{
		{"browser opened", SignInPrompt{URL: url, WillOpen: true}, true},
		{"browser failed", SignInPrompt{URL: url, WillOpen: true, BrowserErr: errors.New("no xdg-open")}, false},
		{"no browser", SignInPrompt{URL: url}, false},
	} {
		model := signInWizard(t, neverReturns)
		model.screen = signInScreen
		model, _ = advanceWizard(t, model, "enter")
		next, _ := model.Update(signInPromptMsg{attempt: 1, prompt: tc.prompt})
		model = next.(wizardModel)
		model.stopSignIn()

		flat := strings.Join(strings.Fields(model.View()), " ")
		got := strings.Contains(flat, "Open it in the browser where you are signed in to beacon.sh. If a different browser opened, copy the URL there.")
		if got != tc.want {
			t.Fatalf("%s: hint shown = %t, want %t:\n%s", tc.name, got, tc.want, model.View())
		}
		if !strings.Contains(model.View(), url) {
			t.Fatalf("%s: the URL must stay on one unbroken line:\n%s", tc.name, model.View())
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
	if flat := strings.Join(strings.Fields(model.View()), " "); !strings.Contains(flat, "connection refused") {
		t.Fatalf("recovery screen does not show why sign-in failed:\n%s", flat)
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

func TestManagedWizardOffersCloudMCPAndSkillsSelected(t *testing.T) {
	model := newWizardModel(WizardOptions{
		SignedIn:        true,
		Email:           "person@example.com",
		OfferManaged:    true,
		OfferAgentTools: true,
	})
	model.width, model.height = 100, 40
	model, _ = advanceWizard(t, model, "enter") // welcome
	model, _ = advanceWizard(t, model, "enter") // Beacon Cloud
	model, _ = advanceWizard(t, model, "enter") // disclosure
	model, _ = advanceWizard(t, model, "enter") // privacy
	if model.screen != agentToolsScreen {
		t.Fatalf("screen = %v, want agent tools", model.screen)
	}
	view := model.View()
	for _, want := range []string{"[x] Beacon Cloud MCP", "[x] Beacon agent skills", "space"} {
		if !strings.Contains(view, want) {
			t.Fatalf("agent tools screen missing %q:\n%s", want, view)
		}
	}
	model, _ = advanceWizard(t, model, "enter")
	if view := model.View(); !strings.Contains(view, "Agent tools: Beacon Cloud MCP, Beacon agent skills") {
		t.Fatalf("confirm screen should list the agent tools:\n%s", view)
	}
	model, _ = advanceWizard(t, model, "enter")
	if !model.result.Completed || !model.result.ConnectCloudMCP || !model.result.InstallSkills {
		t.Fatalf("result = %#v", model.result)
	}
}

func TestManagedWizardCanTurnOffCloudMCP(t *testing.T) {
	model := newWizardModel(WizardOptions{
		SignedIn:          true,
		OfferManaged:      true,
		OfferAgentTools:   true,
		DestinationOnly:   true,
		PresetDestination: DestinationAsymptote,
	})
	model.width, model.height = 100, 40
	model, _ = advanceWizard(t, model, "enter") // disclosure
	model, _ = advanceWizard(t, model, "enter") // privacy
	model, _ = advanceWizard(t, model, " ")
	if model.result.ConnectCloudMCP || !model.result.InstallSkills {
		t.Fatalf("space should turn off Beacon Cloud MCP only: %#v", model.result)
	}
	if view := model.View(); !strings.Contains(view, "[ ] Beacon Cloud MCP") {
		t.Fatalf("turned-off tool should render unchecked:\n%s", view)
	}
	model, _ = advanceWizard(t, model, "j")
	model, _ = advanceWizard(t, model, " ")
	model, _ = advanceWizard(t, model, "enter")
	if view := model.View(); !strings.Contains(view, "Agent tools: none") {
		t.Fatalf("confirm should say no agent tools:\n%s", view)
	}
	model, _ = advanceWizard(t, model, "enter")
	if !model.result.Completed || model.result.ConnectCloudMCP || model.result.InstallSkills {
		t.Fatalf("result = %#v", model.result)
	}
}

func TestLocalWizardOffersOnlySkills(t *testing.T) {
	model := newWizardModel(WizardOptions{
		SignedIn:        true,
		OfferManaged:    true,
		OfferAgentTools: true,
	})
	model.width, model.height = 100, 40
	model, _ = advanceWizard(t, model, "enter") // welcome
	model, _ = advanceWizard(t, model, "j")
	model, _ = advanceWizard(t, model, "enter") // Local
	if model.screen != agentToolsScreen {
		t.Fatalf("screen = %v, want agent tools", model.screen)
	}
	view := model.View()
	if strings.Contains(view, "Cloud MCP") || !strings.Contains(view, "[x] Beacon agent skills") {
		t.Fatalf("local agent tools screen should offer only the skills:\n%s", view)
	}
	model, _ = advanceWizard(t, model, "enter")
	model, _ = advanceWizard(t, model, "enter")
	if !model.result.Completed || model.result.ConnectCloudMCP || !model.result.InstallSkills {
		t.Fatalf("result = %#v", model.result)
	}
}

func TestFinishingWithoutAnAccountStillOffersSkills(t *testing.T) {
	model := newWizardModel(WizardOptions{OfferManaged: true, OfferAgentTools: true, NoDisplay: errors.New("no display")})
	model.width, model.height = 100, 40
	model, _ = advanceWizard(t, model, "enter")
	if model.screen != signInFailedScreen {
		t.Fatalf("screen = %v, want recovery", model.screen)
	}
	model, _ = advanceWizard(t, model, "enter") // finish without an account
	if model.screen != agentToolsScreen || !model.result.InstallSkills {
		t.Fatalf("screen = %v result = %#v", model.screen, model.result)
	}
	if view := model.View(); strings.Contains(view, "Cloud MCP") {
		t.Fatalf("no Cloud MCP without Beacon Cloud:\n%s", view)
	}
	model, _ = advanceWizard(t, model, "enter")
	model, _ = advanceWizard(t, model, "enter")
	if model.result.ConnectCloudMCP {
		t.Fatalf("result = %#v", model.result)
	}
}

func TestAgentToolsScreenIsSkippedWhenNotOffered(t *testing.T) {
	model := newWizardModel(WizardOptions{SignedIn: true, OfferManaged: true, DestinationOnly: true, PresetDestination: DestinationLocal})
	if model.screen != confirmScreen || model.result.ConnectCloudMCP || model.result.InstallSkills {
		t.Fatalf("screen = %v result = %#v", model.screen, model.result)
	}
}

// Confirming Beacon Cloud also uploads the session backfill, so the confirm screen, which is the
// consent point, has to say so; and a local choice says what the install reads.
func TestConfirmScreenNamesTheSessionBackfill(t *testing.T) {
	render := func(destination string, days int) string {
		model := newWizardModel(WizardOptions{
			SignedIn:          true,
			Email:             "person@example.com",
			OfferManaged:      true,
			DestinationOnly:   true,
			PresetDestination: destination,
			PresetPrivacyMode: "standard",
			BackfillDays:      days,
		})
		model.width, model.height = 100, 30
		model.screen = confirmScreen
		model.result.Destination = destination
		model.result.PrivacyMode = "standard"
		return strings.Join(strings.Fields(model.View()), " ")
	}
	if view := render(DestinationAsymptote, 30); !strings.Contains(view, "It also uploads your agent sessions from the last 30 days") {
		t.Fatalf("managed confirm screen does not name the backfill:\n%s", view)
	}
	if view := render(DestinationLocal, 30); !strings.Contains(view, "Beacon reads your agent sessions from the last 30 days into its local log.") {
		t.Fatalf("local confirm screen does not name the backfill:\n%s", view)
	}
	for _, destination := range []string{DestinationAsymptote, DestinationLocal} {
		if view := render(destination, 0); strings.Contains(view, "last 30 days") || strings.Contains(view, "agent sessions from") {
			t.Fatalf("confirm screen names a backfill that is off:\n%s", view)
		}
	}
}
