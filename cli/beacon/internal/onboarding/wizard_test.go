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
	for _, want := range []string{"sends new telemetry to beacon.sh", "Nothing is forwarded", "beacon endpoint connect", "Existing local history is not uploaded"} {
		if !strings.Contains(view, want) {
			t.Fatalf("managed disclosure missing %q:\n%s", want, view)
		}
	}
	model, _ = advanceWizard(t, model, "enter")
	if model.screen != confirmScreen {
		t.Fatalf("screen = %v, want confirmation", model.screen)
	}
	model, _ = advanceWizard(t, model, "enter")
	if !model.result.Completed {
		t.Fatalf("result = %#v", model.result)
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
