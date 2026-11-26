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
