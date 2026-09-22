package onboarding

import (
	"strings"
	"testing"

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
	if view := model.View(); !strings.Contains(view, "does not send telemetry") {
		t.Fatalf("sign-in disclosure missing:\n%s", view)
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
		"sends new telemetry to beacon.sh",
		"Nothing is forwarded",
		"beacon endpoint connect",
		// The disclosure has to describe what actually ships. The runtime source is
		// read_from = "end", so pre-connect runtime events stay local -- but the
		// inventory source is read_from = "beginning", so the existing snapshot does
		// upload. The screen used to flatly claim nothing existing is uploaded.
		"Runtime events recorded before you connect stay on this machine",
		"inventory snapshot",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("managed disclosure missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Existing local history is not uploaded") {
		t.Fatalf("the disclosure must not claim existing history is never uploaded; inventory is:\n%s", view)
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
	for _, want := range []string{"searchable", "Unlimited retention", "analytics"} {
		if !strings.Contains(managedDetail, want) {
			t.Fatalf("managed detail missing %q: %q", want, managedDetail)
		}
	}
	localLabel, localDetail := destinationCopy(DestinationLocal)
	if !strings.Contains(localLabel, "Local only") {
		t.Fatalf("local label = %q", localLabel)
	}
	// Local is a supported end state, not a penalty: it says where data lives and
	// what the user takes on, and does not editorialize.
	if !strings.Contains(localDetail, "stays in ~/.beacon") {
		t.Fatalf("local detail should say where data lives: %q", localDetail)
	}
	for _, unwanted := range []string{"Opt out", "Nothing is sent"} {
		if strings.Contains(localDetail, unwanted) {
			t.Fatalf("local detail should read as a peer choice, found %q: %q", unwanted, localDetail)
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
	for _, want := range []string{"No backend to run.", "limited to this device."} {
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
