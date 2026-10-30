package onboarding

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

// errRawUnavailable means the terminal could not be put into raw mode, so the caller
// should fall back to the typed numbered menu. Plenty of real environments cannot do
// raw input -- CI pseudo-terminals, some IDE consoles, unusual SSH setups -- and none
// of them should lose the ability to answer.
var errRawUnavailable = errors.New("raw terminal mode unavailable")

const (
	colorReset = "\x1b[0m"
	colorDim   = "\x1b[2m"
	colorAccnt = "\x1b[38;5;141m"   // the Beacon purple used by the root splash
	colorTitle = "\x1b[1;38;5;141m" // bold purple: every question title, so the prompt reads as one piece
	colorGreen = "\x1b[32m"
)

// choice is one row of a picker: a label and an optional one-line detail drawn dim
// beneath it, for questions whose options need a sentence of explanation.
type choice struct {
	Label  string
	Detail string
}

func choices(labels []string) []choice {
	rows := make([]choice, len(labels))
	for i, label := range labels {
		rows[i] = choice{Label: label}
	}
	return rows
}

// menuStep is how far a key moves the cursor of an arrow-key menu: -1 up, 1 down, 0 for
// any other key. An arrow moves whatever modifier is held; terminals send Shift+Up as
// ESC [ 1 ; 2 A, and the picker once read those parameter digits as number keys.
func menuStep(msg tea.KeyMsg) int {
	switch msg.Type {
	case tea.KeyUp, tea.KeyShiftUp, tea.KeyCtrlUp, tea.KeyCtrlShiftUp:
		return -1
	case tea.KeyDown, tea.KeyShiftDown, tea.KeyCtrlDown, tea.KeyCtrlShiftDown:
		return 1
	case tea.KeyRunes:
		if len(msg.Runes) == 1 && !msg.Alt {
			return runeStep(msg.Runes[0])
		}
	}
	return 0
}

// runeStep is menuStep for the vim keys.
func runeStep(r rune) int {
	switch r {
	case 'k', 'K':
		return -1
	case 'j', 'J':
		return 1
	}
	return 0
}

// pickerModel is the inline arrow-key menu. chosen stays -1 until an option is picked,
// so a model that quit with it unset was cancelled.
type pickerModel struct {
	options  []choice
	color    bool
	width    int
	selected int
	chosen   int
	done     bool
}

func newPickerModel(options []choice, color bool, width int) pickerModel {
	return pickerModel{options: options, color: color, width: width, chosen: -1}
}

// selectOption runs the arrow-key menu inline, below whatever the caller printed, and
// returns the index the user chose. The menu is erased on every exit, leaving the cursor
// where it began so the caller's confirmation line takes its place.
func selectOption(in *os.File, out io.Writer, options []choice, color bool) (int, error) {
	fd := int(in.Fd())
	if !term.IsTerminal(fd) {
		return 0, errRawUnavailable
	}
	// The size message arrives after the first draw, and a line that wrapped in it would
	// throw off the line count every redraw rewinds by.
	width, _, err := term.GetSize(fd)
	if err != nil {
		width = 0
	}
	program := tea.NewProgram(newPickerModel(options, color, width),
		tea.WithInput(in), tea.WithOutput(out), tea.WithoutBracketedPaste())
	final, err := program.Run()
	if err != nil {
		// Run fails without ErrProgramKilled only while setting the terminal up, before
		// anything is drawn: raw mode was refused.
		if !errors.Is(err, tea.ErrProgramKilled) {
			return 0, errRawUnavailable
		}
		return 0, ErrPromptAborted
	}
	model := final.(pickerModel)
	if model.chosen < 0 {
		return 0, ErrPromptAborted
	}
	return model.chosen, nil
}

func (m pickerModel) Init() tea.Cmd {
	return nil
}

func (m pickerModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	// Keys read in the same burst as the answer still arrive after it; none may change it.
	if m.done {
		return m, nil
	}
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "ctrl+d", "esc":
			m.done = true
			return m, tea.Quit
		case "enter", "ctrl+j":
			m.chosen, m.done = m.selected, true
			return m, tea.Quit
		}
		// Keys typed faster than they are read arrive as one message, so each rune is
		// its own keypress: `jj` moves twice and `x2` still picks the second option.
		if msg.Type == tea.KeyRunes && !msg.Alt {
			for _, r := range msg.Runes {
				// A number key picks that entry outright: anyone who already knows the
				// menu should not have to arrow down to their answer.
				if r >= '1' && r <= '9' && int(r-'0') <= len(m.options) {
					m.chosen, m.done = int(r-'1'), true
					return m, tea.Quit
				}
				m.move(runeStep(r))
			}
			return m, nil
		}
		m.move(menuStep(msg))
	}
	return m, nil
}

func (m *pickerModel) move(step int) {
	n := len(m.options)
	m.selected = (m.selected + step + n) % n
}

// View draws the option list plus its key hint. Every line is clipped to one column less
// than the terminal, so nothing wraps even on terminals that wrap eagerly at the last
// column. Once an answer is in, the view is empty, which erases the menu.
func (m pickerModel) View() string {
	if m.done {
		return ""
	}
	accent, dimmed, plain := "", "", ""
	if m.color {
		accent, dimmed, plain = colorAccnt, colorDim, colorReset
	}
	var b strings.Builder
	for i, option := range m.options {
		if i == m.selected {
			fmt.Fprintf(&b, "  %s❯ %s%s\n", accent, clip(option.Label, m.width-5), plain)
		} else {
			fmt.Fprintf(&b, "    %s\n", clip(option.Label, m.width-5))
		}
		if option.Detail != "" {
			fmt.Fprintf(&b, "      %s%s%s\n", dimmed, clip(option.Detail, m.width-7), plain)
		}
	}
	fmt.Fprintf(&b, "  %s↑/↓ move · ↵ select · ^C cancel%s", dimmed, plain)
	return b.String()
}

// clip shortens text to fit in width columns, ending it with an ellipsis. A width of zero
// or less means the terminal size is unknown and the text is left alone.
func clip(text string, width int) string {
	if width <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}

// terminalFile reports whether r is a real terminal that raw mode can drive.
func terminalFile(r io.Reader) (*os.File, bool) {
	file, ok := r.(*os.File)
	if !ok {
		return nil, false
	}
	return file, term.IsTerminal(int(file.Fd()))
}

// supportsColor mirrors the CLI's existing convention for the root splash.
func supportsColor(out io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return false
	}
	file, ok := out.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}
