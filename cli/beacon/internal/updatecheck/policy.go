package updatecheck

import "strings"

// Older reports whether release version a is older than release version b; false when either is
// not a release version.
func Older(a, b string) bool {
	cmp, ok := compareVersions(a, b)
	return ok && cmp < 0
}

// CanCheckVersion reports whether a build version can be compared to releases.
func CanCheckVersion(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || v == "dev" {
		return false
	}
	_, ok := parseReleaseVersion(v)
	return ok
}
