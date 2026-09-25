//go:build !windows

package handoff

func envNameEqual(a, b string) bool { return a == b }
