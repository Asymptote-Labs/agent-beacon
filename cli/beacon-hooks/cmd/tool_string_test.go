package cmd

import "testing"

// A command's own quotes are part of what it runs, and rules read them; only a value that arrives
// wrapped whole in JSON quotes is unwrapped.
func TestFirstToolStringKeepsQuotesThatBelongToTheValue(t *testing.T) {
	for raw, want := range map[string]string{
		`echo "hi"`:            `echo "hi"`,
		`"a" b "c"`:            `"a" b "c"`,
		`git commit -m "fix"`:  `git commit -m "fix"`,
		`"npm test"`:           `npm test`,
		`  "\"quoted\" arg"  `: `"quoted" arg`,
		`"`:                    `"`,
	} {
		if got := firstToolString(map[string]interface{}{"command": raw}, "command"); got != want {
			t.Errorf("firstToolString(%q) = %q, want %q", raw, got, want)
		}
	}
}
