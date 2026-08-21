package onboarding

import (
	"errors"
	"strings"
)

// Domain classifications reported alongside a submitted address.
const (
	DomainCorporate = "corporate"
	DomainFree      = "free"
	DomainUnknown   = "unknown"
)

// Usage values describe how the operator is running Beacon.
const (
	UsageWork       = "work"
	UsagePersonal   = "personal"
	UsageEvaluating = "evaluating"
)

// ValidUsages lists the accepted usage values in menu order.
var ValidUsages = []string{UsageWork, UsagePersonal, UsageEvaluating}

// UsageLabels are the human-facing menu entries, indexed alongside ValidUsages.
var UsageLabels = []string{
	"Work — at my company",
	"Personal projects",
	"Evaluating for my team",
}

// NormalizeUsage maps loose input onto a canonical usage value.
func NormalizeUsage(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "work", "company", "1":
		return UsageWork, true
	case "personal", "2":
		return UsagePersonal, true
	case "evaluating", "evaluation", "team", "3":
		return UsageEvaluating, true
	default:
		return "", false
	}
}

// Email validation is deliberately hand-written rather than a single regex. The
// point of this check is to produce a message that tells the user what to fix, and a
// regex can only ever say "no".
var (
	ErrEmailEmpty         = errors.New("an email address is required")
	ErrEmailNoAt          = errors.New("an email address needs an @ (for example: you@company.com)")
	ErrEmailMultipleAt    = errors.New("an email address can only contain one @")
	ErrEmailNoLocalPart   = errors.New("there's nothing before the @")
	ErrEmailLocalTooLong  = errors.New("the part before the @ is too long")
	ErrEmailLocalBadChar  = errors.New("the part before the @ contains a space or control character")
	ErrEmailNoDomain      = errors.New("there's nothing after the @")
	ErrEmailDomainTooLong = errors.New("the domain is too long")
	ErrEmailNoDot         = errors.New("the domain needs a dot (for example: company.com)")
	ErrEmailEmptyLabel    = errors.New("the domain has an empty part, like a doubled or trailing dot")
	ErrEmailBadTLD        = errors.New("the domain doesn't end in a valid extension")
	ErrEmailPlaceholder   = errors.New("that looks like a placeholder address")
)

const (
	maxLocalPart = 64
	maxDomain    = 255
)

// placeholderDomains are domains that cannot belong to a real mailbox. Accepting one
// would mean the lead list quietly fills with rows nobody can act on.
//
// This list is deliberately short and limited to reserved names (RFC 2606 and RFC
// 6761) plus documentation stand-ins. Guessing that a registered domain "looks fake"
// is not worth it: a wrong guess hard-blocks a real user behind an error message,
// while a junk row only costs us a filter downstream. The server-side MX check is the
// right place to catch domains that merely cannot receive mail.
var placeholderDomains = map[string]bool{
	"example.com":    true,
	"example.net":    true,
	"example.org":    true,
	"example.edu":    true,
	"yourdomain.com": true,
	"mydomain.com":   true,
	"domain.tld":     true,
	"email.tld":      true,
}

// reservedTLDs can never resolve to a real mailbox (RFC 2606, RFC 6761).
var reservedTLDs = map[string]bool{
	"test":      true,
	"invalid":   true,
	"localhost": true,
	"example":   true,
	"local":     true,
}

// freeProviders are consumer mailbox providers. An address here is not worth less —
// plenty of contractors and solo developers use one — but it does mean the domain
// says nothing about which company the user belongs to, so per-domain rate limits and
// company attribution both have to treat it differently.
var freeProviders = map[string]bool{
	"gmail.com":      true,
	"googlemail.com": true,
	"outlook.com":    true,
	"hotmail.com":    true,
	"hotmail.co.uk":  true,
	"live.com":       true,
	"msn.com":        true,
	"yahoo.com":      true,
	"yahoo.co.uk":    true,
	"ymail.com":      true,
	"icloud.com":     true,
	"me.com":         true,
	"mac.com":        true,
	"aol.com":        true,
	"proton.me":      true,
	"protonmail.com": true,
	"pm.me":          true,
	"gmx.com":        true,
	"gmx.de":         true,
	"web.de":         true,
	"mail.com":       true,
	"zoho.com":       true,
	"yandex.com":     true,
	"yandex.ru":      true,
	"fastmail.com":   true,
	"hey.com":        true,
	"tutanota.com":   true,
	"tuta.io":        true,
	"qq.com":         true,
	"163.com":        true,
	"126.com":        true,
	"naver.com":      true,
	"duck.com":       true,
}

// NormalizeEmail validates an address and returns it in canonical lowercase form.
//
// The returned error is meant to be shown to the user verbatim, so each failure mode
// gets its own message.
func NormalizeEmail(input string) (string, error) {
	email := strings.TrimSpace(input)
	// People paste addresses out of mail clients, which often wrap them.
	email = strings.Trim(email, "<>")
	email = strings.TrimSpace(email)
	if email == "" {
		return "", ErrEmailEmpty
	}

	at := strings.Count(email, "@")
	switch at {
	case 0:
		return "", ErrEmailNoAt
	case 1:
	default:
		return "", ErrEmailMultipleAt
	}

	local, domain, _ := strings.Cut(email, "@")
	if local == "" {
		return "", ErrEmailNoLocalPart
	}
	if len(local) > maxLocalPart {
		return "", ErrEmailLocalTooLong
	}
	for _, r := range local {
		if r <= ' ' || r == 0x7f {
			return "", ErrEmailLocalBadChar
		}
	}

	domain = strings.ToLower(domain)
	if domain == "" {
		return "", ErrEmailNoDomain
	}
	if len(domain) > maxDomain {
		return "", ErrEmailDomainTooLong
	}
	if !strings.Contains(domain, ".") {
		return "", ErrEmailNoDot
	}
	labels := strings.Split(domain, ".")
	for _, label := range labels {
		if label == "" {
			return "", ErrEmailEmptyLabel
		}
		for _, r := range label {
			isLower := r >= 'a' && r <= 'z'
			isDigit := r >= '0' && r <= '9'
			if !isLower && !isDigit && r != '-' {
				return "", ErrEmailBadTLD
			}
		}
	}
	tld := labels[len(labels)-1]
	if len(tld) < 2 {
		return "", ErrEmailBadTLD
	}
	for _, r := range tld {
		if r < 'a' || r > 'z' {
			return "", ErrEmailBadTLD
		}
	}

	if placeholderDomains[domain] || reservedTLDs[tld] {
		return "", ErrEmailPlaceholder
	}

	return strings.ToLower(local) + "@" + domain, nil
}

// EmailDomain returns the domain of an already-normalized address.
func EmailDomain(email string) string {
	_, domain, found := strings.Cut(email, "@")
	if !found {
		return ""
	}
	return domain
}

// ClassifyDomain reports whether a domain belongs to a consumer mailbox provider.
func ClassifyDomain(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return DomainUnknown
	}
	if freeProviders[domain] {
		return DomainFree
	}
	return DomainCorporate
}
