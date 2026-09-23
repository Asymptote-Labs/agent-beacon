package onboarding

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var errSSHSession = errors.New("no browser can be opened on this machine: this is an SSH session")

// countingSignIn is a sign-in that records every attempt and never finishes on its own.
func countingSignIn(requests chan<- SignInRequest) SignInFunc {
	return func(ctx context.Context, req SignInRequest, _ Reporter) (Account, error) {
		requests <- req
		<-ctx.Done()
		return Account{}, ctx.Err()
	}
}

func recoveryIDs(model wizardModel) []string {
	var ids []string
	for _, choice := range model.recoveryChoices() {
		ids = append(ids, choice.id)
	}
	return ids
}

// With no display, the browser Beacon opens is one nobody sees, and the wizard used to wait five
// minutes on it before saying anything. It now says so before any attempt starts.
func TestNoDisplayGoesStraightToRecovery(t *testing.T) {
	requests := make(chan SignInRequest, 4)
	model := signInWizard(t, countingSignIn(requests))
	model.options.NoDisplay = errSSHSession
	model.screen = welcomeScreen

	model, _ = advanceWizard(t, model, "enter")
	if model.screen == signInWaitScreen {
		t.Fatal("the wizard waited for a browser on a machine with no display")
	}
	if model.screen != signInFailedScreen {
		t.Fatalf("screen = %v, want recovery straight after welcome", model.screen)
	}
	select {
	case req := <-requests:
		t.Fatalf("a sign-in started (%#v) with no display to finish it on", req)
	case <-time.After(50 * time.Millisecond):
	}

	if got, want := strings.Join(recoveryIDs(model), ","), "local,manual,cancel"; got != want {
		t.Fatalf("recovery choices = %s, want %s", got, want)
	}
	flat := strings.Join(strings.Fields(model.View()), " ")
	for _, want := range []string{
		"No browser on this machine",
		"this is an SSH session",
		"redirects a browser back to this machine",
		"Finish setup without an account",
		"Show the sign-in URL",
	} {
		if !strings.Contains(flat, want) {
			t.Fatalf("recovery screen missing %q:\n%s", want, flat)
		}
	}
	if strings.Contains(flat, "Try signing in again") {
		t.Fatalf("retrying cannot find a display, so it must not be offered:\n%s", flat)
	}

	// The default choice finishes locally.
	model, _ = advanceWizard(t, model, "enter")
	if model.screen != confirmScreen || !model.result.WithoutAccount || model.result.Destination != DestinationLocal {
		t.Fatalf("screen = %v result = %#v, want a local finish without an account", model.screen, model.result)
	}
}

// Reinstalling after an earlier setup starts at the sign-in step, and takes the same path.
func TestNoDisplayDestinationOnlyStartsAtRecovery(t *testing.T) {
	model := newWizardModel(WizardOptions{DestinationOnly: true, NoDisplay: errSSHSession, SignIn: neverReturns})
	if model.screen != signInFailedScreen {
		t.Fatalf("screen = %v, want recovery", model.screen)
	}
}

// Showing the URL is the SSH path: the user forwards the callback port and opens the URL on their
// own computer. The command has to be on screen, unwrapped, while the sign-in waits.
func TestNoDisplayShowURLNamesThePortToForward(t *testing.T) {
	requests := make(chan SignInRequest, 4)
	model := signInWizard(t, countingSignIn(requests))
	model.options.NoDisplay = errSSHSession
	model.screen = welcomeScreen
	model, _ = advanceWizard(t, model, "enter")

	for i, id := range recoveryIDs(model) {
		if id == "manual" {
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
			t.Fatalf("request = %#v, want the browser suppressed", req)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("showing the URL never started a sign-in")
	}

	forward := "ssh -L 54123:127.0.0.1:54123 <this machine>"
	next, _ := model.Update(signInPromptMsg{attempt: 1, prompt: SignInPrompt{
		URL:        "https://beacon.sh/cli/auth?port=54123&state=" + strings.Repeat("a", 43),
		SSHForward: forward,
	}})
	model = next.(wizardModel)
	if view := model.View(); !strings.Contains(view, forward) {
		t.Fatalf("waiting screen should show %q on one line:\n%s", forward, view)
	}

	// A failure of that attempt is an ordinary failure: retry is offered again.
	next, _ = model.Update(signInDoneMsg{attempt: 1, err: errors.New("timed out")})
	model = next.(wizardModel)
	if ids := recoveryIDs(model); ids[0] != "retry" {
		t.Fatalf("recovery after a real attempt = %v, want retry first", ids)
	}
}

// --no-browser already asks for the URL, so a missing display changes nothing.
func TestNoBrowserFlagKeepsTheSignInScreenWithoutADisplay(t *testing.T) {
	model := signInWizard(t, neverReturns)
	model.options.NoDisplay = errSSHSession
	model.options.NoBrowser = true
	model.screen = welcomeScreen
	model, _ = advanceWizard(t, model, "enter")
	if model.screen != signInScreen {
		t.Fatalf("screen = %v, want the sign-in screen", model.screen)
	}
}
