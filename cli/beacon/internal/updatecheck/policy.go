package updatecheck

import "strings"

// CanCheckVersion reports whether a build version can be compared to releases.
func CanCheckVersion(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || v == "dev" {
		return false
	}
	_, ok := parseReleaseVersion(v)
	return ok
}
