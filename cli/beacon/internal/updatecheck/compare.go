package updatecheck

import (
	"strconv"
	"strings"
)

type releaseVersion struct {
	major int
	minor int
	patch int
}

func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	return v
}

func displayVersion(v string) string {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" {
		return ""
	}
	normalized := normalizeVersion(trimmed)
	if _, ok := parseReleaseVersion(normalized); !ok {
		return trimmed
	}
	return "v" + normalized
}

func parseReleaseVersion(v string) (releaseVersion, bool) {
	v = normalizeVersion(v)
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return releaseVersion{}, false
	}
	var parsed [3]int
	for i, part := range parts {
		if part == "" {
			return releaseVersion{}, false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return releaseVersion{}, false
			}
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return releaseVersion{}, false
		}
		parsed[i] = n
	}
	return releaseVersion{major: parsed[0], minor: parsed[1], patch: parsed[2]}, true
}

func compareVersions(current, latest string) (int, bool) {
	cur, ok := parseReleaseVersion(current)
	if !ok {
		return 0, false
	}
	next, ok := parseReleaseVersion(latest)
	if !ok {
		return 0, false
	}
	switch {
	case cur.major != next.major:
		return compareInts(cur.major, next.major), true
	case cur.minor != next.minor:
		return compareInts(cur.minor, next.minor), true
	default:
		return compareInts(cur.patch, next.patch), true
	}
}

func compareInts(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
