package onboarding

import (
	"errors"
	"testing"
)

func TestNormalizeEmailAccepts(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", "shukan@asymptotelabs.ai", "shukan@asymptotelabs.ai"},
		{"uppercase domain", "shukan@AsymptoteLabs.AI", "shukan@asymptotelabs.ai"},
		{"uppercase local", "Shukan.Shah@asymptotelabs.ai", "shukan.shah@asymptotelabs.ai"},
		{"surrounding space", "  shukan@asymptotelabs.ai  ", "shukan@asymptotelabs.ai"},
		{"angle brackets from a mail client", "<shukan@asymptotelabs.ai>", "shukan@asymptotelabs.ai"},
		{"plus addressing", "shukan+beacon@asymptotelabs.ai", "shukan+beacon@asymptotelabs.ai"},
		{"subdomain", "ops@mail.corp.asymptotelabs.ai", "ops@mail.corp.asymptotelabs.ai"},
		{"hyphenated domain", "dev@some-company.io", "dev@some-company.io"},
		{"long tld", "dev@company.engineering", "dev@company.engineering"},
		{"digits in domain", "dev@cloud123.com", "dev@cloud123.com"},
		{"free provider is still valid", "someone@gmail.com", "someone@gmail.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeEmail(tc.input)
			if err != nil {
				t.Fatalf("NormalizeEmail(%q) returned error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeEmail(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestNormalizeEmailRejects(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  error
	}{
		{"empty", "", ErrEmailEmpty},
		{"whitespace only", "   ", ErrEmailEmpty},
		{"no at", "shukan.asymptotelabs.ai", ErrEmailNoAt},
		{"two ats", "shukan@@asymptotelabs.ai", ErrEmailMultipleAt},
		{"no local part", "@asymptotelabs.ai", ErrEmailNoLocalPart},
		{"no domain", "shukan@", ErrEmailNoDomain},
		{"no dot in domain", "shukan@localhost", ErrEmailNoDot},
		{"trailing dot", "shukan@asymptotelabs.ai.", ErrEmailEmptyLabel},
		{"doubled dot", "shukan@asymptote..ai", ErrEmailEmptyLabel},
		{"single character tld", "shukan@company.a", ErrEmailBadTLD},
		{"numeric tld", "shukan@company.12", ErrEmailBadTLD},
		{"space in local part", "shukan shah@company.com", ErrEmailLocalBadChar},
		{"rfc2606 example domain", "shukan@example.com", ErrEmailPlaceholder},
		{"reserved test tld", "shukan@company.test", ErrEmailPlaceholder},
		{"reserved invalid tld", "shukan@company.invalid", ErrEmailPlaceholder},
		{"documentation stand-in", "you@yourdomain.com", ErrEmailPlaceholder},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeEmail(tc.input)
			if err == nil {
				t.Fatalf("NormalizeEmail(%q) = %q, want error %v", tc.input, got, tc.want)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("NormalizeEmail(%q) error = %v, want %v", tc.input, err, tc.want)
			}
		})
	}
}

func TestNormalizeEmailRejectsOverlongParts(t *testing.T) {
	long := make([]byte, maxLocalPart+1)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := NormalizeEmail(string(long) + "@company.com"); !errors.Is(err, ErrEmailLocalTooLong) {
		t.Fatalf("overlong local part error = %v, want %v", err, ErrEmailLocalTooLong)
	}
}

// A registered domain that merely looks suspicious must still be accepted. A wrong
// guess hard-blocks a real user; a junk row only costs a downstream filter.
func TestNormalizeEmailAcceptsRealButOddDomains(t *testing.T) {
	for _, input := range []string{"dev@test.com", "dev@foo.com", "dev@company.com", "dev@asdf.com"} {
		if _, err := NormalizeEmail(input); err != nil {
			t.Fatalf("NormalizeEmail(%q) returned error %v, want it accepted", input, err)
		}
	}
}

func TestEmailDomain(t *testing.T) {
	if got := EmailDomain("shukan@asymptotelabs.ai"); got != "asymptotelabs.ai" {
		t.Fatalf("EmailDomain() = %q, want %q", got, "asymptotelabs.ai")
	}
	if got := EmailDomain("not-an-email"); got != "" {
		t.Fatalf("EmailDomain() = %q for a malformed address, want empty", got)
	}
}

func TestClassifyDomain(t *testing.T) {
	cases := []struct {
		domain string
		want   string
	}{
		{"asymptotelabs.ai", DomainCorporate},
		{"some-startup.io", DomainCorporate},
		{"gmail.com", DomainFree},
		{"GMAIL.COM", DomainFree},
		{"icloud.com", DomainFree},
		{"proton.me", DomainFree},
		{"", DomainUnknown},
		{"   ", DomainUnknown},
	}
	for _, tc := range cases {
		if got := ClassifyDomain(tc.domain); got != tc.want {
			t.Fatalf("ClassifyDomain(%q) = %q, want %q", tc.domain, got, tc.want)
		}
	}
}

func TestNormalizeUsage(t *testing.T) {
	cases := []struct {
		input string
		want  string
		ok    bool
	}{
		{"1", UsageWork, true},
		{"work", UsageWork, true},
		{"WORK", UsageWork, true},
		{" 2 ", UsagePersonal, true},
		{"personal", UsagePersonal, true},
		{"3", UsageEvaluating, true},
		{"evaluating", UsageEvaluating, true},
		{"team", UsageEvaluating, true},
		{"", "", false},
		{"4", "", false},
		{"maybe", "", false},
	}
	for _, tc := range cases {
		got, ok := NormalizeUsage(tc.input)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("NormalizeUsage(%q) = (%q, %t), want (%q, %t)", tc.input, got, ok, tc.want, tc.ok)
		}
	}
}

// The menu prints UsageLabels against ValidUsages by index, so they must stay aligned.
func TestUsageLabelsMatchValidUsages(t *testing.T) {
	if len(UsageLabels) != len(ValidUsages) {
		t.Fatalf("len(UsageLabels) = %d, len(ValidUsages) = %d; they are indexed together", len(UsageLabels), len(ValidUsages))
	}
	for i, usage := range ValidUsages {
		if _, ok := NormalizeUsage(usage); !ok {
			t.Fatalf("ValidUsages[%d] = %q is not accepted by NormalizeUsage", i, usage)
		}
	}
}
