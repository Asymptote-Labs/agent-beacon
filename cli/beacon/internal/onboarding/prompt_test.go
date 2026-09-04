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
	answers, _, err := runPrompt(t, "  work  \n  <Shukan@AsymptoteLabs.AI>  \n")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if answers.Usage != UsageWork {
		t.Fatalf("Usage = %q, want %q", answers.Usage, UsageWork)
	}
	if answers.Email != "shukan@asymptotelabs.ai" {
		t.Fatalf("Email = %q, want it normalized", answers.Email)
	}
}

func TestPromptRetriesInvalidEmailThenAccepts(t *testing.T) {
	answers, out, err := runPrompt(t, "1\nnot-an-email\nstill@bad\nshukan@asymptotelabs.ai\n")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if answers.Email != "shukan@asymptotelabs.ai" {
		t.Fatalf("Email = %q, want the third answer accepted", answers.Email)
	}
	if !strings.Contains(out, ErrEmailNoAt.Error()) {
		t.Fatalf("output did not explain the first failure:\n%s", out)
	}
	if !strings.Contains(out, ErrEmailNoDot.Error()) {
		t.Fatalf("output did not explain the second failure:\n%s", out)
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

func TestPromptGivesUpAfterTooManyBadEmails(t *testing.T) {
	input := "1\n" + strings.Repeat("nope\n", maxAttempts+2)
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
		"Forward to Asymptote Managed", "revoke it from the dashboard",
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
