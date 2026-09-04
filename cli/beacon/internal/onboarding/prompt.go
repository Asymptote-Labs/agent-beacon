package onboarding

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// maxAttempts bounds the email question so someone who cannot produce an address ends
// with a clear message instead of looping.
const maxAttempts = 5

// ErrPromptAborted means the user ended input (Ctrl-C or Ctrl-D) without answering.
// Cancelling aborts this install and nothing more: the user can re-run and answer.
// The message deliberately does not mention BEACON_ONBOARDING -- that variable is for
// unattended CI and MDM installs, documented in the CLI reference, not an escape hatch
// to hand someone the moment they try to decline.
var ErrPromptAborted = errors.New("onboarding cancelled")

// ErrTooManyAttempts means the question was answered incorrectly too many times.
var ErrTooManyAttempts = errors.New("too many invalid answers")

// Answers holds what the user chose.
type Answers struct {
	Email string
	Usage string
	// ManagedIngestOffered is true when the forwarding question was asked;
	// ManagedIngest is the answer.
	ManagedIngestOffered bool
	ManagedIngest        bool
}

// PromptOptions tunes the one-time prompt.
type PromptOptions struct {
	// OfferManagedIngest adds the "forward to Asymptote Managed now?" question. The
	// caller decides whether it is offerable (Vector present, not already connected).
	OfferManagedIngest bool
}

// Prompt runs the one-time onboarding questions.
//
// On a real terminal the usage question is an arrow-key picker. Anywhere else -- a
// buffer in tests, a terminal that will not enter raw mode -- it degrades to a typed
// numbered menu, so no environment loses the ability to answer.
func Prompt(in io.Reader, out io.Writer) (Answers, error) {
	return PromptWith(in, out, PromptOptions{})
}

// PromptWith is Prompt with options.
func PromptWith(in io.Reader, out io.Writer, opts PromptOptions) (Answers, error) {
	color := supportsColor(out)
	reader := bufio.NewReader(in)

	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %sBeacon is free and open source.%s Sharing your email helps us\n", bold(color), reset(color))
	fmt.Fprintln(out, "  prioritize which agent runtimes to support next.")
	fmt.Fprintln(out)

	usage, err := askUsage(reader, in, out, color)
	if err != nil {
		return Answers{}, err
	}

	email, err := askEmail(reader, out, color)
	if err != nil {
		return Answers{}, err
	}

	// A work answer on a consumer mailbox is worth a note but not a rejection:
	// contractors and people at companies without their own domain are all real.
	if usage == UsageWork && ClassifyDomain(EmailDomain(email)) == DomainFree {
		fmt.Fprintf(out, "  %s%s is a personal mailbox — using it anyway.%s\n", dim(color), EmailDomain(email), reset(color))
	}

	answers := Answers{Email: email, Usage: usage}
	if opts.OfferManagedIngest {
		fmt.Fprintln(out)
		connect, err := askManagedIngest(reader, out, color)
		if err != nil {
			return Answers{}, err
		}
		answers.ManagedIngestOffered = true
		answers.ManagedIngest = connect
	}

	fmt.Fprintln(out)
	return answers, nil
}

// AskManagedIngest asks only the forwarding question, for a machine that went through
// onboarding before the question existed.
func AskManagedIngest(in io.Reader, out io.Writer) (bool, error) {
	color := supportsColor(out)
	fmt.Fprintln(out)
	answer, err := askManagedIngest(bufio.NewReader(in), out, color)
	fmt.Fprintln(out)
	return answer, err
}

// askManagedIngest offers to connect this machine to Asymptote Managed. Default is no:
// forwarding telemetry off the machine is a choice, never a side effect of Enter.
func askManagedIngest(reader *bufio.Reader, out io.Writer, color bool) (bool, error) {
	fmt.Fprintf(out, "  %sForward this machine's agent telemetry to Asymptote Managed?%s\n", bold(color), reset(color))
	fmt.Fprintln(out, "  This opens your browser to sign in and approve this device; you can revoke it")
	fmt.Fprintln(out, "  from the dashboard at any time. Nothing recorded before approval is sent.")
	fmt.Fprintln(out)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		line, err := readLine(reader, out, "  Connect now? [y/N] ")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "", "n", "no":
			printChoice(out, "Not now. Run `beacon endpoint connect` whenever you are ready.", color)
			return false, nil
		case "y", "yes":
			printChoice(out, "Connecting after install.", color)
			return true, nil
		}
		fmt.Fprintf(out, "  %s✗ answer y or n%s\n", warn(color), reset(color))
	}
	return false, ErrTooManyAttempts
}

func askUsage(reader *bufio.Reader, in io.Reader, out io.Writer, color bool) (string, error) {
	fmt.Fprintf(out, "  %sHow are you using Beacon?%s\n\n", bold(color), reset(color))

	if tty, ok := terminalFile(in); ok {
		index, err := selectOption(tty, out, UsageLabels, color)
		if err == nil {
			printChoice(out, UsageLabels[index], color)
			return ValidUsages[index], nil
		}
		if !errors.Is(err, errRawUnavailable) {
			return "", ErrPromptAborted
		}
		// Raw mode was refused; fall through to the typed menu below.
	}

	for i, label := range UsageLabels {
		fmt.Fprintf(out, "    %d) %s\n", i+1, label)
	}
	fmt.Fprintln(out)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		line, err := readLine(reader, out, fmt.Sprintf("  Choice [1-%d] ", len(UsageLabels)))
		if err != nil {
			return "", err
		}
		if usage, ok := NormalizeUsage(line); ok {
			// Echo the same confirmation the picker shows, so both paths read alike.
			for i, candidate := range ValidUsages {
				if candidate == usage {
					printChoice(out, UsageLabels[i], color)
					break
				}
			}
			return usage, nil
		}
		fmt.Fprintf(out, "  %s✗ enter a number from 1 to %d%s\n", warn(color), len(UsageLabels), reset(color))
	}
	return "", ErrTooManyAttempts
}

func askEmail(reader *bufio.Reader, out io.Writer, color bool) (string, error) {
	for attempt := 0; attempt < maxAttempts; attempt++ {
		line, err := readLine(reader, out, fmt.Sprintf("  %sEmail%s › ", bold(color), reset(color)))
		if err != nil {
			return "", err
		}
		email, invalid := NormalizeEmail(line)
		if invalid == nil {
			return email, nil
		}
		fmt.Fprintf(out, "  %s✗ %s%s\n", warn(color), invalid, reset(color))
	}
	return "", ErrTooManyAttempts
}

func printChoice(out io.Writer, label string, color bool) {
	fmt.Fprintf(out, "  %s✓%s %s\n\n", ok(color), reset(color), label)
}

// readLine writes a prompt and reads one line. EOF with no content means Ctrl-D or
// exhausted input, which is an abort rather than a bad answer.
func readLine(reader *bufio.Reader, out io.Writer, prompt string) (string, error) {
	fmt.Fprint(out, prompt)
	line, err := reader.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line), nil
		}
		fmt.Fprintln(out)
		return "", ErrPromptAborted
	}
	return strings.TrimSpace(line), nil
}

// Colour helpers return empty strings when colour is off, so every format string can
// use them unconditionally.
func bold(c bool) string {
	if c {
		return "\x1b[1m"
	}
	return ""
}

func dim(c bool) string {
	if c {
		return colorDim
	}
	return ""
}

func warn(c bool) string {
	if c {
		return "\x1b[33m"
	}
	return ""
}

func ok(c bool) string {
	if c {
		return colorGreen
	}
	return ""
}

func reset(c bool) string {
	if c {
		return colorReset
	}
	return ""
}
