package traceview

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSearchControlCQuitsTraceBrowser(t *testing.T) {
	model := NewModel(fixtureStore())
	model.loading = false
	next, _ := model.Update(key("/"))
	model = next.(Model)
	if !model.searching {
		t.Fatal("slash did not enter search")
	}
	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("Ctrl+C while searching did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("Ctrl+C returned a command other than quit")
	}
	// q remains ordinary search input; Escape cancels search without quitting.
	next, cmd = model.Update(key("q"))
	model = next.(Model)
	if cmd != nil || model.searchInput != "q" || !model.searching {
		t.Fatal("q no longer stays in the search input")
	}
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(Model)
	if cmd != nil || model.searching {
		t.Fatal("Escape no longer cancels search without quitting")
	}
}
