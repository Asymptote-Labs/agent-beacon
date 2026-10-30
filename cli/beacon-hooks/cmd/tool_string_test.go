package cmd

import "testing"

// A command's own quotes are part of what it runs, and rules read them; only a value wrapped whole
// in one pair of quotes loses that pair, and backslashes are never unescaped.
func TestFirstToolStringKeepsQuotesThatBelongToTheValue(t *testing.T) {
	for raw, want := range map[string]string{
		`echo "hi"`:              `echo "hi"`,
		`"a" b "c"`:              `"a" b "c"`,
		`git commit -m "fix"`:    `git commit -m "fix"`,
		`"npm test"`:             `npm test`,
		`"C:\tools\bin\run.exe"`: `C:\tools\bin\run.exe`,
		`"C:\Users\me\a.go"`:     `C:\Users\me\a.go`,
		`"$(\rm -rf ~ )"`:        `$(\rm -rf ~ )`,
		`"`:                      `"`,
	} {
		if got := firstToolString(map[string]interface{}{"command": raw}, "command"); got != want {
			t.Errorf("firstToolString(%q) = %q, want %q", raw, got, want)
		}
	}
}
