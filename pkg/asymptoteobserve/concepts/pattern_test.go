package concepts

import (
	"regexp"
	"strings"
	"testing"
)

// TestPatterns pins what a pattern names. Each row is asked of the matcher and of the regular
// expression generated from the pattern, which must both say the same.
func TestPatterns(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		path    string
		want    bool
	}{
		// The last components of the path, in any directory, and nothing more or less.
		{".aws/credentials", "/home/u/.aws/credentials", true},
		{".aws/credentials", ".aws/credentials", true},
		{".aws/credentials", "~/.aws/credentials", true},
		{".aws/credentials", "$HOME/.aws/credentials", true},
		{".aws/credentials", "/home/u/.aws/credentials.bak", false},
		{".aws/credentials", "/home/u/.aws/credentials/x", false},
		{".aws/credentials", "/home/u/.aws/credentials/", false},
		{".aws/credentials", "/home/u/my.aws/credentials", false},
		{".aws/credentials", "/home/u/.aws/sub/credentials", false},
		{".aws/credentials", "/home/u/.aws//credentials", false},
		{".aws/credentials", "/home/u/xaws/credentials", false}, // the dot is a dot
		{".env", ".env", true},
		{".env", "apps/web/.env", true},
		{".env", ".envrc", false},
		{".env", "x.env", false},
		{".env", "", false},
		// Either separator, and letters in either case.
		{".aws/credentials", `C:\Users\u\.aws\credentials`, true},
		{".aws/credentials", "/home/u/.AWS/Credentials", true},
		{"Login Data", "/p/login data", true},
		{"straße", "/p/STRASSE", false}, // simple folding: ß is not ss
		{"k", "/p/\u212a", true},        // and the Kelvin sign is k, as RE2 has it
		// * within a component.
		{"*.kdbx", "/d/Passwords.kdbx", true},
		{"*.kdbx", "/d/.kdbx", true},
		{"*.kdbx", "/d/x.kdbx/y", false},
		{"*.kdbx", "/d.kdbx/y", false},
		{".env.*", "/r/.env.local", true},
		{".env.*", "/r/.env.", true},
		{".env.*", "/r/.env", false},
		{".env.*", "/r/.env.d/x", false},
		{".kube/*kubeconfig*", "/h/.kube/prod-kubeconfig.yml", true},
		{".kube/*kubeconfig*", "/h/.kube/kubeconfig", true},
		{".kube/*kubeconfig*", "/h/.kube/sub/kubeconfig", false},
		{"a*b*c", "/x/abc", true},
		{"a*b*c", "/x/a-b-b-c", true},
		{"a*b*c", "/x/acb", false},
		// {a,b,} is one of the alternatives, the empty one too.
		{"id_{rsa,ed25519}", "/h/.ssh/id_rsa", true},
		{"id_{rsa,ed25519}", "/h/.ssh/id_ed25519", true},
		{"id_{rsa,ed25519}", "/h/.ssh/id_ecdsa", false},
		{"id_{rsa,ed25519}", "/h/.ssh/id_rsa.pub", false},
		{"sa{-,_,}token{,.csv}", "/s/satoken", true},
		{"sa{-,_,}token{,.csv}", "/s/sa_token.csv", true},
		{"sa{-,_,}token{,.csv}", "/s/sa.token", false},
		{"sa{-,_,}token{,.csv}", "/s/sa-token.txt", false},
		{"{*cookies,*login data}", "/p/Extension Cookies", true},
		{"{*cookies,*login data}", "/p/Cookies-journal", false},
		{"a.{b,c}.d", "/x/a.b.d", true},
		{"a.{b,c}.d", "/x/a.bc.d", false},
		// ** is any number of whole directories.
		{"chrome/**/cookies", "/c/chrome/cookies", true},
		{"chrome/**/cookies", "/c/chrome/Default/Network/Cookies", true},
		{"chrome/**/cookies", "/c/chromex/Default/Cookies", false},
		{"chrome/**/cookies", "/c/chrome/Default/Cookies/x", false},
		{"chrome/**/cookies", "/c/chrome//cookies", false},
		{".kube/**/*token*", "/h/.kube/cache/oidc/id-token.json", true},
		{".kube/**/*token*", "/h/.kube/token", true},
		{".kube/**/*token*", "/h/kube/cache/token", false},
		// A trailing /** is everything under a directory, and not the directory.
		{".password-store/**", "/h/.password-store/work/github.gpg", true},
		{".password-store/**", "/h/.password-store/.gpg-id", true},
		{".password-store/**", "/h/.password-store/", true},
		{".password-store/**", "/h/.password-store", false},
		{".password-store/**", "/h/x.password-store/a", false},
		{"a/**/b/**", "/x/a/b/c", true},
		{"a/**/b/**", "/x/a/m/n/b/c/d", true},
		{"a/**/b/**", "/x/a/m/n/b", false},
		// Characters that mean something in a regular expression are themselves.
		{"a+b(c)$.d", "/x/a+b(c)$.d", true},
		{"a+b(c)$.d", "/x/aab(c)$xd", false},
		{"kubernetes.io/token", "/var/run/secrets/kubernetes.io/token", true},
		{"kubernetes.io/token", "/var/run/secrets/kubernetesXio/token", false},
	} {
		p, err := parsePattern(tc.pattern)
		if err != nil {
			t.Errorf("parsePattern(%q): %v", tc.pattern, err)
			continue
		}
		if got := p.matches(tc.path); got != tc.want {
			t.Errorf("pattern %q matches %q = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
		regex := patternsRegex([]pattern{p})
		if got := regexp.MustCompile(regex).MatchString(tc.path); got != tc.want {
			t.Errorf("pattern %q as %s matches %q = %v, want %v", tc.pattern, regex, tc.path, got, tc.want)
		}
	}
}

func TestPatternSyntaxErrors(t *testing.T) {
	for pattern, want := range map[string]string{
		"":              "empty pattern",
		"/etc/passwd":   "empty component",
		"a/":            "empty component",
		"a//b":          "empty component",
		"**":            "leading **",
		"**/x":          "leading **",
		"a/**/**":       "** next to **",
		"a/**/**/b":     "** next to **",
		"a**b":          "** inside a component",
		"a/x**":         "** inside a component",
		"id_{rsa":       "{ without }",
		"id_rsa}":       "} without {",
		"{a,{b,c}}":     "{ inside {..}",
		"{a}":           "one alternative",
		"id_[rd]sa":     "not part of the pattern syntax",
		"id_?sa":        "not part of the pattern syntax",
		`C:\Users`:      "not part of the pattern syntax",
		"!secret":       "not part of the pattern syntax",
		"a\tb":          "not part of the pattern syntax",
		"{a,b}/{c,d/e}": "{ without }", // a / splits components first
	} {
		if _, err := parsePattern(pattern); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("parsePattern(%q) = %v, want an error about %q", pattern, err, want)
		}
	}
}
