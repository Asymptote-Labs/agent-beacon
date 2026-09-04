package onboarding

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeKeysSingleKeypress(t *testing.T) {
	const total = 3
	cases := []struct {
		name  string
		bytes []byte
		want  []keyEvent
	}{
		{"up arrow", []byte{0x1b, '[', 'A'}, []keyEvent{{keyUp, 0}}},
		{"down arrow", []byte{0x1b, '[', 'B'}, []keyEvent{{keyDown, 0}}},
		{"vim k", []byte{'k'}, []keyEvent{{keyUp, 0}}},
		{"vim j", []byte{'j'}, []keyEvent{{keyDown, 0}}},
		{"enter", []byte{'\r'}, []keyEvent{{keySelect, 0}}},
		{"newline", []byte{'\n'}, []keyEvent{{keySelect, 0}}},
		{"digit picks directly", []byte{'2'}, []keyEvent{{keyPick, 1}}},
		{"last digit", []byte{'3'}, []keyEvent{{keyPick, 2}}},
		{"digit past the end is ignored", []byte{'4'}, nil},
		{"zero is not an option", []byte{'0'}, nil},
		{"ctrl-c aborts", []byte{0x03}, []keyEvent{{keyAbort, 0}}},
		{"ctrl-d aborts", []byte{0x04}, []keyEvent{{keyAbort, 0}}},
		{"empty read yields nothing", []byte{}, nil},
		{"unknown letter ignored", []byte{'z'}, nil},
		{"right arrow ignored", []byte{0x1b, '[', 'C'}, nil},
		{"bare escape ignored", []byte{0x1b}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertEvents(t, decodeKeys(tc.bytes, total), tc.want)
		})
	}
}

// A terminal hands over whatever has arrived, not one keystroke at a time. Fast
// typing, a paste, and scripted input all produce multi-byte reads -- and an earlier
// version dropped them silently, leaving the picker waiting on a key already pressed.
func TestDecodeKeysMultipleKeysInOneRead(t *testing.T) {
	const total = 3
	cases := []struct {
		name  string
		bytes []byte
		want  []keyEvent
	}{
		{"digit then newline", []byte("1\n"), []keyEvent{{keyPick, 0}}},
		{"digit then carriage return", []byte("2\r"), []keyEvent{{keyPick, 1}}},
		{"two downs then enter", append(append([]byte{0x1b, '[', 'B'}, 0x1b, '[', 'B'), '\r'),
			[]keyEvent{{keyDown, 0}, {keyDown, 0}, {keySelect, 0}}},
		{"vim keys then enter", []byte("jjk\r"),
			[]keyEvent{{keyDown, 0}, {keyDown, 0}, {keyUp, 0}, {keySelect, 0}}},
		{"noise before a digit", []byte("xy2"), []keyEvent{{keyPick, 1}}},
		{"nothing survives past a select", []byte("\r3jjj"), []keyEvent{{keySelect, 0}}},
		{"nothing survives past an abort", []byte{0x03, '2', '\r'}, []keyEvent{{keyAbort, 0}}},
		{"a whole pasted line still selects", []byte("1\nsomebody@example.org\n"), []keyEvent{{keyPick, 0}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertEvents(t, decodeKeys(tc.bytes, total), tc.want)
		})
	}
}

func assertEvents(t *testing.T, got, want []keyEvent) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("decodeKeys returned %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("decodeKeys returned %v, want %v", got, want)
		}
	}
}

func TestRenderMarksTheSelectedOption(t *testing.T) {
	var out bytes.Buffer
	render(&out, choices([]string{"Alpha", "Beta", "Gamma"}), 1, false, true)
	got := out.String()

	if !strings.Contains(got, "❯ Beta") {
		t.Fatalf("selected option is not marked:\n%s", got)
	}
	if strings.Contains(got, "❯ Alpha") || strings.Contains(got, "❯ Gamma") {
		t.Fatalf("more than one option is marked:\n%s", got)
	}
	if !strings.Contains(got, "↑/↓ move") || !strings.Contains(got, "^C cancel") {
		t.Fatalf("key hint is missing:\n%s", got)
	}
}

// The first draw must not move the cursor up, or it will eat whatever the caller
// printed above the menu.
func TestRenderFirstDrawDoesNotMoveTheCursor(t *testing.T) {
	var first, redraw bytes.Buffer
	render(&first, choices([]string{"A", "B"}), 0, false, true)
	render(&redraw, choices([]string{"A", "B"}), 0, false, false)

	if strings.Contains(first.String(), "\x1b[3A") {
		t.Fatalf("first draw moved the cursor up:\n%q", first.String())
	}
	if !strings.HasPrefix(redraw.String(), "\x1b[3A") {
		t.Fatalf("redraw did not rewind over its own %d lines:\n%q", 3, redraw.String())
	}
}

func TestRenderHonorsColorOff(t *testing.T) {
	var out bytes.Buffer
	render(&out, choices([]string{"Alpha", "Beta"}), 0, false, true)
	if strings.Contains(out.String(), colorAccnt) || strings.Contains(out.String(), colorDim) {
		t.Fatalf("colour escapes leaked with colour disabled:\n%q", out.String())
	}

	out.Reset()
	render(&out, choices([]string{"Alpha", "Beta"}), 0, true, true)
	if !strings.Contains(out.String(), colorAccnt) {
		t.Fatalf("colour was requested but not emitted:\n%q", out.String())
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

// clear must rewind exactly as far as it wiped, so the confirmation line lands where
// the menu was instead of below it.
func TestClearRewindsWhatItWiped(t *testing.T) {
	var out bytes.Buffer
	clear(&out, 4)
	got := out.String()

	if !strings.HasPrefix(got, "\x1b[4A") {
		t.Fatalf("clear did not rewind 4 lines first:\n%q", got)
	}
	if strings.Count(got, "\x1b[K") != 4 {
		t.Fatalf("clear wiped %d lines, want 4:\n%q", strings.Count(got, "\x1b[K"), got)
	}
	if !strings.HasSuffix(got, "\x1b[3A\r") {
		t.Fatalf("clear did not park the cursor at the start of the block:\n%q", got)
	}
}

// A detail line is drawn dim under its option and counted when the menu is redrawn or
// cleared, so a picker with descriptions never eats the lines above it.
func TestRenderDrawsDetailsAndCountsThem(t *testing.T) {
	rows := []choice{{Label: "Alpha", Detail: "first thing"}, {Label: "Beta"}, {Label: "Gamma", Detail: "third thing"}}
	if got := renderedLines(rows); got != 6 {
		t.Fatalf("renderedLines = %d, want 3 options + 2 details + hint", got)
	}
	var out bytes.Buffer
	render(&out, rows, 0, true, true)
	got := out.String()
	if !strings.Contains(got, "      "+colorDim+"first thing"+colorReset) || !strings.Contains(got, colorDim+"third thing") {
		t.Fatalf("details are not drawn dim under their options:\n%q", got)
	}
	if strings.Index(got, "Alpha") > strings.Index(got, "first thing") || strings.Index(got, "first thing") > strings.Index(got, "Beta") {
		t.Fatalf("detail must sit between its option and the next:\n%s", got)
	}
	var redraw bytes.Buffer
	render(&redraw, rows, 1, false, false)
	if !strings.HasPrefix(redraw.String(), "\x1b[6A") {
		t.Fatalf("redraw must move up over options, details and hint:\n%q", redraw.String())
	}
}
