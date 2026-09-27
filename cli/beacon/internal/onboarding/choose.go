package onboarding

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Option is one answer to a Choose question: a label and an optional line of detail.
type Option struct {
	Label  string
	Detail string
}

// Choose asks one question with the same arrow-key picker as the install wizard, or a numbered
// menu where the terminal cannot do raw input, and returns the index of the answer. Enter, or an
// empty typed answer, picks the first option.
func Choose(in io.Reader, out io.Writer, question string, options []Option) (int, error) {
	if len(options) == 0 {
		return 0, errors.New("no options to choose from")
	}
	color := supportsColor(out)
	fmt.Fprintf(out, "  %s%s%s\n\n", title(color), question, reset(color))
	if tty, ok := terminalFile(in); ok {
		rows := make([]choice, len(options))
		for i, option := range options {
			rows[i] = choice{Label: option.Label, Detail: option.Detail}
		}
		index, err := selectOption(tty, out, rows, color)
		if err == nil {
			printChoice(out, options[index].Label, color)
			return index, nil
		}
		if !errors.Is(err, errRawUnavailable) {
			return 0, ErrPromptAborted
		}
	}
	for i, option := range options {
		fmt.Fprintf(out, "    %d) %s\n", i+1, option.Label)
		if option.Detail != "" {
			fmt.Fprintf(out, "       %s%s%s\n", dim(color), option.Detail, reset(color))
		}
	}
	fmt.Fprintln(out)
	reader := bufio.NewReader(in)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		line, err := readLine(reader, out, fmt.Sprintf("  Choice [1-%d] ", len(options)))
		if err != nil {
			return 0, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			printChoice(out, options[0].Label, color)
			return 0, nil
		}
		if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= len(options) {
			printChoice(out, options[n-1].Label, color)
			return n - 1, nil
		}
		fmt.Fprintf(out, "  %s✗ enter a number from 1 to %d%s\n", warn(color), len(options), reset(color))
	}
	return 0, ErrTooManyAttempts
}
