package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// freeEmailDomains are personal mailbox providers: an address at one of them says nothing about
// the person's company, so it gets no company group. gmx.* is matched by prefix. The list is
// FREE_EMAIL_DOMAINS in the websites' shared PostHog contract (posthog-handoff.ts); keep the two
// the same so a person is grouped alike wherever they are identified.
var freeEmailDomains = map[string]bool{
	"gmail.com": true, "googlemail.com": true, "outlook.com": true, "hotmail.com": true, "live.com": true, "msn.com": true,
	"yahoo.com": true, "ymail.com": true, "rocketmail.com": true, "icloud.com": true, "me.com": true, "mac.com": true,
	"proton.me": true, "protonmail.com": true, "pm.me": true, "aol.com": true, "mail.com": true, "zoho.com": true,
	"yandex.com": true, "yandex.ru": true, "qq.com": true, "163.com": true, "126.com": true, "hey.com": true, "fastmail.com": true,
	"tutanota.com": true, "duck.com": true, "hotmail.co.uk": true, "yahoo.co.uk": true, "outlook.fr": true, "hotmail.fr": true,
	"yahoo.fr": true, "web.de": true, "t-online.de": true,
}

// normalizeEmail is the form both the distinct ID and the person property use.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// DistinctIDForEmail is the PostHog distinct ID of the person with this email: the hex SHA-256 of
// the trimmed, lower-cased address, the same ID beacon.sh and asymptotelabs.ai identify them by.
func DistinctIDForEmail(email string) string {
	sum := sha256.Sum256([]byte(normalizeEmail(email)))
	return hex.EncodeToString(sum[:])
}

// emailDomain returns the lower-cased domain of an email, or "".
func emailDomain(email string) string {
	_, domain, ok := strings.Cut(normalizeEmail(email), "@")
	if !ok {
		return ""
	}
	return domain
}

// IsFreeEmailDomain reports whether an email is at a personal mailbox provider.
func IsFreeEmailDomain(email string) bool {
	domain := emailDomain(email)
	return freeEmailDomains[domain] || strings.HasPrefix(domain, "gmx.")
}

// CompanyDomain is the company group key for an email: its domain, or "" for a personal mailbox
// provider.
func CompanyDomain(email string) string {
	domain := emailDomain(email)
	if domain == "" || IsFreeEmailDomain(email) {
		return ""
	}
	return domain
}
