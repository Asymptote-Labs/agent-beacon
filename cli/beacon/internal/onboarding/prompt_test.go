package onboarding

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func runPrompt(t *testing.T, input string) (Answers, string, error) {
	t.Helper()
	var out bytes.Buffer
	answers, err := Prompt(strings.NewReader(input), &out)
	return answers, out.String(), err
}

func TestPromptHappyPath(t *testing.T) {
	answers, out, err := runPrompt(t, "1\nshukan@asymptotelabs.ai\n")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if answers.Usage != UsageWork {
		t.Fatalf("Usage = %q, want %q", answers.Usage, UsageWork)
	}
	if answers.Email != "shukan@asymptotelabs.ai" {
		t.Fatalf("Email = %q, want %q", answers.Email, "shukan@asymptotelabs.ai")
	}
	if !strings.Contains(out, "Email") {
		t.Fatalf("prompt output did not ask for an email:\n%s", out)
	}
}

// One line of context, not a wall of text. Users skim, and a prompt that lectures
// gets skipped harder than one that asks.
func TestPromptExplainsWhyItIsAsking(t *testing.T) {
	_, out, err := runPrompt(t, "2\ndev@gmail.com\n")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	for _, want := range []string{"free and open source", "How are you using Beacon?"} {
		if !strings.Contains(out, want) {
			t.Fatalf("prompt output is missing %q:\n%s", want, out)
		}
	}
	if lines := strings.Count(strings.TrimSpace(out), "\n"); lines > 14 {
		t.Fatalf("prompt renders %d lines; keep it short:\n%s", lines+1, out)
	}
}

func TestPromptNormalizesAnswers(t *testing.T) {
	answers, out, err := runPrompt(t, "  work  \n  <Shukan@AsymptoteLabs.AI>  \n")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if strings.Contains(out, "using it anyway") {
		t.Fatalf("an address that parses was flagged:\n%s", out)
	}
	if answers.Usage != UsageWork {
		t.Fatalf("Usage = %q, want %q", answers.Usage, UsageWork)
	}
	if answers.Email != "shukan@asymptotelabs.ai" {
		t.Fatalf("Email = %q, want it normalized", answers.Email)
	}
}

// A malformed address is kept, not thrown away. The whole point of the prompt is to
// end up with an answer on disk; refusing one we cannot parse loses the email, the
// usage answer and the install alongside it.
func TestPromptKeepsInvalidEmail(t *testing.T) {
	cases := map[string]struct {
		typed, want string
		reason      error
	}{
		"no at":       {"not-an-email", "not-an-email", ErrEmailNoAt},
		"no dot":      {"still@bad", "still@bad", ErrEmailNoDot},
		"placeholder": {"you@example.com", "you@example.com", ErrEmailPlaceholder},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			answers, out, err := runPrompt(t, "1\n"+tc.typed+"\n")
			if err != nil {
				t.Fatalf("Prompt returned error: %v", err)
			}
			if answers.Email != tc.want {
				t.Fatalf("Email = %q, want the typed answer %q", answers.Email, tc.want)
			}
			if answers.Usage != UsageWork {
				t.Fatalf("Usage = %q, want the usage answer kept too", answers.Usage)
			}
			if !strings.Contains(out, tc.reason.Error()) {
				t.Fatalf("output did not say what looked wrong:\n%s", out)
			}
			if !strings.Contains(out, "using it anyway") {
				t.Fatalf("output did not say the answer was kept:\n%s", out)
			}
			// One note, then on to the next question -- not a second ask.
			if strings.Count(out, "Email") != 1 {
				t.Fatalf("asked for an email %d times, want once:\n%s", strings.Count(out, "Email"), out)
			}
		})
	}
}

// A pasted wall of text is not an answer either, so it is asked again rather than
// written to the profile and sent.
func TestPromptRepromptsForOverlongEmail(t *testing.T) {
	answers, out, err := runPrompt(t, "1\n"+strings.Repeat("x", maxEmail+1)+"\nshukan@asymptotelabs.ai\n")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if answers.Email != "shukan@asymptotelabs.ai" {
		t.Fatalf("Email = %q, want the second answer", answers.Email)
	}
	if !strings.Contains(out, ErrEmailTooLong.Error()) {
		t.Fatalf("output did not say the answer was too long:\n%s", out)
	}
	if strings.Contains(out, "using it anyway") {
		t.Fatalf("an overlong answer was kept:\n%s", out)
	}
}

// An empty line is the one answer with nothing to keep, so it is asked again.
func TestPromptRepromptsForEmptyEmail(t *testing.T) {
	answers, out, err := runPrompt(t, "1\n\n   \nshukan@asymptotelabs.ai\n")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if answers.Email != "shukan@asymptotelabs.ai" {
		t.Fatalf("Email = %q, want the typed answer", answers.Email)
	}
	if !strings.Contains(out, ErrEmailEmpty.Error()) {
		t.Fatalf("output did not ask again for a blank answer:\n%s", out)
	}
	if strings.Contains(out, "using it anyway") {
		t.Fatalf("a blank answer was kept:\n%s", out)
	}
}

func TestPromptRetriesInvalidUsageThenAccepts(t *testing.T) {
	answers, out, err := runPrompt(t, "9\nmaybe\n3\ndev@company.io\n")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if answers.Usage != UsageEvaluating {
		t.Fatalf("Usage = %q, want %q", answers.Usage, UsageEvaluating)
	}
	if !strings.Contains(out, "enter a number from 1 to 3") {
		t.Fatalf("output did not reprompt for the menu:\n%s", out)
	}
}

func TestPromptGivesUpAfterTooManyEmptyEmails(t *testing.T) {
	input := "1\n" + strings.Repeat(" \n", maxAttempts+2)
	_, out, err := runPrompt(t, input)
	if !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("error = %v, want ErrTooManyAttempts", err)
	}
	// Nothing in the message advertises a way to skip the question.
	if strings.Contains(err.Error(), "BEACON_ONBOARDING") {
		t.Fatalf("error %q leaks the unattended-install escape hatch", err)
	}
	if strings.Count(out, "Email") != maxAttempts {
		t.Fatalf("asked for an email %d times, want %d", strings.Count(out, "Email"), maxAttempts)
	}
}

func TestPromptGivesUpAfterTooManyBadUsageAnswers(t *testing.T) {
	_, _, err := runPrompt(t, strings.Repeat("nope\n", maxAttempts+2))
	if !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("error = %v, want ErrTooManyAttempts", err)
	}
}

// Ctrl-D at either question aborts the install and says only that. Cancelling is not
// a lock: the user can re-run and answer.
func TestPromptAbortsOnEOF(t *testing.T) {
	cases := map[string]string{
		"eof at usage": "",
		"eof at email": "1\n",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := runPrompt(t, input)
			if !errors.Is(err, ErrPromptAborted) {
				t.Fatalf("error = %v, want ErrPromptAborted", err)
			}
			if got := err.Error(); got != "onboarding cancelled" {
				t.Fatalf("error = %q, want a bare cancellation message", got)
			}
		})
	}
}

// A final line without a trailing newline is still an answer, not an abort.
func TestPromptAcceptsUnterminatedFinalLine(t *testing.T) {
	answers, _, err := runPrompt(t, "1\nshukan@asymptotelabs.ai")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if answers.Email != "shukan@asymptotelabs.ai" {
		t.Fatalf("Email = %q, want the unterminated line accepted", answers.Email)
	}
}

// A consumer mailbox with a work answer is accepted with a note. Rejecting it would
// lose real contractors and consultants over a formatting opinion.
func TestPromptAcceptsFreeMailboxForWorkUse(t *testing.T) {
	answers, out, err := runPrompt(t, "1\nsomeone@gmail.com\n")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if answers.Email != "someone@gmail.com" || answers.Usage != UsageWork {
		t.Fatalf("answers = %+v, want the free mailbox accepted for work use", answers)
	}
	if !strings.Contains(out, "personal mailbox") {
		t.Fatalf("output did not note the personal mailbox:\n%s", out)
	}
}

func TestPromptDoesNotNoteFreeMailboxForPersonalUse(t *testing.T) {
	_, out, err := runPrompt(t, "2\nsomeone@gmail.com\n")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if strings.Contains(out, "personal mailbox") {
		t.Fatalf("personal use should not trigger the mailbox note:\n%s", out)
	}
}

func TestPromptAsksDestinationOnlyWhenRequested(t *testing.T) {
	answers, out, err := runPrompt(t, "1\nshukan@asymptotelabs.ai\n")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if answers.DestinationAsked || strings.Contains(out, "Where should") {
		t.Fatalf("default prompt must not ask the destination question: %+v\n%s", answers, out)
	}

	var buf bytes.Buffer
	answers, err = PromptWith(strings.NewReader("1\nshukan@asymptotelabs.ai\n3\n"), &buf, PromptOptions{AskDestination: true, OfferAsymptote: true})
	if err != nil {
		t.Fatalf("PromptWith returned error: %v", err)
	}
	if !answers.DestinationAsked || answers.Destination != DestinationAsymptote {
		t.Fatalf("expected the Asymptote answer, got %+v", answers)
	}
	for _, want := range []string{
		"Where should this machine's agent telemetry go?",
		"Keep it on this machine", "Nothing is sent anywhere.",
		"Forward to your own infrastructure", "SIEM, observability platform, or an S3/GCS bucket you own.",
		"Forward to Asymptote Managed", "revoke from the dashboard",
		"Connecting after install.",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("destination output missing %q:\n%s", want, buf.String())
		}
	}
}

// Enter (an empty answer in the typed menu) keeps telemetry local: forwarding is a choice.
func TestDestinationDefaultsToLocal(t *testing.T) {
	for _, input := range []string{"\n", "1\n", "local\n", "KEEP\n"} {
		var buf bytes.Buffer
		answers, err := PromptWith(strings.NewReader("1\nshukan@asymptotelabs.ai\n"+input), &buf, PromptOptions{AskDestination: true, OfferAsymptote: true})
		if err != nil {
			t.Fatalf("input %q: %v", input, err)
		}
		if answers.Destination != DestinationLocal {
			t.Fatalf("input %q should keep telemetry local, got %+v", input, answers)
		}
		if !strings.Contains(buf.String(), "beacon endpoint connect") || !strings.Contains(buf.String(), ForwardingDocsURL) {
			t.Fatalf("the local answer should say how to forward later:\n%s", buf.String())
		}
	}
	var buf bytes.Buffer
	if _, err := PromptWith(strings.NewReader("1\nshukan@asymptotelabs.ai\nmaybe\n9\nhuh\n?\n!\n"), &buf, PromptOptions{AskDestination: true, OfferAsymptote: true}); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("expected ErrTooManyAttempts after repeated bad answers, got %v", err)
	}
}

func TestDestinationOwnInfrastructurePointsAtTheDocs(t *testing.T) {
	for _, input := range []string{"2\n", "own\n", "siem\n", "own_infra\n"} {
		var buf bytes.Buffer
		answers, err := PromptWith(strings.NewReader("1\nshukan@asymptotelabs.ai\n"+input), &buf, PromptOptions{AskDestination: true, OfferAsymptote: true})
		if err != nil || answers.Destination != DestinationOwnInfra {
			t.Fatalf("input %q: destination=%q err=%v", input, answers.Destination, err)
		}
		for _, want := range []string{ForwardingDocsURL, "beacon endpoint datadog", "Vector pack"} {
			if !strings.Contains(buf.String(), want) {
				t.Fatalf("own-infrastructure answer missing %q:\n%s", want, buf.String())
			}
		}
	}
}

// BEACON_MANAGED_INGEST=0 hides the Asymptote row; the question is still asked.
func TestDestinationHidesAsymptoteWhenNotOffered(t *testing.T) {
	var buf bytes.Buffer
	answers, err := PromptWith(strings.NewReader("1\nshukan@asymptotelabs.ai\n3\nasymptote\n2\n"), &buf, PromptOptions{AskDestination: true, OfferAsymptote: false})
	if err != nil {
		t.Fatal(err)
	}
	if answers.Destination != DestinationOwnInfra {
		t.Fatalf("with two rows, 3 and asymptote must be rejected and 2 accepted, got %+v", answers)
	}
	if strings.Contains(buf.String(), "Asymptote Managed") {
		t.Fatalf("Asymptote row must be hidden:\n%s", buf.String())
	}
	if strings.Count(buf.String(), "enter a number from 1 to 2") != 2 {
		t.Fatalf("expected two rejections:\n%s", buf.String())
	}
}

func TestAskDestinationAlone(t *testing.T) {
	var buf bytes.Buffer
	destination, err := AskDestination(strings.NewReader("3\n"), &buf, true)
	if err != nil || destination != DestinationAsymptote {
		t.Fatalf("AskDestination = %q, %v", destination, err)
	}
	if _, err := AskDestination(strings.NewReader(""), &buf, true); !errors.Is(err, ErrPromptAborted) {
		t.Fatalf("EOF should abort, got %v", err)
	}
}

// Every destination row must draw without wrapping on a standard 80-column terminal, with
// the last column left free for terminals that wrap eagerly, or the picker's line count
// is off and it eats the lines above it on redraw.
func TestDestinationRowsFitEightyColumns(t *testing.T) {
	for _, item := range destinationChoices(true) {
		if n := 4 + len([]rune(item.label)); n >= 80 {
			t.Fatalf("label %q draws %d columns", item.label, n)
		}
		if n := 6 + len([]rune(item.detail)); n >= 80 {
			t.Fatalf("detail %q draws %d columns", item.detail, n)
		}
	}
	if !strings.Contains(destinationChoices(true)[1].detail, "SIEM, observability platform, or an S3/GCS bucket you own.") {
		t.Fatal("the own-infrastructure wording is part of the product copy")
	}
}
