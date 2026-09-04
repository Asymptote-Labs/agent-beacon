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
	// DestinationAsked is true when the telemetry destination question was asked;
	// Destination is the answer (DestinationLocal, DestinationOwnInfra or
	// DestinationAsymptote).
	DestinationAsked bool
	Destination      string
}

// PromptOptions tunes the one-time prompt.
type PromptOptions struct {
	// AskDestination adds the "where should this machine's telemetry go?" question. The
	// caller decides whether to ask (not already connected, not forced by --connect).
	AskDestination bool
	// OfferAsymptote includes the Asymptote Managed row; BEACON_MANAGED_INGEST=0 hides it.
	OfferAsymptote bool
}

// ForwardingDocsURL is where the own-infrastructure answer points: the pack-by-pack
// guide for shipping the local JSONL to a SIEM, observability platform or bucket.
const ForwardingDocsURL = "https://docs.asymptotelabs.ai/log-forwarding"

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
	fmt.Fprintf(out, "  %sBeacon is free and open source.%s Sharing your email helps us\n", title(color), reset(color))
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
	if opts.AskDestination {
		fmt.Fprintln(out)
		destination, err := askDestination(reader, in, out, color, opts.OfferAsymptote)
		if err != nil {
			return Answers{}, err
		}
		answers.DestinationAsked = true
		answers.Destination = destination
	}

	fmt.Fprintln(out)
	return answers, nil
}

// AskDestination asks only the destination question, for a machine that went through
// onboarding before the question existed.
func AskDestination(in io.Reader, out io.Writer, offerAsymptote bool) (string, error) {
	color := supportsColor(out)
	fmt.Fprintln(out)
	answer, err := askDestination(bufio.NewReader(in), in, out, color, offerAsymptote)
	fmt.Fprintln(out)
	return answer, err
}

// destinationChoice is one answer to the destination question.
type destinationChoice struct {
	value, label, detail string
}

// destinationChoices lists the answers in display order. Local is first so that Enter
// never forwards anything: sending telemetry off the machine is a choice, never a default.
func destinationChoices(offerAsymptote bool) []destinationChoice {
	items := []destinationChoice{
		{DestinationLocal, "Keep it on this machine", "Nothing is sent anywhere. Local JSONL and local dashboard; change it any time."},
		{DestinationOwnInfra, "Forward to your own infrastructure", "SIEM, observability platform, or an S3/GCS bucket you own."},
	}
	if offerAsymptote {
		items = append(items, destinationChoice{DestinationAsymptote, "Forward to Asymptote Managed", "Opens your browser to approve this device; revoke it from the dashboard any time."})
	}
	return items
}

// askDestination asks where this machine's telemetry should go. On a terminal it is the
// same arrow-key picker as the usage question, with a line of detail under each row;
// elsewhere it degrades to the numbered menu, where an empty answer means the first row.
func askDestination(reader *bufio.Reader, in io.Reader, out io.Writer, color bool, offerAsymptote bool) (string, error) {
	items := destinationChoices(offerAsymptote)
	fmt.Fprintf(out, "  %sWhere should this machine's agent telemetry go?%s\n\n", title(color), reset(color))

	if tty, ok := terminalFile(in); ok {
		rows := make([]choice, len(items))
		for i, item := range items {
			rows[i] = choice{Label: item.label, Detail: item.detail}
		}
		index, err := selectOption(tty, out, rows, color)
		if err == nil {
			printDestination(out, items[index], color)
			return items[index].value, nil
		}
		if !errors.Is(err, errRawUnavailable) {
			return "", ErrPromptAborted
		}
	}

	for i, item := range items {
		fmt.Fprintf(out, "    %d) %s\n", i+1, item.label)
		fmt.Fprintf(out, "       %s%s%s\n", dim(color), item.detail, reset(color))
	}
	fmt.Fprintln(out)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		line, err := readLine(reader, out, fmt.Sprintf("  Choice [1-%d] ", len(items)))
		if err != nil {
			return "", err
		}
		if index, ok := parseDestination(line, items); ok {
			printDestination(out, items[index], color)
			return items[index].value, nil
		}
		fmt.Fprintf(out, "  %s✗ enter a number from 1 to %d%s\n", warn(color), len(items), reset(color))
	}
	return "", ErrTooManyAttempts
}

// parseDestination accepts the row number, the stored value, or a plain word for it.
// An empty answer is the first row, matching what Enter does in the picker.
func parseDestination(line string, items []destinationChoice) (int, bool) {
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer == "" {
		return 0, true
	}
	for i, item := range items {
		if answer == fmt.Sprint(i+1) || answer == item.value {
			return i, true
		}
	}
	aliases := map[string]string{
		"local": DestinationLocal, "keep": DestinationLocal, "none": DestinationLocal,
		"own": DestinationOwnInfra, "self": DestinationOwnInfra, "siem": DestinationOwnInfra,
		"asymptote": DestinationAsymptote, "managed": DestinationAsymptote,
	}
	if value, ok := aliases[answer]; ok {
		for i, item := range items {
			if item.value == value {
				return i, true
			}
		}
	}
	return 0, false
}

// printDestination confirms the choice and says what happens next.
func printDestination(out io.Writer, item destinationChoice, color bool) {
	fmt.Fprintf(out, "  %s✓%s %s\n", ok(color), reset(color), item.label)
	switch item.value {
	case DestinationLocal:
		fmt.Fprintf(out, "  %sTo forward later: a forwarding pack (%s) or `beacon endpoint connect`.%s\n", dim(color), ForwardingDocsURL, reset(color))
	case DestinationOwnInfra:
		fmt.Fprintln(out, "  Beacon keeps writing JSONL locally; a Vector pack ships it to your destination.")
		fmt.Fprintf(out, "  Set one up: %s%s%s\n", accent(color), ForwardingDocsURL, reset(color))
		fmt.Fprintf(out, "  %sPacks: beacon endpoint datadog | elastic | falcon | s3 | gcs | wazuh | sentinel … (--help lists them all)%s\n", dim(color), reset(color))
	case DestinationAsymptote:
		fmt.Fprintf(out, "  %sConnecting after install.%s\n", dim(color), reset(color))
	}
	fmt.Fprintln(out)
}

func askUsage(reader *bufio.Reader, in io.Reader, out io.Writer, color bool) (string, error) {
	fmt.Fprintf(out, "  %sHow are you using Beacon?%s\n\n", title(color), reset(color))

	if tty, ok := terminalFile(in); ok {
		index, err := selectOption(tty, out, choices(UsageLabels), color)
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
		line, err := readLine(reader, out, fmt.Sprintf("  %sEmail%s › ", title(color), reset(color)))
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
func title(c bool) string {
	if c {
		return colorTitle
	}
	return ""
}

func accent(c bool) string {
	if c {
		return colorAccnt
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
