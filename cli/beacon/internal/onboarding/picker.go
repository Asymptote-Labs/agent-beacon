package onboarding

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// errRawUnavailable means the terminal could not be put into raw mode, so the caller
// should fall back to the typed numbered menu. Plenty of real environments cannot do
// raw input -- CI pseudo-terminals, some IDE consoles, unusual SSH setups -- and none
// of them should lose the ability to answer.
var errRawUnavailable = errors.New("raw terminal mode unavailable")

// ANSI control sequences. Only the handful worth using: anything more elaborate
// misbehaves across the range of terminals people actually run.
const (
	ansiUp        = "\x1b[%dA"
	ansiClearLine = "\r\x1b[K"
	ansiHideCur   = "\x1b[?25l"
	ansiShowCur   = "\x1b[?25h"
)

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

// renderedLines is how many terminal lines a picker draws: one per option, one per
// detail, and the key hint.
func renderedLines(options []choice) int {
	n := 1
	for _, option := range options {
		n++
		if option.Detail != "" {
			n++
		}
	}
	return n
}

// keyAction is what a keypress means to the picker.
type keyAction int

const (
	keyUp keyAction = iota
	keyDown
	keySelect
	keyPick
	keyAbort
)

// keyEvent is one decoded keypress. index is meaningful only for keyPick.
type keyEvent struct {
	action keyAction
	index  int
}

// selectOption renders an arrow-key menu and returns the index the user chose.
//
// The terminal is restored on every exit path, including Ctrl-C: raw mode disables
// the interrupt signal, so leaving without restoring would hand the user back a shell
// with no echo.
func selectOption(in *os.File, out io.Writer, options []choice, color bool) (int, error) {
	fd := int(in.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return 0, errRawUnavailable
	}
	defer func() {
		_ = term.Restore(fd, state)
		fmt.Fprint(out, ansiShowCur)
	}()

	fmt.Fprint(out, ansiHideCur)

	// Every drawn line is clipped to the terminal width so nothing wraps: the redraw and
	// the final clear count lines, and a wrapped line would make them eat whatever the
	// caller printed above the menu.
	width, _, sizeErr := term.GetSize(fd)
	if sizeErr != nil {
		width = 0
	}

	selected := 0
	render(out, options, selected, color, true, width)

	buf := make([]byte, 32)
	for {
		n, err := in.Read(buf)
		if err != nil || n == 0 {
			clear(out, renderedLines(options))
			return 0, ErrPromptAborted
		}
		moved := false
		for _, event := range decodeKeys(buf[:n], len(options)) {
			switch event.action {
			case keyUp:
				selected = (selected - 1 + len(options)) % len(options)
				moved = true
			case keyDown:
				selected = (selected + 1) % len(options)
				moved = true
			case keySelect:
				clear(out, renderedLines(options))
				return selected, nil
			case keyPick:
				clear(out, renderedLines(options))
				return event.index, nil
			case keyAbort:
				clear(out, renderedLines(options))
				return 0, ErrPromptAborted
			}
		}
		if moved {
			render(out, options, selected, color, false, width)
		}
	}
}

// decodeKeys turns a single raw read into the keypresses it contains.
//
// One Read can carry several keys, because a terminal hands over whatever has arrived
// rather than one keystroke at a time: fast typing, a paste, and scripted input all
// produce multi-byte reads. An earlier version matched only an exact one-byte read or
// a three-byte escape, so an ordinary `1\n` was silently discarded and the picker sat
// waiting for a key the user had already pressed.
func decodeKeys(buf []byte, total int) []keyEvent {
	var events []keyEvent
	for i := 0; i < len(buf); {
		if buf[i] == 0x1b {
			// Arrow keys. Anything else starting with escape is a sequence this picker
			// does not use; drop the remainder rather than misread its bytes as
			// separate keypresses.
			if i+2 < len(buf) && buf[i+1] == '[' {
				switch buf[i+2] {
				case 'A':
					events = append(events, keyEvent{keyUp, 0})
				case 'B':
					events = append(events, keyEvent{keyDown, 0})
				}
				i += 3
				continue
			}
			break
		}
		if event, ok := decodeByte(buf[i], total); ok {
			events = append(events, event)
			// Selecting or aborting ends the menu, so nothing after it can matter.
			if event.action != keyUp && event.action != keyDown {
				return events
			}
		}
		i++
	}
	return events
}

// decodeByte maps a single non-escape byte onto an action.
func decodeByte(c byte, total int) (keyEvent, bool) {
	switch {
	case c == 'k' || c == 'K':
		return keyEvent{keyUp, 0}, true
	case c == 'j' || c == 'J':
		return keyEvent{keyDown, 0}, true
	case c == '\r' || c == '\n':
		return keyEvent{keySelect, 0}, true
	// A number key picks that entry outright: anyone who already knows the menu
	// should not have to arrow down to their answer.
	case c >= '1' && c <= '9' && int(c-'0') <= total:
		return keyEvent{keyPick, int(c - '1')}, true
	case c == 0x03 || c == 0x04: // Ctrl-C, Ctrl-D
		return keyEvent{keyAbort, 0}, true
	default:
		return keyEvent{}, false
	}
}

// render draws the option list plus its key hint, overwriting the previous draw. width is
// the terminal width in columns, or 0 when unknown; every line is clipped to one column
// less than that, so the draw occupies exactly renderedLines rows even on terminals that
// wrap eagerly at the last column.
func render(out io.Writer, options []choice, selected int, color bool, first bool, width int) {
	if !first {
		fmt.Fprintf(out, ansiUp, renderedLines(options))
	}
	accent, dimmed, clear := "", "", ""
	if color {
		accent, dimmed, clear = colorAccnt, colorDim, colorReset
	}
	for i, option := range options {
		fmt.Fprint(out, ansiClearLine)
		if i == selected {
			fmt.Fprintf(out, "  %s❯ %s%s\r\n", accent, clip(option.Label, width-5), clear)
		} else {
			fmt.Fprintf(out, "    %s\r\n", clip(option.Label, width-5))
		}
		if option.Detail != "" {
			fmt.Fprint(out, ansiClearLine)
			fmt.Fprintf(out, "      %s%s%s\r\n", dimmed, clip(option.Detail, width-7), clear)
		}
	}
	fmt.Fprint(out, ansiClearLine)
	fmt.Fprintf(out, "  %s↑/↓ move · ↵ select · ^C cancel%s\r\n", dimmed, clear)
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

// clear erases the last n rendered lines and parks the cursor at their start.
func clear(out io.Writer, n int) {
	fmt.Fprintf(out, ansiUp, n)
	for i := 0; i < n; i++ {
		fmt.Fprint(out, ansiClearLine)
		if i < n-1 {
			fmt.Fprint(out, "\r\n")
		}
	}
	fmt.Fprintf(out, ansiUp, n-1)
	fmt.Fprint(out, "\r")
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
