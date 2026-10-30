package onboarding

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// runPicker feeds raw terminal bytes through bubbletea's own key parser into a three-option
// picker, as a terminal would, and returns the model it ended with.
func runPicker(t *testing.T, input string) pickerModel {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	program := tea.NewProgram(newPickerModel(choices([]string{"One", "Two", "Three"}), false, 0),
		tea.WithContext(ctx), tea.WithInput(strings.NewReader(input)), tea.WithOutput(io.Discard), tea.WithoutSignalHandler())
	final, err := program.Run()
	if err != nil {
		t.Fatalf("picker did not finish on %q: %v", input, err)
	}
	return final.(pickerModel)
}

func TestPickerKeys(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  int // -1 is cancelled
	}{
		{"enter picks the first option", "\r", 0},
		{"newline picks the first option", "\n", 0},
		{"down arrow", "\x1b[B\r", 1},
		{"up arrow wraps to the last option", "\x1b[A\r", 2},
		{"vim j", "j\r", 1},
		{"vim k wraps", "k\r", 2},
		{"digit picks directly", "2", 1},
		{"last digit", "3", 2},
		{"digit past the end is ignored", "4\r", 0},
		{"zero is not an option", "0\r", 0},
		{"alt+digit is not a number key", "\x1b2\r", 0},
		{"right arrow ignored", "\x1b[C\r", 0},
		{"ctrl-c cancels", "\x03", -1},
		{"ctrl-d cancels", "\x04", -1},
		// Modified arrows carry parameters (Shift+Up is ESC [ 1 ; 2 A) whose digits the
		// picker once read as number keys.
		{"shift+up moves", "\x1b[1;2A\r", 2},
		{"shift+down moves", "\x1b[1;2B\r", 1},
		{"alt+up moves", "\x1b[1;3A\r", 2},
		{"ctrl+down moves", "\x1b[1;5B\r", 1},
		{"ctrl+shift+down moves", "\x1b[1;6B\r", 1},
		{"delete ignored", "\x1b[3~\r", 0},
		{"insert ignored", "\x1b[2~\r", 0},
		{"f2 ignored", "\x1b[12~\r", 0},
		{"f5 ignored", "\x1b[15~\r", 0},
		{"page up ignored", "\x1b[5~\r", 0},
		// A terminal hands over whatever has arrived, not one keystroke at a time: fast
		// typing, a paste, and scripted input all arrive together.
		{"two downs then enter", "\x1b[B\x1b[B\r", 2},
		{"vim keys typed together", "jjk\r", 1},
		{"noise before a digit", "xy2", 1},
		{"nothing survives past a select", "\r3jjj", 0},
		{"nothing survives past a cancel", "\x032\r", -1},
		{"a whole pasted line still selects", "1\nsomebody@example.org\n", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runPicker(t, tc.input).chosen; got != tc.want {
				t.Fatalf("picker chose %d, want %d", got, tc.want)
			}
		})
	}
}

func TestPickerEscCancels(t *testing.T) {
	model, command := newPickerModel(choices([]string{"A", "B"}), false, 0).Update(tea.KeyMsg{Type: tea.KeyEsc})
	if command == nil || model.(pickerModel).chosen != -1 {
		t.Fatalf("esc did not cancel: chosen=%d quit=%v", model.(pickerModel).chosen, command != nil)
	}
}

func TestPickerViewMarksTheSelectedOption(t *testing.T) {
	model := newPickerModel(choices([]string{"Alpha", "Beta", "Gamma"}), false, 0)
	model.selected = 1
	got := model.View()
	if !strings.Contains(got, "❯ Beta") {
		t.Fatalf("selected option is not marked:\n%s", got)
	}
	if strings.Contains(got, "❯ Alpha") || strings.Contains(got, "❯ Gamma") {
		t.Fatalf("more than one option is marked:\n%s", got)
	}
}

func TestPickerViewHonorsColorOff(t *testing.T) {
	plain := newPickerModel(choices([]string{"Alpha", "Beta"}), false, 0).View()
	if strings.Contains(plain, "\x1b") {
		t.Fatalf("colour escapes leaked with colour disabled:\n%q", plain)
	}
	if colored := newPickerModel(choices([]string{"Alpha", "Beta"}), true, 0).View(); !strings.Contains(colored, colorAccnt) {
		t.Fatalf("colour was requested but not emitted:\n%q", colored)
	}
}

// A detail line is drawn dim under its option, before the next one.
func TestPickerViewDrawsDetailsUnderTheirOptions(t *testing.T) {
	rows := []choice{{Label: "Alpha", Detail: "first thing"}, {Label: "Beta"}, {Label: "Gamma", Detail: "third thing"}}
	got := newPickerModel(rows, true, 0).View()
	if !strings.Contains(got, "      "+colorDim+"first thing"+colorReset) || !strings.Contains(got, colorDim+"third thing") {
		t.Fatalf("details are not drawn dim under their options:\n%q", got)
	}
	if strings.Index(got, "Alpha") > strings.Index(got, "first thing") || strings.Index(got, "first thing") > strings.Index(got, "Beta") {
		t.Fatalf("detail must sit between its option and the next:\n%s", got)
	}
	if lines := strings.Count(got, "\n") + 1; lines != 6 {
		t.Fatalf("view is %d lines, want 3 options + 2 details + hint", lines)
	}
}

// A line longer than the terminal is clipped, never wrapped: a wrapped line would throw
// off the line count that every redraw rewinds by.
func TestPickerViewClipsToTerminalWidth(t *testing.T) {
	rows := []choice{{Label: "Alpha", Detail: strings.Repeat("d", 100)}, {Label: strings.Repeat("L", 100)}}
	got := newPickerModel(rows, false, 40).View()
	for _, line := range strings.Split(got, "\n") {
		if n := len([]rune(line)); n >= 40 {
			t.Fatalf("line is %d columns wide on a 40-column terminal; must leave the last column free: %q", n, line)
		}
	}
	if !strings.Contains(got, "…") {
		t.Fatalf("clipped lines should end with an ellipsis:\n%q", got)
	}
	if clip("short", 0) != "short" || clip("short", 10) != "short" || clip("abcdef", 4) != "abc…" {
		t.Fatal("clip: unknown width leaves text alone; otherwise width-1 runes plus an ellipsis")
	}
}

// Once answered the view is empty, which erases the menu so the caller's confirmation line
// lands where it was.
func TestPickerViewIsEmptyOnceAnswered(t *testing.T) {
	model, _ := newPickerModel(choices([]string{"A", "B"}), false, 0).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := model.View(); got != "" {
		t.Fatalf("answered picker still draws:\n%q", got)
	}
}

// Raw mode is impossible on a plain file, and that must degrade to the typed menu
// rather than failing the install.
func TestSelectOptionFallsBackWithoutATerminal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-tty")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer file.Close()

	if _, err := selectOption(file, io.Discard, choices([]string{"A", "B"}), false); !errors.Is(err, errRawUnavailable) {
		t.Fatalf("selectOption error = %v, want errRawUnavailable", err)
	}
}

func TestTerminalFileRejectsNonFiles(t *testing.T) {
	if _, ok := terminalFile(strings.NewReader("x")); ok {
		t.Fatalf("a strings.Reader was reported as a terminal")
	}
	path := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	file, _ := os.Open(path)
	defer file.Close()
	if _, ok := terminalFile(file); ok {
		t.Fatalf("a regular file was reported as a terminal")
	}
}

func TestSupportsColorRespectsNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if supportsColor(os.Stdout) {
		t.Fatalf("NO_COLOR was set but colour was still enabled")
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	if supportsColor(os.Stdout) {
		t.Fatalf("TERM=dumb but colour was still enabled")
	}
}
