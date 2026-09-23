package threatrules

import (
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestEnvBuilds(t *testing.T) {
	if _, err := Env(); err != nil {
		t.Fatalf("Env() error: %v", err)
	}
}

func TestCompileMatch(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr string // substring; "" means expect success
	}{
		{"valid bool", `e.event.action == "file.read"`, ""},
		{"valid matches", `e.command.command.matches("curl")`, ""},
		{"deep field", `e.gen_ai.usage.input_tokens > 1000`, ""},
		{"empty", "  ", "empty match expression"},
		{"unknown field", `e.fil.path == "x"`, "compile match"},
		{"non-bool result", `e.command.command`, "must evaluate to bool"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CompileMatch(tt.expr)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// CEL does not validate a non-constant regex receiver at compile time, so an invalid
// pattern compiles but errors at evaluation. This still catches bad regexes in shipped
// rules because every rule carries fixtures that run through evaluation.
func TestInvalidRegexErrorsAtEval(t *testing.T) {
	prog, err := CompileMatch(`e.command.command.matches("(")`)
	if err != nil {
		t.Fatalf("compile (regex not validated at compile time): %v", err)
	}
	if _, err := EvalMatch(prog, asymptoteobserve.Event{}); err == nil {
		t.Fatalf("expected eval error for invalid regex")
	}
}

func TestEvalMatchNullSafety(t *testing.T) {
	// A match referencing a sub-object that is absent must evaluate to false, not error.
	prog, err := CompileMatch(`e.command.command.matches("curl")`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// No Command sub-object at all.
	got, err := EvalMatch(prog, asymptoteobserve.Event{
		Event: asymptoteobserve.EventInfo{Action: "file.read"},
	})
	if err != nil {
		t.Fatalf("eval (missing command) error: %v", err)
	}
	if got {
		t.Fatalf("expected no match when command is absent")
	}

	// Deep nesting absent: gen_ai -> usage -> input_tokens.
	deep, err := CompileMatch(`e.gen_ai.usage.input_tokens > 1000`)
	if err != nil {
		t.Fatalf("compile deep: %v", err)
	}
	got, err = EvalMatch(deep, asymptoteobserve.Event{})
	if err != nil {
		t.Fatalf("eval (missing gen_ai) error: %v", err)
	}
	if got {
		t.Fatalf("expected no match when gen_ai is absent")
	}
}

func TestEvalMatchPresent(t *testing.T) {
	prog, err := CompileMatch(`e.event.action == "command.executed" && e.command.command.matches("curl")`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	got, err := EvalMatch(prog, asymptoteobserve.Event{
		Event:   asymptoteobserve.EventInfo{Action: "command.executed"},
		Command: &asymptoteobserve.CommandInfo{Command: "curl https://x"},
	})
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if !got {
		t.Fatalf("expected match")
	}
}

// Browser chat events name their browser in e.user_agent, so a rule can scope to (or away
// from) the managed browser. Events from every other source have no user_agent at all, and a
// rule on it must quietly not match them rather than error.
func TestEvalMatchUserAgent(t *testing.T) {
	prog, err := CompileMatch(`e.harness.name == "chatgpt_web" && e.user_agent.name != "Microsoft Edge"`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	cases := []struct {
		name  string
		event asymptoteobserve.Event
		want  bool
	}{
		{"personal Brave", asymptoteobserve.Event{Harness: asymptoteobserve.HarnessInfo{Name: "chatgpt_web"}, UserAgent: &asymptoteobserve.UserAgentInfo{Name: "Brave", Version: "124"}}, true},
		{"managed Edge", asymptoteobserve.Event{Harness: asymptoteobserve.HarnessInfo{Name: "chatgpt_web"}, UserAgent: &asymptoteobserve.UserAgentInfo{Name: "Microsoft Edge", Version: "153"}}, false},
		{"CLI agent without user_agent", asymptoteobserve.Event{Harness: asymptoteobserve.HarnessInfo{Name: "claude_code"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EvalMatch(prog, tc.event)
			if err != nil {
				t.Fatalf("eval: %v", err)
			}
			if got != tc.want {
				t.Fatalf("match = %v, want %v", got, tc.want)
			}
		})
	}
}
