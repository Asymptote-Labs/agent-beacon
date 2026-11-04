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
