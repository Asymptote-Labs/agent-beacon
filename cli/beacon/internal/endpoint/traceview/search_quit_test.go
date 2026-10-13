package traceview

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

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

func TestSearchControlCExitsNativeProgram(t *testing.T) {
	model := NewModel(fixtureStore())
	model.loading = false
	next, _ := model.Update(key("/"))
	model = next.(Model)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	program := tea.NewProgram(model, tea.WithInput(strings.NewReader("\x03")), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithContext(ctx))
	if _, err := program.Run(); err != nil {
		t.Fatalf("real input loop did not quit on Ctrl+C while searching: %v", err)
	}
}
