package asymptoteobserve

import (
	"strings"
	"testing"
)

// A labeled value after Go's and make's `:=`, or after the `=>` of a Ruby, PHP or Perl hash, is
// redacted like one after `=` or `:`, and the operator is kept as written. Read as `:` and a
// value, `password := "x"` gave up the value `=` and kept the quoted secret after it (#1374).
func TestRedactStringRedactsAValueAfterColonEqualsAndArrow(t *testing.T) {
	tests := []struct{ input, want string }{
		{`password := "hunter2hunter2"`, `password := "[REDACTED]"`},
		{`apiToken := 'hunter2hunter2'`, `apiToken := '[REDACTED]'`},
		{"	secret := `hunter2hunter2`", "	secret := `[REDACTED]`"},
		{`TOKEN := hunter2hunter2`, `TOKEN := [REDACTED]`},
		{`API_KEY:=hunter2hunter2`, `API_KEY:=[REDACTED]`},
		{`'password' => 'hunter2hunter2'`, `'password' => '[REDACTED]'`},
		{`"api_key" => "hunter2hunter2"`, `"api_key" => "[REDACTED]"`},
		{`:password => "hunter2hunter2"`, `:password => "[REDACTED]"`},
		// Go source quoted inside a JSON string, as a tool's input carries it.
		{`{"content":"token := \"hunter2hunter2\"\n"}`, `{"content":"token := \"[REDACTED]\"\n"}`},
	}
	for _, tc := range tests {
		if got := RedactString(tc.input); got != tc.want {
			t.Errorf("RedactString(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// The value of a `:=` assignment is removed wherever else it is copied, as the value of an `=`
// assignment is.
func TestRedactStringRedactsACopyOfAColonEqualsValue(t *testing.T) {
	input := `password := "hunter2hunter2"` + "\n" + `login(user, "hunter2hunter2")`
	if got := RedactString(input); strings.Contains(got, "hunter2") {
		t.Errorf("RedactString(%q) = %q, kept a copy of the value", input, got)
	}
}

// `=>` before a bare value is most often a JavaScript arrow function, and is left alone, as is a
// comparison; before this, the arrow's `>` was taken for the value of `token =`.
func TestRedactStringLeavesArrowFunctionsAlone(t *testing.T) {
	for _, code := range []string{
		`const ids = tokens.map(token => token.id)`,
		`secrets.forEach(secret => console.log(secret.name))`,
		`if password == "" {`,
	} {
		if got := RedactString(code); got != code {
			t.Errorf("RedactString(%q) = %q, want it unchanged", code, got)
		}
	}
}

// A value that only starts with an operator's bytes is a value, and is redacted whole: the shell
// sets DB_PASSWORD to `=Xk9q2r` and `>s3cr3t` here. Only a value made of operator bytes alone is the
// rest of an operator (#1375 review).
func TestRedactStringRedactsAValueThatStartsWithAnOperatorByte(t *testing.T) {
	tests := []struct{ input, want string }{
		{`password==Xk9!q2`, `password=[REDACTED]`},
		{`DB_PASSWORD==Xk9q2r`, `DB_PASSWORD=[REDACTED]`},
		{`DB_PASSWORD=>s3cr3t`, `DB_PASSWORD=[REDACTED]`},
		{`password=>abc123`, `password=[REDACTED]`},
		{`TOKEN=$(whoami)=hunter2`, `TOKEN=[REDACTED]`},
	}
	for _, tc := range tests {
		if got := RedactString(tc.input); got != tc.want {
			t.Errorf("RedactString(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// make's other assignment operators are separators too, and are kept as written; `!=` is not, since
// what follows it is a shell command to run (#1375 review).
func TestRedactStringRedactsAValueAfterMakesAssignmentOperators(t *testing.T) {
	tests := []struct{ input, want string }{
		{`API_TOKEN ?= abc123secret`, `API_TOKEN ?= [REDACTED]`},
		{`API_TOKEN += abc123secret`, `API_TOKEN += [REDACTED]`},
		{`TOKEN ::= "hunter2"`, `TOKEN ::= "[REDACTED]"`},
		{`TOKEN :::= "hunter2"`, `TOKEN :::= "[REDACTED]"`},
		{`TOKEN != gh auth token`, `TOKEN != gh auth token`},
	}
	for _, tc := range tests {
		if got := RedactString(tc.input); got != tc.want {
			t.Errorf("RedactString(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// `${NAME:=$(cmd)}` keeps its one substitution, as `NAME=$(cmd)` does: the brace ends the word, and
// the log still names the command that handed the credential over. Text beside the substitution is
// still part of the secret (#1375 review).
func TestRedactStringKeepsTheSubstitutionOfADefaultAssignment(t *testing.T) {
	tests := []struct{ input, want string }{
		{`: "${GH_TOKEN:=$(gh auth token)}"; gh api user`, `: "${GH_TOKEN:=$(gh auth token)}"; gh api user`},
		{`echo ${TOKEN=$(cat ~/.aws/credentials)}`, `echo ${TOKEN=$(cat ~/.aws/credentials)}`},
		{`: "${GH_TOKEN:=$(gh auth token)hunter2}"`, `: "${GH_TOKEN:=[REDACTED]"`},
	}
	for _, tc := range tests {
		if got := RedactString(tc.input); got != tc.want {
			t.Errorf("RedactString(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// `=>` before a backtick is an arrow function returning a template literal, left alone; and a value
// after `=>` is not captured, so its copies elsewhere are not removed (#1375 review).
func TestRedactStringLeavesAnArrowFunctionsTemplateLiteralAlone(t *testing.T) {
	code := "tokens.map(token =>` ${token.kind}:${token.text}`)"
	if got := RedactString(code); got != code {
		t.Errorf("RedactString(%q) = %q, want it unchanged", code, got)
	}
	input := "const labels = { 'token' => 'required-field' }\nvalidate('required-field')"
	if got := RedactString(input); !strings.HasSuffix(got, "validate('required-field')") {
		t.Errorf("RedactString(%q) = %q, removed a copy of a value after =>", input, got)
	}
}
